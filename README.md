# hotmail-checker

Telegram bot that validates Microsoft/Hotmail session cookies in bulk and searches the inboxes that still work.

Send cookies, get back the working ones as a file, a full report with account identity, and a keyword search across live mailboxes. Bring your own proxies so your checks run on your IPs.

## Deploy

**One click — no CLI needed:**

[![Deploy](https://www.herokucdn.com/deploy/button.svg)](https://heroku.com/deploy?template=https://github.com/GT56GT/hotmail-checker)

The button reads `app.json`, provisions an Eco worker dyno, and asks for `BOT_TOKEN`. That is the whole setup.

**Or from this machine:**

```bash
brew tap heroku/brew && brew install heroku
heroku login
./deploy.sh                    # creates a new app
./deploy.sh my-bot-name        # or deploys to an existing one
```

`deploy.sh` reads `BOT_TOKEN` from `.env`, sets config vars, pushes, scales the worker and tails the first boot.

**Verify after deploy:**

```bash
heroku run --type=worker bin/hotmail-checker -selftest -a <app>
```

Expected: `verdict: dead` and `OK — TLS impersonation works and the endpoint answered.`

### Worker-only app

There is no `web` process — the bot polls Telegram, it does not serve HTTP. Scale the worker, never web:

```bash
heroku ps:scale worker=1 -a <app>
```

From the dashboard: **Resources → worker → toggle on**.

If a deploy stops at *"Error scaling dynos: Couldn't find that process type (web)"*, that is an older `app.json` declaring a web dyno that does not exist. Pull the latest and redeploy — or just toggle the worker on manually, which is the only step that was missing.

## Why this one works

`outlook.live.com/mail/0/inbox` returns **HTTP 200 with a login page to everyone**. A status-code checker calls every cookie live.

This bot uses the silent OAuth flow (`prompt=none`), which answers with an access token when the session is valid and `error=login_required` when it is not. A cookie-only endpoint (`GetOwaUserConfiguration`, 401 without a session) covers the fallback.

TLS impersonation via `bogdanfinn/tls-client` (uTLS, Chrome 124 profile) — Microsoft fingerprints the ClientHello and rejects anything that does not look like a browser.

## Check

```
paste cookies, upload .txt, or upload a .zip of logs
  → every file inside the zip is parsed and merged
  → live progress
  → cookies_<job>.txt   working cookies only, drop-in
  → report_<job>.txt    per-account detail incl. search matches
  → results_<job>.zip   report · cookies · accounts.csv · dead.txt · search.csv
```

**No batch limit.** Send 10 or 10,000 — the only bound is the 20 MB upload cap. Cookies inside a zip are deduplicated across files, so overlapping logs collapse to one entry each.

Formats: raw header, JSON export, Netscape `cookies.txt`. Required cookies: `MSPAuth`, `MSPProf`, `ANON`.

Zip safety: 500 files max, 16 MB per file, 128 MB uncompressed total. Binary files, macOS `__MACOSX` metadata and path-traversal entries are skipped, and the bot reports what it ignored.

## Your own proxies

```
/proxies add
  → paste your list or upload a .txt
```

Every check and search for that user runs through their own proxies. No shared pool contention, no one else's rate limits.

```
/proxies list     see what's saved (credentials masked)
/proxies test     reachability + latency per proxy
/proxies del 3    remove one
/proxies clear    start over
```

**All formats accepted, in one list:**

```
1.2.3.4:8080
1.2.3.4:8080:user:pass
user:pass@1.2.3.4:8080
socks5://1.2.3.4:1080
socks5://user:pass@1.2.3.4:1080
http://1.2.3.4:8080
https://1.2.3.4:8443
[2001:db8::1]:8080
```

Separators: newlines, commas or semicolons. `#` lines are ignored. Up to 500 per user.

**Persistence:** set `DATABASE_URL` (Neon or Supabase free tier) and lists survive restarts. Without it, they reset when the dyno restarts and the bot says so in `/stats`.

## Inbox search

```
/search invoice
  → send cookies or a zip
  → check runs, then every live account is searched
  → matches are folded into report_<job>.txt and shipped as search.csv inside the zip
```

Per account, in the report:

```
### 1. user@hotmail.com
status : live
name   : Jane Doe
proxy  : 1.2.3.4:8080
search : 3 match(es) for "invoice" via graph

  2026-11-20 14:32 | billing@vendor.com | Invoice #4491 — payment due
      Hi, attached is invoice 4491 for the period ending Nov 30…
      https://outlook.live.com/mail/0/inbox/id/...
```

Token chain: Graph Mail.Read → Outlook REST (Graph scope) → Outlook REST (mail scope). First one that answers wins, so accounts without Graph consent still return results.

## Commands

| Command | What |
|---|---|
| `/start` | usage |
| `/search <term>` | arm an inbox search, then send cookies |
| `/proxies` | manage your proxy list |
| `/cancel` | stop a running job |
| `/stats` | service status |

## Config

| Var | Default | Purpose |
|---|---|---|
| `BOT_TOKEN` | — | required |
| `ADMIN_IDS` | — | admin Telegram IDs |
| `DATABASE_URL` | — | proxy persistence (optional) |
| `PROXY_LIST_URL` / `PROXY_LIST` | — | shared fallback pool |
| `CHECK_CONCURRENCY` | 20 | goroutine workers |
| `MAX_COOKIES_PER_JOB` | 0 | 0 = no limit |
| `MAX_SEARCH_ACCOUNTS` | 0 | live accounts searched (0 = all) |
| `MAX_PROXIES_PER_USER` | 500 | per-user proxy cap |
| `JOB_COOLDOWN_SECONDS` | 10 | between jobs |
| `LOG_LEVEL` | info | |

## Cost

$5/mo — one Eco worker. No Postgres required. Neon's free tier covers proxy persistence at $0.

## Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token; the second instance gets `409 Conflict` and both drop updates. Throughput scales with goroutines, not dyno count.

Full design, endpoint research and failure modes: [PLAN.md](PLAN.md)
