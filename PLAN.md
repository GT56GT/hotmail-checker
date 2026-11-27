# Hotmail Cookie Checker — Go / Heroku

Telegram bot that validates Microsoft consumer session cookies in bulk, returns the ones that still work as a downloadable file, reports identity for each live account, and searches live inboxes for keywords.

Free for everyone. One Heroku Eco worker dyno. No database, no disk, no Redis.

---

## 1. The two things that decide whether a checker works

Most cookie checkers are wrong in one of two places. Both were verified empirically before any bot code was written.

### 1.1 TLS fingerprint

Microsoft's edge inspects the TLS ClientHello. `net/http`, `reqwest` and `requests` all produce a non-browser fingerprint and get 403 or a challenge page. This project uses `bogdanfinn/tls-client`, which wraps uTLS and reproduces Chrome's JA3/JA4 and extension ordering.

Profile: `Chrome_124`, with `WithRandomTLSExtensionOrder()`.

### 1.2 The probe endpoint

`https://outlook.live.com/mail/0/inbox` **returns HTTP 200 with a login shell to everyone**, authenticated or not. Measured against a deliberately invalid cookie:

```
GET https://outlook.live.com/mail/0/inbox  →  200, 14150 bytes, no auth
```

A status-code checker reports every cookie as live. This is the single most common reason checkers produce garbage results.

Endpoints were probed with an invalid cookie to find a real discriminator:

| Endpoint | Invalid cookie | Useful |
|---|---|---|
| `outlook.live.com/mail/0/inbox` | 200, 14 KB SPA shell | no |
| `outlook.live.com/owa/` | 302 → `/mail/` | no |
| `outlook.live.com/owa/service.svc?action=GetOwaUserConfiguration` | **401** | yes |
| `substrate.office.com/profileb2/v2/me/V2` | **401** | yes (needs Bearer) |
| `login.live.com/oauth20_authorize.srf?...&prompt=none` | **302 → `#error=login_required`** | **yes, primary** |

The silent-OAuth endpoint is the primary probe because it answers a single unambiguous question:

- valid session → `302` to `oauth20_desktop.srf#access_token=…`
- invalid session → `302` to `oauth20_desktop.srf#error=login_required`
- valid but needs consent → `#error=interaction_required`

And the successful case hands back a token that resolves the account identity in the same flow. One request, both answers.

---

## 2. Verification flow

```
cookie set
   │
   ├─ GET silentOAuth (prompt=none, MBI_SSL scope)
   │      │
   │      ├─ #access_token=…            → LIVE
   │      │      └─ GET substrate profile → email + display name
   │      │
   │      ├─ #error=login_required      → DEAD
   │      │
   │      ├─ #error=interaction_required
   │      │  #error=consent_required    → CHALLENGED
   │      │
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
| `rate-limited` | proxy or IP is being throttled — retry on another |
| `error` | network, proxy, TLS, or unexpected status |

`challenged` exists because collapsing it into `dead` undercounts valid accounts, and collapsing it into `live` produces cookies that do not work.

---

## 3. Inbox search

After a check completes, every live account in the batch is searched for the user's keyword.

### 3.1 Scope research

The OAuth client was tested against four scopes with an invalid cookie. All four returned `error=login_required` rather than `invalid_scope`, which confirms the client is registered for each of them:

| Scope | Response |
|---|---|
| `service::outlook.office.com::MBI_SSL` | `login_required` ✓ |
| `https://graph.microsoft.com/Mail.Read` | `login_required` ✓ |
| `https://outlook.office.com/mail.read` | `login_required` ✓ |
| Graph + REST combined | `login_required` ✓ |

Endpoint shapes were confirmed reachable:

| Endpoint | No token |
|---|---|
| `graph.microsoft.com/v1.0/me/messages` | 401 `InvalidAuthenticationToken` |
| `outlook.office.com/api/v2.0/me/messages` | 401 |
| `outlook.live.com/owa/service.svc?action=FindItem` | 401 |

### 3.2 Token chain

Accounts differ in which scopes they have consented to, so search tries three paths and takes the first that answers:

```
Graph token      → GET graph.microsoft.com/v1.0/me/messages?$search="term"
REST (graph tok) → GET outlook.office.com/api/v2.0/me/messages?$search="term"
REST (mail tok)  → same endpoint, mail-scope token
```

An account without Graph consent still returns results through the REST path. If all three fail, the account is reported as `search unavailable` — never as a false negative on the check itself.

### 3.3 Result shape

Per account: date, sender, subject, body preview (180 chars), and the message web link.

```
## user@hotmail.com — 3 match(es) via graph
2026-11-20 14:32 | billing@vendor.com | Invoice #4491 — payment due
    Hi, attached is invoice 4491 for the period ending Nov 30…
    https://outlook.live.com/mail/0/inbox/id/...
```

### 3.4 Limits

Search is capped independently of the check:

| Limit | Default | Env |
|---|---|---|
| Accounts searched per job | 20 | `MAX_SEARCH_ACCOUNTS` |
| Messages per account | 25 | `SEARCH_RESULTS_LIMIT` |
| Search term length | 120 chars | `MAX_SEARCH_TERM_LENGTH` |
| Concurrent searches | 3 | — |

Only live hits are searched, so dead cookies never consume a search slot.

---

## 4. Architecture

```
Telegram ──long poll──► worker dyno (single process)
                           │
                           ├─ gotgbot dispatcher
                           ├─ in-memory job registry (one job per user)
                           ├─ pending-search map (armed terms, 5 min TTL)
                           ├─ N goroutine workers (default 20)
                           │      ├─ proxy pool (per-proxy semaphore, quarantine)
                           │      └─ tls-client pool (one client per proxy)
                           │
                           ├─ check phase → verdicts
                           ├─ search phase (only if armed) → matches
                           │
                           └─ results
                                  ├─ cookies_<job>.txt  working cookies only
                                  ├─ report_<job>.txt   full check detail
                                  └─ search_<job>.txt   inbox matches
```

No database. Telegram is the store — a delivered file stays in the chat indefinitely, so the user can re-download whenever they want without the bot retaining anything.

In-flight jobs live in a mutex-guarded map. A dyno restart drops them; the user resends. That is the whole failure mode.

---

## 5. Dyno and cost

| Item | Plan | Monthly |
|---|---|---|
| Worker dyno | Eco (1000 hrs pooled) | $5 |
| Database | none | $0 |
| Redis | none | $0 |
| **Total** | | **$5** |

One worker at 24/7 uses 720 of 1000 pooled Eco hours, leaving ~280 spare.

Polling keeps a long-poll connection open every 25–30 seconds, which counts as activity and should prevent the Eco dyno from idling. Confirm in the first 24 hours; if it does idle, the fix is a Basic dyno, not a redesign.

### Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token — the second instance gets `409 Conflict` and both start dropping updates.

Throughput comes from goroutines inside the process, not from dyno count.

---

## 6. Free-for-all limits

The bot is open to everyone, so one user must not be able to starve the rest. Limits live at the pool level, identical for every user:

| Limit | Default | Env |
|---|---|---|
| Concurrent jobs per user | 1 | — |
| Max accounts per upload | 2000 | `MAX_COOKIES_PER_JOB` |
| Cooldown between jobs | 30s | `JOB_COOLDOWN_SECONDS` |
| Global concurrent checks | 20 | `CHECK_CONCURRENCY` |
| Concurrent checks per proxy | 4 | — |
| Upload size | 10 MB | — |

At 2000 accounts with 20 workers, a full batch runs roughly 2–4 minutes of wall time, then search adds up to 20 mailbox queries at concurrency 3.

---

## 7. Proxy layer

Proxies are fuel, not an optimisation. Microsoft 429s datacenter ranges within minutes.

- Round-robin across the pool, sticky per check
- Per-proxy semaphore of 4
- Three consecutive failures quarantines an entry for 2 minutes
- Source: `PROXY_LIST_URL` (remote, refreshed every 10 min) or `PROXY_LIST` (inline)
- With no proxies configured the bot runs direct and logs a warning

Mobile and residential survive. Datacenter dies.

---

## 8. Project layout

```
hotmail-checker/
├── main.go                     entrypoint + -selftest
├── Procfile                    worker: bin/hotmail-checker
├── app.json                    Heroku manifest (eco worker, go buildpack)
├── go.mod / go.sum
├── internal/
│   ├── config/config.go        env parsing + limits
│   ├── checker/
│   │   ├── parse.go            raw / netscape / json cookie parsing
│   │   ├── parse_test.go       parser + identity tests
│   │   ├── identity.go         email + display name extraction
│   │   ├── check.go            OAuth probe, fallback probe, verdicts
│   │   └── search.go           Graph + REST mail search, token chain
│   ├── proxy/pool.go           rotation, semaphores, quarantine
│   ├── job/registry.go         job lifecycle, per-user limits
│   └── bot/
│       ├── bot.go              dispatcher, polling
│       ├── commands.go         /start /search /cancel /stats /proxies
│       ├── check.go            intake, worker pool, search phase, delivery
│       └── format.go           cookies / report / search file builders
├── tools/probe/                endpoint diagnostics (not shipped logic)
└── bin/                        build output (gitignored)
```

---

## 9. Deploy runbook

```bash
heroku create <app-name> --stack heroku-24
heroku buildpacks:set heroku/go
heroku config:set BOT_TOKEN=... ADMIN_IDS=...
git push heroku main
heroku ps:scale worker=1 web=0
heroku logs --tail
```

Verify the TLS layer, the check probe and the search path on the deployed dyno:

```bash
heroku run --type=worker bin/hotmail-checker -selftest
```

Expected:

```
verdict: dead
reason:  session expired
OK — TLS impersonation works and the endpoint answered.

issuing one inbox search (expected: unavailable)…
reason: search unavailable for this account
```

A `403 forbidden` means the fingerprint is not matching. A timeout means the dyno cannot reach Microsoft. A search that hangs means the token chain is reaching endpoints but not resolving — re-run `go run ./tools/probe` locally.

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
- **Go 1.27 required.** `tls-client` and `fhttp` pin it. Heroku's Go buildpack installs from `go.dev`, which has it.
- **Search depends on consent.** Some accounts will not have Graph Mail.Read consented; the REST fallback covers most. If all three token paths fail, that account reports `search unavailable`.
- **`$search` semantics are server-side.** Graph treats the term as a KQL-style query, so quotes and operators inside the keyword can change behaviour. Terms are wrapped in double quotes before escaping.
- **Identity depends on the substrate profile call.** If that endpoint changes, live accounts still report correctly but without an email.
- **No hit archive.** Nothing is retained server-side; Telegram holds the delivered files.
- **OAuth client dependency.** If Microsoft retires client `000000004C12AE6F`, the fallback `GetOwaUserConfiguration` probe keeps verdicts correct but identity and search degrade. The code already branches on `unauthorized_client`.
