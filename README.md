# hotmail-checker

Telegram bot that validates Microsoft/Hotmail session cookies in bulk and searches the inboxes that still work. Send cookies, get back the working ones as a file, a full report with account identity, and a keyword search across every live mailbox.

Free for everyone. One Heroku Eco dyno. No database, no disk.

## Why this one works

`outlook.live.com/mail/0/inbox` returns **HTTP 200 with a login page to everyone**. A status-code checker calls every cookie live.

This bot uses the silent OAuth flow (`prompt=none`), which answers with an access token when the session is valid and `error=login_required` when it is not. A cookie-only endpoint (`GetOwaUserConfiguration`, 401 without a session) covers the fallback.

TLS impersonation via `bogdanfinn/tls-client` (uTLS, Chrome 124 profile) — Microsoft fingerprints the ClientHello and rejects anything that does not look like a browser.

## Check

```
paste cookies or upload .txt
  → live progress
  → cookies_<job>.txt   working cookies only, drop-in
  → report_<job>.txt    email, name, dead, challenged, errors
```

Up to **2000 accounts per job**. Formats: raw header, JSON export, Netscape `cookies.txt`.
Required cookies: `MSPAuth`, `MSPProf`, `ANON`.

## Inbox search

```
/search invoice
  → bot arms the search
  → send cookies
  → check runs, then every live account is searched
  → search_<job>.txt
```

Results per account: date, sender, subject, body preview, direct link.

```
2026-11-20 14:32 | billing@vendor.com | Invoice #4491 — payment due
    Hi, attached is invoice 4491 for the period ending Nov 30…
    https://outlook.live.com/mail/0/inbox/id/...
```

Only live accounts are searched, so dead cookies cost nothing. Capped at 20 accounts and 25 messages per account per job.

Token chain: Graph Mail.Read → Outlook REST (Graph scope) → Outlook REST (mail scope). First one that answers wins, so accounts without Graph consent still return results.

## Commands

| Command | What |
|---|---|
| `/start` | usage |
| `/search <term>` | arm an inbox search, then send cookies |
| `/cancel` | stop a running job, clear armed search |
| `/stats` | service status |
| `/proxies` | pool health (admin) |

## Deploy

```bash
heroku create <app-name> --stack heroku-24
heroku buildpacks:set heroku/go
heroku config:set BOT_TOKEN=... ADMIN_IDS=...
git push heroku main
heroku ps:scale worker=1 web=0
heroku run --type=worker bin/hotmail-checker -selftest
```

The selftest needs no token. It proves TLS impersonation reaches Microsoft, the check probe answers correctly, and the search path degrades cleanly.

## Local

```bash
go build -o bin/hotmail-checker .
./bin/hotmail-checker -selftest
BOT_TOKEN=... ./bin/hotmail-checker
```

Endpoint diagnostics when Microsoft changes something:

```bash
go run ./tools/probe
```

## Config

| Var | Default | Purpose |
|---|---|---|
| `BOT_TOKEN` | — | required |
| `ADMIN_IDS` | — | comma-separated |
| `PROXY_LIST_URL` | — | remote proxy list |
| `PROXY_LIST` | — | inline proxies |
| `CHECK_CONCURRENCY` | 20 | goroutine workers |
| `MAX_COOKIES_PER_JOB` | 2000 | per upload |
| `MAX_SEARCH_ACCOUNTS` | 20 | mailbox searches per job |
| `SEARCH_RESULTS_LIMIT` | 25 | messages per account |
| `JOB_COOLDOWN_SECONDS` | 30 | per user |
| `LOG_LEVEL` | info | |

## Cost

$5/mo — one Eco worker. No Postgres, no Redis, no object storage.

## Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token; the second instance gets `409 Conflict` and both drop updates. Throughput scales with goroutines, not dyno count.

Full design, endpoint research and failure modes: [PLAN.md](PLAN.md)
