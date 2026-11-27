# hotmail-checker

Telegram bot for validating Microsoft/Hotmail session cookies in bulk. Classifies each cookie as live, dead, or error, and returns email + display name for live hits.

Runs as a single Heroku worker dyno. No disk dependencies, no web server, no Redis.

## Stack

| Layer | Choice |
|---|---|
| Bot | aiogram 3 |
| HTTP | curl_cffi (Chrome TLS impersonation — required, Microsoft fingerprints) |
| Database | Heroku Postgres via asyncpg |
| Runtime | Heroku worker dyno, polling mode |
| Files | in-memory → Telegram document |

## Deploy

```bash
heroku create <app-name> --stack heroku-24
heroku buildpacks:set heroku/python
heroku addons:create heroku-postgresql:essential-0
heroku config:set BOT_TOKEN=... ADMIN_IDS=...

git push heroku main
heroku ps:scale worker=1 web=0
heroku logs --tail
```

Or use the `app.json` manifest for a one-click provision.

## Local

```bash
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
cp .env.example .env        # fill in BOT_TOKEN, DATABASE_URL
python -m app.db.migrate
python -m app.bot
```

## Layout

```
app/
├── config.py      env + settings
├── bot.py         dispatcher, polling
├── handlers/      start, check, admin
├── core/          parser, checker, identity, proxy
├── db/            pool, migrate, queries
└── tasks/         scheduled housekeeping
```

Full architecture: [PLAN.md](PLAN.md)

## Cost

$12/mo baseline — Basic worker ($7) + Essential-0 Postgres ($5). Dropping to an external free Postgres brings it to $7/mo.

## Note on scaling

Do not run more than one worker dyno in polling mode. Telegram permits one `getUpdates` consumer per token; a second instance causes `409 Conflict` and both drop updates. Throughput scales through in-process asyncio concurrency, not dyno count. See PLAN.md §3.
