# Cookie Checker — Go / Heroku

Telegram bot that validates session cookies in bulk and returns the ones that still work. Three services today: **Netflix**, **Instagram** and **Microsoft/Hotmail**. Adding a fourth means adding one package that satisfies an interface.

Free for everyone. One Heroku Eco worker dyno. No mandatory database.

---

## 1. Provider architecture

Every service implements one interface. The bot is service-agnostic: it detects which service a paste or file belongs to, routes it to the matching checker, and renders whatever metadata that checker returns.

```go
type Checker interface {
	Name() string                                                   // "netflix"
	Label() string                                                  // "Netflix"
	Detect(text string) bool                                        // is this mine?
	ParseBlock(text string) ([]*Cookie, []error)                    // extract cookie sets
	Check(ctx context.Context, ck *Cookie, proxyURL string) Result  // validate
}
```

`Result` carries a verdict, email, display name, proxy, elapsed time, and a `Meta` map of provider-specific fields that the report renders as `key : value` lines.

Shared types live in `internal/provider`. `internal/checker` (Microsoft) and `internal/netflix` both alias them, so the bot never type-switches on results.

`PROVIDERS` selects which are active:

| Value | Behaviour |
|---|---|
| `microsoft,netflix,instagram` | default — auto-detects all three |
| `netflix` | Netflix-only bot |
| `instagram` | Instagram-only bot |
| `microsoft` | Hotmail-only bot |

Detection runs in order, so a single-service bot is the same binary with a different token and `PROVIDERS` set. Mixed ZIPs work: each file is detected and tagged separately, and the report groups hits by service.

---

## 2. Netflix

### 2.1 Why `/YourAccount`, not `/browse`

`/browse` redirects to locale roots (`/in/`, `/us/`) when profile state is unusual. A checker watching for `netflix.com/browse` in the final URL reads that as dead when the session is perfectly alive. `/YourAccount` is stable.

Redirects are followed by hand rather than by the HTTP client, because the final URL *is* the live/dead signal:

```
GET https://www.netflix.com/YourAccount
  → 302 /login            → DEAD
  → 200 on /browse|/YourAccount|/account → LIVE
  → 200 elsewhere + "Sign Out" in body    → LIVE
  → anything else          → DEAD (redirected to X)
```

429 is rate-limited, 403 is treated as a network failure (Netflix blocks ranges at the edge; that says nothing about the cookie), any other status is dead.

### 2.2 Data extraction

`/account` carries everything in `window.netflix.reactContext`. That JSON nests, so a lazy regex cannot capture it — `balancedEnd` walks the brace depth with string and escape tracking.

Layers, in order:

1. `reactContext.models.user.data` — email, country, membership status, verification flags
2. `models.membershipData.data.membership` (or `billingSummary.data`) — plan, quality, max streams, price, next billing, member since
3. `models.profilesList.data` — profile count
4. Targeted regexes for every field, covering the newer Apollo-rendered markup

`N/A` and `null` are dropped before rendering, so the report only shows what was actually found.

### 2.3 Login links

A live `NetflixId` can be exchanged for one-click sign-in URLs via Netflix's private iOS Argo endpoint:

```
PC      https://www.netflix.com/browse?nftoken=…
TV      https://www.netflix.com/tv2?nftoken=…
Phone   https://www.netflix.com/unsupported?nftoken=…
```

This is undocumented, uses a fixed device identity, and can be withdrawn without notice. It runs best-effort with an 8 s budget after a live result — any failure returns nothing and the rest of the report stands. Disable with `NF_LOGIN_LINKS=false`.

### 2.4 Cookie parsing

Required: `NetflixId`. `SecureNetflixId`, `nfvdid` and `OptanonConsent` are carried when present.

One line can stack several accounts — dumps often concatenate. `splitStacked` splits on each `NetflixId=` occurrence, rejecting matches preceded by a word character so `SecureNetflixId` is not mistaken for a session boundary.

Formats accepted: raw header, JSON (flat or array-of-objects, filtered to netflix domains), Netscape.

---

## 3. Instagram

### 3.1 Probe endpoints

Measured against an invalid `sessionid` before the package was written:

| Endpoint | Invalid session | Role |
|---|---|---|
| `/api/v1/accounts/edit/web_form_data/` | **302 → `/accounts/login/`** | **primary** |
| `/api/v1/accounts/current_user/` | **401 `{"require_login":true}`** | fallback |
| `/api/v1/users/web_profile_info/` | 404 html, `class="not-logged-in"` | stats only |
| `www.instagram.com/` | 302 loop to itself | no |

The edit-form endpoint is primary because a dead session yields a redirect, not a body that has to be sniffed. `current_user` returns JSON with an explicit `require_login` flag, so it covers the case where the form endpoint changes shape.

### 3.2 Verdicts

| Signal | Verdict |
|---|---|
| 302 → `/accounts/login` | DEAD |
| 401, or 403 carrying `require_login` | DEAD |
| 403 **without** a login marker | FAILED — edge block, not a cookie verdict |
| 200 with `form_data` | LIVE |
| `checkpoint_required` / `challenge_required` | CHALLENGED |
| 429 | RATE_LIMITED |

The 403 split matters. Instagram blocks datacenter ranges and answers with a generic failure body — counting that as dead produces false negatives on good cookies behind a bad proxy. Verified: `i.instagram.com/api/v1/accounts/current_user/?edit=true` returns `403 "We're sorry, but something went wrong"` for anything, even a valid-shaped request.

### 3.3 Fields

`form_data` carries username, full name, email, phone, biography and external link. `current_user` adds the numeric user id and account flags. `web_profile_info` adds follower, following and post counts plus verified/business/private — login-gated, so best effort.

### 3.4 Headers

Every API call sends `x-ig-app-id: 936619743392459`, `x-asbd-id: 198387` and `x-requested-with: XMLHttpRequest`. When present, the `csrftoken` cookie is echoed as `x-csrftoken` and `ds_user_id` as `ig-intended-user-id`.

### 3.5 Cookie parsing

Required: `sessionid`. `ds_user_id`, `csrftoken`, `mid`, `ig_did`, `datr` and `rur` are carried when present. JSON imports are filtered to instagram domains, so a mixed extension export cannot smuggle in another site's cookies.

## 4. Microsoft / Hotmail

### 4.1 TLS fingerprint

Microsoft's edge inspects the ClientHello. `net/http` gets 403. Both providers use `bogdanfinn/tls-client` (uTLS, Chrome 124 profile, randomised extension order).

### 4.2 The probe endpoint

`outlook.live.com/mail/0/inbox` **returns HTTP 200 with a login shell to everyone**, authenticated or not — measured at 14,150 bytes with a deliberately invalid cookie. A status-code checker reports every dead cookie as live.

Endpoints probed with an invalid cookie:

| Endpoint | Invalid cookie | Useful |
|---|---|---|
| `/mail/0/inbox` | 200, SPA shell | no |
| `/owa/` | 302 → `/mail/` | no |
| `owa/service.svc?action=GetOwaUserConfiguration` | **401** | fallback |
| `substrate.office.com/profileb2/v2/me/V2` | **401** | identity |
| `oauth20_authorize.srf?...&prompt=none` | **302 `#error=login_required`** | **primary** |

### 4.3 Flow

```
GET silentOAuth (prompt=none)
  ├─ #access_token=…             → LIVE → substrate profile → email + name
  ├─ #error=login_required       → DEAD
  ├─ #error=interaction_required → CHALLENGED
  └─ ambiguous                   → cookie-only fallback
```

`challenged` is its own verdict: collapsing it into `dead` undercounts valid accounts, collapsing it into `live` hands out cookies that do not work.

### 4.4 Inbox search

After a check, every live Microsoft account is searched (`MAX_SEARCH_ACCOUNTS=0` = all). Token chain, first to answer wins:

```
Graph Mail.Read      → graph.microsoft.com/v1.0/me/messages?$search="term"
REST + Graph token   → outlook.office.com/api/v2.0/me/messages?$search="term"
REST + mail token    → same endpoint
```

All four scopes return `login_required` rather than `invalid_scope` for the OAuth client, confirming it is registered for each. An account without Graph consent still returns results through REST.

---

## 5. Intake

One entry point for paste, text file and ZIP.

| Format | Handling |
|---|---|
| Paste | one cookie set per line |
| `.txt` | same parser |
| `.zip` | every text entry parsed, merged, deduplicated |

Deduplication hashes the normalised cookie header per provider, so the same session appearing in five files collapses to one entry.

ZIP bounds — a ZIP is attacker-controlled:

| Limit | Value |
|---|---|
| Files | 500 |
| Per-file size | 16 MB |
| Total uncompressed | 128 MB |
| Skipped | binaries, `__MACOSX`, path traversal |

Nothing is written to disk; entries are read from memory, so extraction cannot escape anywhere. Skipped entries are reported to the user.

No batch limit. The 20 MB upload cap is the only bound.

---

## 6. Output

| File | Contents |
|---|---|
| `cookies_<id>.txt` | working cookies only, drop-in |
| `report_<id>.txt` | per-account detail with Meta and search matches |
| `results_<id>.zip` | `report.txt`, `cookies.txt`, `accounts.csv`, `dead.txt`, `search.csv` |

The zip is sent when a search ran or the batch exceeds 50 accounts.

```
### 1. jane@example.com
service : Netflix
status  : live
Plan    : Premium
Quality : 4K+HDR
Screens : 4
Price   : $22.99
Country : US
Profiles : 3
proxy   : 1.2.3.4:8080
PC login : https://www.netflix.com/browse?nftoken=…
```

`accounts.csv` is `provider,email,name,status,plan,country,proxy,search_matches`.
`search.csv` is `email,date,from,subject,link`.

---

## 7. Proxies

Each user can attach their own list. Their checks use it; the shared pool is a fallback.

```
/proxies add|list|test|del|clear
```

All formats parsed by one normaliser, mixed freely, separated by newlines, commas or semicolons:

```
1.2.3.4:8080                  → http://1.2.3.4:8080
1.2.3.4:8080:user:pass        → http://user:pass@1.2.3.4:8080
user:pass@1.2.3.4:8080        → http://user:pass@1.2.3.4:8080
socks5://user:pass@1.2.3.4:1080
[2001:db8::1]:8080            → http://[2001:db8::1]:8080
```

Three-field entries (`host:port:user`) are rejected rather than guessed — that shape could be either credential layout.

Resolution: user pool → shared pool → direct. Per-proxy concurrency is 4, three consecutive failures quarantine an entry for two minutes.

**Persistence** needs `DATABASE_URL`; Heroku's filesystem is ephemeral. Without it lists are in-memory and both `/stats` and `/proxies` say so.

```sql
CREATE TABLE IF NOT EXISTS user_proxies (
    user_id  BIGINT      NOT NULL,
    proxy    TEXT        NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, proxy)
);
```

Created on boot. Neon pooler URLs append `channel_binding=require`, which pgx forwards as a server startup option and Postgres rejects — `sanitiseDSN` strips it. Verified with `-dbcheck`.

---

## 8. Architecture

```
Telegram ──long poll──► worker dyno (single process)
                           │
                           ├─ gotgbot dispatcher
                           ├─ job registry (one job per user, 10s cooldown)
                           ├─ pending map (armed search / armed proxy add)
                           ├─ provider.Checker[]  ← detection order
                           │     ├─ microsoft
                           │     └─ netflix
                           ├─ proxy.Manager (per-user pools + shared)
                           ├─ N goroutine workers (default 20)
                           │     ├─ per-proxy semaphore, quarantine
                           │     └─ tls-client pool (one client per proxy)
                           │
                           ├─ check phase → verdicts + Meta
                           ├─ search phase (Microsoft only, if armed)
                           └─ cookies · report · zip
```

No hit archive. Telegram holds the delivered files.

---

## 9. Dyno and cost

| Item | Plan | Monthly |
|---|---|---|
| Worker dyno | Eco (1000 hrs pooled) | $5 |
| Database | Neon free tier | $0 |
| **Total** | | **$5** |

One worker at 24/7 uses 720 of 1000 pooled hours.

**Do not scale the worker.** Telegram allows one `getUpdates` consumer per token; a second dyno gets `409 Conflict` and both drop updates.

---

## 10. Project layout

```
hotmail-checker/
├── main.go                     entrypoint + -selftest / -dbcheck
├── deploy.sh                   one-command Heroku deploy
├── Procfile                    worker: bash run.sh
├── app.json                    Heroku button manifest
├── internal/
│   ├── provider/               shared types + Checker interface
│   ├── checker/                Microsoft: parse, identity, check, search
│   ├── netflix/                Netflix: parse, extract, check, login links
│   ├── instagram/              Instagram: parse, extract, check
│   ├── archive/                zip extract/build with bomb limits
│   ├── proxy/                  normaliser, pool, per-user manager
│   ├── store/                  optional Postgres persistence
│   ├── job/                    lifecycle, limits, hits
│   └── bot/                    dispatcher, intake, report, commands
└── tools/probe/                endpoint diagnostics
```

---

## 11. Deploy

```
https://heroku.com/deploy?template=https://github.com/GT56GT/hotmail-checker
```

or

```bash
./deploy.sh            # reads .env, creates app, pushes, scales worker
```

Verify:

```bash
heroku run --type=worker bin/hotmail-checker -selftest -a <app>
```

Expected: Microsoft `dead` / `session expired`, search `unavailable`, Netflix `dead` / `session expired`.

---

## 12. Known limits

- **Restart drops jobs.** Users resend.
- **Proxy lists persist only with `DATABASE_URL`.**
- **Go 1.27 required** — `tls-client` and `fhttp` pin it.
- **Netflix login links are private API.** They can break without warning; nothing else depends on them.
- **Netflix markup shifts.** Extraction has regex fallbacks, but a full redesign could blank the plan/country fields while the live/dead verdict stays correct.
- **Instagram stats need a live session.** `web_profile_info` is login-gated, so follower counts only resolve on live cookies; the verdict and identity come from the edit-form endpoint instead.
- **Instagram challenges are not deaths.** `checkpoint_required` reports as `challenged` — the session is real but Instagram wants verification.
- **Microsoft search depends on consent.** Accounts without Graph Mail.Read fall back to REST.
- **No hit archive.** Telegram holds the delivered files.
