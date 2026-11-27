# Hotmail Cookie Checker — Go / Heroku

Telegram bot that validates Microsoft consumer session cookies in bulk, returns the ones that still work as a downloadable file, reports identity for each live account, and searches live inboxes for keywords. Users can attach their own proxy lists so their checks run on their own IPs.

Free for everyone. One Heroku Eco worker dyno. No mandatory database.

---

## 1. The two things that decide whether a checker works

Both were verified empirically before any bot code was written.

### 1.1 TLS fingerprint

Microsoft's edge inspects the TLS ClientHello. `net/http`, `reqwest` and `requests` all produce a non-browser fingerprint and get 403 or a challenge page. This project uses `bogdanfinn/tls-client`, which wraps uTLS and reproduces Chrome's JA3/JA4 and extension ordering.

Profile: `Chrome_124`, with `WithRandomTLSExtensionOrder()`.

### 1.2 The probe endpoint

`https://outlook.live.com/mail/0/inbox` **returns HTTP 200 with a login shell to everyone**, authenticated or not. Measured against a deliberately invalid cookie:

```
GET https://outlook.live.com/mail/0/inbox  →  200, 14150 bytes, no auth
```

A status-code checker reports every cookie as live. Endpoints probed with an invalid cookie:

| Endpoint | Invalid cookie | Useful |
|---|---|---|
| `outlook.live.com/mail/0/inbox` | 200, 14 KB SPA shell | no |
| `outlook.live.com/owa/` | 302 → `/mail/` | no |
| `outlook.live.com/owa/service.svc?action=GetOwaUserConfiguration` | **401** | yes |
| `substrate.office.com/profileb2/v2/me/V2` | **401** | yes (needs Bearer) |
| `login.live.com/oauth20_authorize.srf?...&prompt=none` | **302 → `#error=login_required`** | **yes, primary** |

The silent-OAuth endpoint answers one unambiguous question and hands back a token in the same response:

- valid session → `302` to `oauth20_desktop.srf#access_token=…`
- invalid session → `302` to `oauth20_desktop.srf#error=login_required`
- valid but needs consent → `#error=interaction_required`

---

## 2. Verification flow

```
cookie set
   │
   ├─ GET silentOAuth (prompt=none, MBI_SSL scope)
   │      ├─ #access_token=…            → LIVE → substrate profile → email + name
   │      ├─ #error=login_required      → DEAD
   │      ├─ #error=interaction_required
   │      │  #error=consent_required    → CHALLENGED
   │      └─ ambiguous / client error   → fall through
   │
   └─ GET owa/service.svc (GetOwaUserConfiguration)
          ├─ 200 (+ identity parsed)    → LIVE
          ├─ 401 / 403                  → DEAD
          ├─ 429                        → RATE_LIMITED
          └─ other                      → ERROR
```

| Verdict | Meaning |
|---|---|
| `live` | session works, mail accessible |
| `dead` | session expired or invalid |
| `challenged` | session is real but consent / re-auth is required |
| `rate-limited` | proxy or IP is being throttled |
| `error` | network, proxy, TLS, or unexpected status |

`challenged` exists because collapsing it into `dead` undercounts valid accounts, and collapsing it into `live` produces cookies that do not work.

---

## 3. No batch limit

`MAX_COOKIES_PER_JOB` defaults to `0`, meaning unlimited. The real bound is the 20 MB Telegram upload cap — roughly 10,000 accounts.

Two guards remain, both about fairness rather than size:

- **One running job per user** — prevents one person monopolising the worker.
- **10 s cooldown between jobs** — prevents restart-spam.

Memory stays flat because dead cookies are reduced to a short label before storage; only live hits keep their full cookie map, and those are needed for search.

### 3.1 ZIP intake

A single upload can be a raw list, a text file, or a ZIP of logs. Every text entry is parsed and merged, then deduplicated by a hash of the normalised cookie header, so overlapping logs collapse to one entry per session.

Bounds, because a ZIP is attacker-controlled:

| Limit | Value |
|---|---|
| Files | 500 |
| Per-file size | 16 MB |
| Total uncompressed | 128 MB |
| Skipped | binaries, `__MACOSX`, path traversal |

Nothing is written to disk — entries are read straight from memory — so extraction cannot escape anywhere. Skipped entries are reported back to the user rather than silently dropped.

---

## 4. Per-user proxy lists

Each user can attach their own proxy list. Their checks and searches run through it; the shared pool is only a fallback.

```
/proxies add      paste or upload a list
/proxies list     indexed, credentials masked
/proxies test     reachability + latency per proxy
/proxies del n    remove one
/proxies clear    remove all
```

### 4.1 Accepted formats

All parsed by one normaliser, mixed freely in a single paste, separated by newlines, commas or semicolons:

```
1.2.3.4:8080                  → http://1.2.3.4:8080
1.2.3.4:8080:user:pass        → http://user:pass@1.2.3.4:8080
user:pass@1.2.3.4:8080        → http://user:pass@1.2.3.4:8080
socks5://1.2.3.4:1080         → unchanged
socks5://user:pass@1.2.3.4:1080
http://1.2.3.4:8080
https://1.2.3.4:8443
[2001:db8::1]:8080            → http://[2001:db8::1]:8080
```

Ambiguous forms are rejected rather than guessed — `host:port:user` (three fields) could be either shape, so it fails loudly instead of silently misparsing credentials.

### 4.2 Persistence

Heroku's filesystem is ephemeral, so user lists need a store.

| Setup | Behaviour |
|---|---|
| `DATABASE_URL` set | lists persist in Postgres (`user_proxies` table, created on boot) |
| `DATABASE_URL` unset | lists live in memory only; the bot says so in `/stats` and `/proxies` |

Neon or Supabase free tier is enough — the table holds a few hundred rows. Heroku Postgres Essential-0 also works but adds $5/mo.

Deployment is wired to a Neon pooler URL. Neon appends `channel_binding=require`, which pgx forwards as a server startup option and Postgres then rejects, so `sanitiseDSN` strips it before parsing. Verified end to end with:

```bash
bin/hotmail-checker -dbcheck
# connected, schema applied
# user_proxies rows: 10
```

Schema:

```sql
CREATE TABLE IF NOT EXISTS user_proxies (
    user_id  BIGINT      NOT NULL,
    proxy    TEXT        NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, proxy)
);
```

Applied with `CREATE TABLE IF NOT EXISTS` on connect, so there is no release-phase migration to fail.

### 4.3 Resolution at check time

```
user has saved proxies  → their pool
otherwise               → shared pool
their pool is exhausted → fall back to shared pool
neither                 → direct
```

Per-proxy concurrency stays at 4, so a user with 5 working proxies gets roughly 20 simultaneous checks — the same ceiling as the shared pool, without the contention.

---

## 5. Inbox search

After a check completes, every live account in the batch is searched for the user's keyword.

### 5.1 Scope research

Four scopes were tested against the OAuth client with an invalid cookie. All returned `login_required` rather than `invalid_scope`, confirming the client is registered for each:

| Scope | Response |
|---|---|
| `service::outlook.office.com::MBI_SSL` | `login_required` ✓ |
| `https://graph.microsoft.com/Mail.Read` | `login_required` ✓ |
| `https://outlook.office.com/mail.read` | `login_required` ✓ |
| Graph + REST combined | `login_required` ✓ |

Endpoint shapes confirmed reachable: Graph returns `401 InvalidAuthenticationToken`, REST `401`, OWA `FindItem` `401`.

### 5.2 Token chain

```
Graph token      → graph.microsoft.com/v1.0/me/messages?$search="term"
REST (graph tok) → outlook.office.com/api/v2.0/me/messages?$search="term"
REST (mail tok)  → same endpoint, mail-scope token
```

An account without Graph consent still returns results through REST. If all three fail, that account reports `search unavailable` — it never contaminates the check verdict.

### 5.3 Output

Every job produces three artefacts:

| File | Contents |
|---|---|
| `cookies_<id>.txt` | working cookies only, one per line, drop-in |
| `report_<id>.txt` | per-account detail with search matches inline |
| `results_<id>.zip` | `report.txt`, `cookies.txt`, `accounts.csv`, `dead.txt`, `search.csv` |

The zip is sent when a search ran or the batch exceeds 50 accounts, so small jobs stay to two files.

Report shape:

```
# hotmail-checker results
# job       1234-5678
# source    logs.zip (14 file(s) in zip)
# checked   1840
# live      42
...

## LIVE

### 1. user@hotmail.com
status : live
name   : Jane Doe
proxy  : 1.2.3.4:8080
search : 3 match(es) for "invoice" via graph

  2026-11-20 14:32 | billing@vendor.com | Invoice #4491 — payment due
      Hi, attached is invoice 4491 for the period ending Nov 30…
      https://outlook.live.com/mail/0/inbox/id/...

## CHALLENGED — session valid, needs consent or re-auth
## DEAD
## ERRORS (grouped)
```

`accounts.csv` is `email,name,status,proxy,search_matches`; `search.csv` is `email,date,from,subject,link`.

### 5.4 Caps

| Limit | Default | Env |
|---|---|---|
| Accounts searched per job | all live | `MAX_SEARCH_ACCOUNTS` (0 = unlimited) |
| Messages per account | 25 | `SEARCH_RESULTS_LIMIT` |
| Search term length | 120 chars | `MAX_SEARCH_TERM_LENGTH` |
| Concurrent searches | 3 | — |

Only live hits consume a search slot, so a 10,000-cookie zip with 40 live accounts performs 40 searches.

---

## 6. Architecture

```
Telegram ──long poll──► worker dyno (single process)
                           │
                           ├─ gotgbot dispatcher
                           ├─ in-memory job registry (one job per user)
                           ├─ pending map (armed search / armed proxy add, 5 min TTL)
                           ├─ proxy.Manager
                           │     ├─ per-user pools  (DATABASE_URL → Postgres)
                           │     └─ shared pool     (PROXY_LIST / PROXY_LIST_URL)
                           ├─ N goroutine workers (default 20)
                           │     ├─ per-proxy semaphore, 3-fail quarantine
                           │     └─ tls-client pool (one client per proxy)
                           │
                           ├─ check phase → verdicts
                           ├─ search phase (only if armed) → matches
                           │
                           └─ cookies_<job>.txt · report_<job>.txt · search_<job>.txt
```

No hit archive. Telegram is the store — a delivered file stays in the chat, so the user can re-download whenever they want.

In-flight jobs live in a mutex-guarded map. A dyno restart drops them; the user resends. Proxy lists survive if `DATABASE_URL` is set, and only those.

---

## 7. Dyno and cost

| Item | Plan | Monthly |
|---|---|---|
| Worker dyno | Eco (1000 hrs pooled) | $5 |
| Database | Neon free tier (optional) | $0 |
| Redis | none | $0 |
| **Total** | | **$5** |

One worker at 24/7 uses 720 of 1000 pooled Eco hours, leaving ~280 spare.

Polling keeps a long-poll connection open every 25–30 seconds, which counts as activity and should prevent the Eco dyno from idling. Confirm in the first 24 hours.

### Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token — the second instance gets `409 Conflict` and both drop updates.

---

## 8. Project layout

```
hotmail-checker/
├── main.go                     entrypoint + -selftest
├── deploy.sh                   one-command Heroku deploy
├── Procfile                    worker: bin/hotmail-checker
├── app.json                    Heroku button manifest (eco worker, go buildpack)
├── .env.example
├── go.mod / go.sum
├── internal/
│   ├── config/config.go        env parsing + limits
│   ├── archive/                zip extract/build with bomb limits + tests
│   ├── checker/
│   │   ├── parse.go            raw / netscape / json cookie parsing
│   │   ├── parse_test.go
│   │   ├── identity.go         email + display name extraction
│   │   ├── check.go            OAuth probe, fallback probe, verdicts
│   │   └── search.go           Graph + REST mail search, token chain
│   ├── proxy/
│   │   ├── parse.go            all-format normaliser + Label
│   │   ├── parse_test.go
│   │   ├── pool.go             rotation, semaphores, quarantine
│   │   └── manager.go          per-user pools + store wiring
│   ├── store/postgres.go       optional user_proxies persistence
│   ├── job/registry.go         job lifecycle, per-user limits
│   └── bot/
│       ├── bot.go              dispatcher, polling, pending state
│       ├── commands.go         /start /search /cancel /stats
│       ├── proxies.go          /proxies add|list|test|del|clear
│       ├── intake.go           paste / txt / zip → cookie sets
│       ├── report.go           consolidated report + csv + zip bundle
│       ├── check.go            intake, workers, search phase, delivery
│       └── format.go           small formatting helpers
├── tools/probe/                endpoint diagnostics
└── bin/                        build output (gitignored)
```

---

## 9. Deploy

### 9.1 Heroku button

```
https://heroku.com/deploy?template=https://github.com/GT56GT/hotmail-checker
```

Reads `app.json`, provisions one Eco worker, prompts for `BOT_TOKEN`. No CLI, no local checkout.

### 9.2 From the repo

```bash
brew tap heroku/brew && brew install heroku
heroku login
./deploy.sh                  # new app with random suffix
./deploy.sh my-bot-name      # existing app
```

`deploy.sh` loads `.env`, creates the app on `heroku-24`, sets the `heroku/go` buildpack, pushes config vars, sets the git remote, pushes the current branch, scales `worker=1 web=0`, and tails the first boot.

### 9.3 Verify

```bash
heroku run --type=worker bin/hotmail-checker -selftest -a <app>
```

Expected:

```
verdict: dead
reason:  session expired
OK — TLS impersonation works and the endpoint answered.

issuing one inbox search (expected: unavailable)…
reason: search unavailable for this account
```

A `403 forbidden` means the fingerprint is not matching. A timeout means the dyno cannot reach Microsoft.

---

## 10. Local

```bash
go build -o bin/hotmail-checker .
./bin/hotmail-checker -selftest        # no token needed
BOT_TOKEN=... ./bin/hotmail-checker    # full bot
go run ./tools/probe                   # endpoint diagnostics
```

---

## 11. Known limits

- **Restart drops jobs.** No persistence by design. Users resend.
- **Proxy lists persist only with `DATABASE_URL`.** Without it they reset on restart, and the bot states this in `/stats` and `/proxies`.
- **Go 1.27 required.** `tls-client` and `fhttp` pin it. Heroku's Go buildpack installs from `go.dev`.
- **Search depends on consent.** Some accounts lack Graph Mail.Read; the REST fallback covers most. If all three token paths fail, that account reports `search unavailable`.
- **`$search` semantics are server-side.** Graph treats the term as a KQL-style query, so quotes and operators inside the keyword can change behaviour. Terms are wrapped in double quotes before escaping.
- **Identity depends on the substrate profile call.** If that endpoint changes, live accounts still report correctly but without an email.
- **No hit archive.** Nothing is retained server-side; Telegram holds the delivered files.
- **OAuth client dependency.** If Microsoft retires client `000000004C12AE6F`, the fallback `GetOwaUserConfiguration` probe keeps verdicts correct but identity and search degrade.
