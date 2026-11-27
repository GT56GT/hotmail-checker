# cookie-checker

Telegram bot that validates session cookies in bulk and returns the ones that still work. Currently **Netflix**, **Instagram** and **Microsoft/Hotmail**.

Send cookies — or a ZIP of logs — and get back the working set, a per-account report, and a machine-readable bundle.

## Deploy

**One click — no CLI needed:**

[![Deploy](https://www.herokucdn.com/deploy/button.svg)](https://heroku.com/deploy?template=https://github.com/GT56GT/hotmail-checker)

**Or from this machine:**

```bash
brew tap heroku/brew && brew install heroku
heroku login
./deploy.sh
```

**Verify after deploy:**

```bash
heroku run --type=worker bin/hotmail-checker -selftest -a <app>
```

**Check one cookie directly** — prints provider, verdict, identity and every field:

```bash
bin/hotmail-checker -check cookies.txt
cat cookies.txt | bin/hotmail-checker -check -
```

### Worker-only app

There is no `web` process — the bot polls Telegram, it does not serve HTTP.

```bash
heroku ps:scale worker=1 -a <app>
```

## Run one service only

```bash
heroku config:set PROVIDERS=netflix -a <app>
```

`PROVIDERS` is a comma list: `microsoft,netflix,instagram` (default), or any subset. Detection is automatic — no command needed. Same binary, different token, different service.

## Instagram

Required cookies: `sessionid` · `ds_user_id` · `csrftoken`

Two probe endpoints, measured against an invalid `sessionid` before this was written:

```
/api/v1/accounts/edit/web_form_data/   302 → /accounts/login/     dead
/api/v1/accounts/current_user/         401 {"require_login":true}  dead
/api/v1/users/web_profile_info/        404 html, gated behind login
```

The edit-form endpoint is primary because a dead session returns a clean redirect rather than a body that has to be sniffed. `current_user` is the JSON fallback.

Reported per account:

```
### 1. jane_doe
service : Instagram
status  : live
Username : jane_doe
Full name : Jane Doe
User ID : 123456789
Followers : 1,500
Following : 120
Posts : 64
Verified : Yes
Business : No
Private : No
Email : jane@example.com
Phone : +15551234567
Bio : hello world
proxy : 1.2.3.4:8080
```

A `403` without a login marker is treated as an edge block, not dead — Instagram blocks ranges and that says nothing about the cookie. `checkpoint_required` in the response is reported as `challenged`, not dead.

## Netflix

Required cookies: `NetflixId` · `SecureNetflixId`

Validation hits `/YourAccount`, not `/browse` — browse redirects to locale roots when profile state is odd and reads as dead when the session is fine. Data comes from `/account` via `window.netflix.reactContext`, with regex fallbacks for the newer Apollo markup.

Reported per account:

```
### 1. jane@example.com
service : Netflix
status  : live
Plan    : Premium
Quality : 4K+HDR
Screens : 4
Price   : $22.99
Next billing : 2026-12-01
Member since : 2024-03-11
Country : US
Profiles : 3
Payment : VISA *4412
On hold : No
proxy   : 1.2.3.4:8080
PC login    : https://www.netflix.com/browse?nftoken=...
TV login    : https://www.netflix.com/tv2?nftoken=...
Phone login : https://www.netflix.com/unsupported?nftoken=...
```

Login links come from Netflix's private iOS Argo endpoint. Best effort — if it declines, everything else still reports. Disable with `NF_LOGIN_LINKS=false`.

## Microsoft / Hotmail

Required cookies: `MSPAuth` · `MSPProf` · `ANON`

```
GET silentOAuth (prompt=none)
  ├─ #access_token=…            → LIVE  → substrate profile → email + name
  ├─ #error=login_required      → DEAD
  ├─ #error=interaction_required → CHALLENGED
  └─ ambiguous                  → cookie-only fallback (GetOwaUserConfiguration)
```

`outlook.live.com/mail/0/inbox` returns HTTP 200 with a login page to everyone — a status-code checker calls every dead cookie live. That is why the probe is the OAuth flow.

Optional inbox search:

```
/search invoice
  → send cookies or a zip
  → live accounts searched, matches inline in the report
```

Token chain: Graph Mail.Read → Outlook REST (Graph scope) → Outlook REST (mail scope).

## Input

- Paste cookie strings, one account per line
- Upload `.txt`
- Upload `.zip` of logs — every text file parsed, merged and deduplicated across files

**No batch limit.** The 20 MB upload cap is the only bound.

Zip safety: 500 files, 16 MB each, 128 MB uncompressed. Binaries, `__MACOSX` and path traversal are skipped and reported.

## Output

| File | Contents |
|---|---|
| `cookies_<id>.txt` | working cookies only, drop-in |
| `report_<id>.txt` | per-account detail |
| `results_<id>.zip` | report · cookies · accounts.csv · dead.txt · search.csv |

## Your own proxies

```
/proxies add      paste or upload, all formats
/proxies list     indexed, credentials masked
/proxies test     reachability + latency
/proxies del 3    remove one
/proxies clear    start over
```

```
1.2.3.4:8080
1.2.3.4:8080:user:pass
user:pass@1.2.3.4:8080
socks5://1.2.3.4:1080
socks5://user:pass@1.2.3.4:1080
[2001:db8::1]:8080
```

Separators: newlines, commas, semicolons. `#` ignored. Up to 500 per user.

Set `DATABASE_URL` (Neon free tier) and lists persist across restarts.

## Commands

| Command | What |
|---|---|
| `/start` | usage |
| `/search <term>` | arm an inbox search (Microsoft) |
| `/proxies` | manage your proxy list |
| `/cancel` | stop a running job |
| `/stats` | service status |

## Config

| Var | Default | Purpose |
|---|---|---|
| `BOT_TOKEN` | — | required |
| `PROVIDERS` | microsoft,netflix,instagram | enabled services |
| `ADMIN_IDS` | — | admin Telegram IDs |
| `DATABASE_URL` | — | proxy persistence |
| `PROXY_LIST_URL` / `PROXY_LIST` | — | shared fallback pool |
| `CHECK_CONCURRENCY` | 20 | goroutine workers |
| `MAX_COOKIES_PER_JOB` | 0 | 0 = no limit |
| `MAX_SEARCH_ACCOUNTS` | 0 | 0 = all live |
| `MAX_PROXIES_PER_USER` | 500 | per-user cap |
| `NF_LOGIN_LINKS` | true | Netflix login links |
| `JOB_COOLDOWN_SECONDS` | 10 | between jobs |

## Cost

$5/mo — one Eco worker. Neon free tier covers proxy persistence at $0.

## Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token; the second instance gets `409 Conflict` and both drop updates. Throughput scales with goroutines, not dyno count.

Design notes and endpoint research: [PLAN.md](PLAN.md)
