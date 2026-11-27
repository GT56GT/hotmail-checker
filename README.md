# hotmail-checker

Telegram bot that validates Microsoft/Hotmail session cookies in bulk. Send cookies, get back the working ones as a file plus a full report with account identity.

Free for everyone. One Heroku Eco dyno. No database, no disk.

## Why this one works

`outlook.live.com/mail/0/inbox` returns **HTTP 200 with a login page to everyone**. A status-code checker calls every cookie live.

This bot uses the silent OAuth flow (`prompt=none`), which answers with an access token when the session is valid and `error=login_required` when it is not. A cookie-only endpoint (`GetOwaUserConfiguration`, 401 without a session) covers the fallback.

TLS impersonation via `bogdanfinn/tls-client` (uTLS, Chrome 124 profile) — Microsoft fingerprints the ClientHello and rejects anything that does not look like a browser.

## Usage

```
/paste cookies or upload .txt
  → live progress
  → cookies_<job>.txt   (working cookies, drop-in)
  → report_<job>.txt    (email, name, dead, challenged, errors)
```

Accepted formats: raw header, JSON export, Netscape `cookies.txt`.

Required cookies: `MSPAuth`, `MSPProf`, `ANON`.

## Commands

| Command | What |
|---|---|
| `/start` | usage |
| `/cancel` | stop a running job |
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

The selftest needs no token and proves two things at once: TLS impersonation reaches Microsoft, and the probe endpoint answers with the expected discriminator.

## Local

```bash
go build -o bin/hotmail-checker .
./bin/hotmail-checker -selftest
BOT_TOKEN=... ./bin/hotmail-checker
```

## Config

| Var | Default | Purpose |
|---|---|---|
| `BOT_TOKEN` | — | required |
| `ADMIN_IDS` | — | comma-separated |
| `PROXY_LIST_URL` | — | remote proxy list |
| `PROXY_LIST` | — | inline proxies |
| `CHECK_CONCURRENCY` | 20 | goroutine workers |
| `MAX_COOKIES_PER_JOB` | 500 | per upload |
| `JOB_COOLDOWN_SECONDS` | 30 | per user |
| `LOG_LEVEL` | info | |

## Cost

$5/mo — one Eco worker. No Postgres, no Redis, no object storage.

## Do not scale the worker

`heroku ps:scale worker=2` breaks the bot. Telegram allows one `getUpdates` consumer per token; the second instance gets `409 Conflict` and both drop updates. Throughput scales with goroutines, not dyno count.

Full design, endpoint research and failure modes: [PLAN.md](PLAN.md)
