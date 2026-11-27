# Hotmail Cookie Checker — Architecture Plan

Telegram bot that validates Microsoft/Hotmail session cookies in bulk, classifies each as live/dead/error, and returns email + display name for live hits. Deployed on Heroku.

---

## 1. What Heroku changes

The first-pass design assumed a VPS with a disk. Heroku has an ephemeral filesystem — every dyno restart wipes it, and Heroku restarts dynos at least once a day. Anything written to disk is gone.

| Original | Heroku reality | Replacement |
|---|---|---|
| `bot.db` (SQLite) | wiped daily | Heroku Postgres (asyncpg) |
| `hits.txt` / `dead.txt` | wiped daily | Postgres rows + in-memory file upload to Telegram |
| proxy health file | wiped daily | `proxies` table |
| Redis queue | unnecessary at one dyno | dropped entirely |
| webhook server | needs a web dyno + port binding | dropped — polling on a worker dyno |

Net effect: fewer moving parts, one database, zero disk dependencies.

---

## 2. Runtime topology

```
Telegram ──getUpdates──► worker dyno
                            │
                            ├─ aiogram dispatcher
                            ├─ asyncio.Queue (in-process, per-user fair)
                            └─ N concurrent check tasks (N=10 default)
                                   │
                                   ├─ curl_cffi session (TLS impersonation)
                                   ├─ proxy from Postgres pool
                                   └─ verdict
                                          │
                      ┌───────────────────┼───────────────────┐
                      ▼                   ▼                   ▼
                 Postgres             hit row            BytesIO file
              (jobs, credits)      (email, name)      → Telegram document
```

One process. One dyno. No Redis, no worker pool service, no web server.

---

## 3. Dyno decision

| Option | Cost | Verdict |
|---|---|---|
| **Basic worker, polling** | $7/mo | **recommended** — always on, no cold start, no port |
| Eco worker, polling | $5/mo (1000 hrs pooled) | works, 720 hrs/mo fits; subject to Eco throttling |
| Web dyno + webhook | $5–7/mo | web dynos sleep on Eco → webhook timeouts; rejected |

Polling beats webhook here because there is no inbound HTTPS requirement, no `$PORT` binding, and no cold-start failure mode.

### The polling scaling trap

`heroku ps:scale worker=2` **breaks the bot.** Telegram allows exactly one `getUpdates` consumer per token — a second instance gets `409 Conflict` and both start dropping updates.

Throughput comes from **asyncio concurrency inside the single dyno**, not from dyno count. One worker with 10 concurrent checks saturates a residential proxy pool anyway. If more is ever needed, the path is webhook mode with a signed secret path and a web dyno — not a second poller.

Documented here so it does not get "fixed" into a broken state later.

---

## 4. Cost

| Item | Plan | Monthly |
|---|---|---|
| Worker dyno | Basic | $7 |
| Postgres | Essential-0 (1 GB) | $5 |
| Scheduler addon | free | $0 |
| Proxies | external provider | variable |
| **Baseline** | | **$12/mo** |

Cheaper path: keep the Basic dyno and point `DATABASE_URL` at an external free Postgres (Neon, Supabase, Aiven). Drops baseline to $7/mo. The app only reads one env var, so this is a config change, not a code change.

---

## 5. Data layer

Postgres via `asyncpg`, connection pool of 5. `release` phase runs migrations before each deploy.

```sql
CREATE TABLE users (
  tg_id       BIGINT PRIMARY KEY,
  username    TEXT,
  credits     INT NOT NULL DEFAULT 0,
  banned      BOOLEAN NOT NULL DEFAULT FALSE,
  joined_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
  id          BIGSERIAL PRIMARY KEY,
  user_id     BIGINT NOT NULL REFERENCES users(tg_id) ON DELETE CASCADE,
  total       INT NOT NULL,
  live        INT NOT NULL DEFAULT 0,
  dead        INT NOT NULL DEFAULT 0,
  err         INT NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'queued',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ
);

CREATE TABLE hits (
  id           BIGSERIAL PRIMARY KEY,
  job_id       BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  user_id      BIGINT NOT NULL,
  email        TEXT NOT NULL,
  display_name TEXT,
  cookie_blob  TEXT NOT NULL,
  proxy_used   TEXT,
  checked_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE proxies (
  id        BIGSERIAL PRIMARY KEY,
  url       TEXT NOT NULL UNIQUE,
  fails     INT NOT NULL DEFAULT 0,
  last_used TIMESTAMPTZ,
  enabled   BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX ON hits (user_id);
CREATE INDEX ON jobs (user_id, created_at DESC);
```

Hit retention: Scheduler runs a daily job trimming `hits` older than N days. Postgres Essential-0 is 1 GB — plenty for six figures of hits at current row size.

---

## 6. File delivery without a disk

Results are built in memory and pushed straight to Telegram:

```python
buf = io.BytesIO(report_text.encode("utf-8"))
await message.answer_document(
    BufferedInputFile(buf.getvalue(), filename=f"hits_{job_id}.txt"),
    caption=f"{live} live / {dead} dead / {err} error",
)
```

No temporary files, no write permissions, nothing to lose on restart. Admin `/export` regenerates the full hit dump from Postgres the same way.

---

## 7. Checker core

Unchanged from the first pass — the validation logic is where checkers succeed or fail.

```python
async with AsyncSession(impersonate="chrome124", proxies=..., timeout=30,
                        follow_redirects=False) as s:
    s.cookies.update(cookies)
    r = await s.get("https://outlook.live.com/mail/0/inbox")
```

Two non-negotiables:

1. **`impersonate="chrome124"`** — Microsoft's edge fingerprints TLS. `requests` and plain `httpx` return 403 or a challenge on every request. `curl_cffi` ships `libcurl-impersonate` and matches Chrome's JA3/JA4.
2. **No redirect following** — the `Location` header is the signal. Following it hides whether the session landed in the inbox or bounced to `login.live.com`.

Classification:

| Signal | Verdict |
|---|---|
| `302` → `login.live.com` / `signin` | DEAD |
| `401` | DEAD |
| `429` | RATE_LIMITED → requeue on fresh proxy |
| `200` + email in page | LIVE |
| `200` + no email | fall back to OAuth flow → substrate profile |
| timeout / connection error | ERROR |

### Build risk: curl_cffi on the Heroku stack

`curl_cffi` publishes `manylinux_2_17` wheels that bundle libcurl-impersonate. On heroku-24 (Ubuntu 24.04, glibc 2.39) those wheels load fine. The failure case is a Python version with no matching wheel — pip then attempts a source build, which needs the full curl-impersonate toolchain and will fail the slug build.

Mitigation: pin `.python-version` to `3.12` and pin `curl_cffi` to a release that ships a `cp312` manylinux wheel. Verify with a clean slug build before trusting it — this is the single highest deployment risk in the project.

---

## 8. Proxy layer

Proxies are the fuel, not an optimization. Microsoft 429s datacenter ranges within minutes.

| Control | Value |
|---|---|
| Concurrency per proxy | 3–5 |
| Inter-check delay | 0.4–1.2s, jittered per worker |
| Rotation | round-robin, sticky per job, drop after 3 fails |
| Session reuse | never — fresh cookie jar per account |
| Supply | `PROXY_LIST_URL` (remote) or `proxies` table (admin `/proxies add`) |

Mobile/residential survives; datacenter dies. No proxy means the bot technically runs and practically returns all-error.

---

## 9. Config vars

```
BOT_TOKEN           Telegram bot token        (secret)
ADMIN_IDS           comma-separated tg IDs
DATABASE_URL        injected by Heroku Postgres
PROXY_LIST_URL      optional remote proxy source
CHECK_CONCURRENCY   default 10
CHECK_TIMEOUT       default 30
DEFAULT_CREDITS     signup grant, default 0
LOG_LEVEL           default INFO
```

Nothing secret in the repo. `.env.example` documents the set; `.env` is gitignored.

---

## 10. Deploy runbook

```bash
heroku create <app-name> --stack heroku-24
heroku buildpacks:set heroku/python
heroku addons:create heroku-postgresql:essential-0
heroku config:set BOT_TOKEN=... ADMIN_IDS=... PROXY_LIST_URL=...
git push heroku main
heroku ps:scale worker=1 web=0
heroku logs --tail
```

`Procfile`:

```
release: python -m app.db.migrate
worker:  python -m app.bot
```

`release` migrates the schema before the new dyno boots. If a migration fails, the release aborts and the old dyno keeps serving — that is the correct failure mode.

Scheduler addon runs three housekeeping jobs:

```
every 10 min   python -m app.tasks.proxy_health
daily 04:00    python -m app.tasks.prune
daily 04:10    python -m app.tasks.refresh_proxies
```

---

## 11. Project layout

```
hotmail-checker/
├── Procfile
├── app.json
├── requirements.txt
├── .python-version
├── .env.example
├── PLAN.md
├── README.md
└── app/
    ├── __init__.py
    ├── config.py            pydantic-settings, env parsing
    ├── bot.py               aiogram entry, polling, routers
    ├── handlers/
    │   ├── start.py         menu, help, stats
    │   ├── check.py         cookie intake, progress, results
    │   └── admin.py         credits, ban, proxies, export
    ├── core/
    │   ├── parser.py        raw / netscape / json cookie parsing
    │   ├── checker.py       validation + classification
    │   ├── identity.py      email + display name extraction
    │   └── proxy.py         rotation, health, failover
    ├── db/
    │   ├── pool.py          asyncpg pool
    │   ├── migrate.py       release-phase schema apply
    │   └── queries.py       all SQL in one place
    └── tasks/
        ├── proxy_health.py
        ├── prune.py
        └── refresh_proxies.py
```

---

## 12. Bot surface

```
/start          welcome, inline menu
<cookie paste>  queue, live progress edit, result file
/credits        balance
/stats          personal check history

admin:
/stats_all      users, checks, hit rate, proxy health
/addcredits <tg_id> <n>
/ban /unban <tg_id>
/proxies add|list|test
/export         full hit dump
/broadcast <msg>
```

Progress message edits on a 3-second throttle — Telegram rate-limits edits to roughly one per second per chat, so editing on every result would get the bot throttled.

---

## 13. Build order

1. `core/parser.py` + fixtures — three cookie formats, reject reasons
2. `core/checker.py` — verify against a real account's own cookies; confirm zero false LIVE
3. `core/proxy.py` — rotation and failover against a test pool
4. `db/` — schema, pool, migrations, queries
5. `handlers/` — intake, progress streaming, in-memory file delivery
6. `handlers/admin.py` + `tasks/` — credits, bans, scheduler jobs
7. Heroku deploy — clean slug build, confirm `curl_cffi` wheel, tail logs

Each stage ships runnable. No stage leaves the repo broken.

---

## 14. Open decisions

1. **Proxy supply** — residential, mobile, or datacenter? Determines whether the checker can find anything at all.
2. **Access model** — open to all, invite code, or credit system with `/redeem`?
3. **Postgres** — Heroku Essential-0 ($5) or external free tier (Neon/Supabase, $0)?
4. **Hit retention** — how long to keep rows before the daily prune?

Defaults ready if unset: credit system with 10-credit signup grant, Heroku Postgres, 30-day retention.
