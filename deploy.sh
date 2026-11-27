#!/usr/bin/env bash
#
# One-command Heroku deploy.
#
#   ./deploy.sh              # creates a new app with a random suffix
#   ./deploy.sh my-app-name  # deploys to an existing app
#
# Reads BOT_TOKEN from .env or the environment. Requires the Heroku CLI:
#   brew tap heroku/brew && brew install heroku
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

# Load local .env when present.
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

if [[ -z "${BOT_TOKEN:-}" ]]; then
  echo "BOT_TOKEN is required — put it in .env or export it." >&2
  exit 1
fi

if ! command -v heroku >/dev/null 2>&1; then
  cat >&2 <<'EOF'
Heroku CLI not found.

  brew tap heroku/brew && brew install heroku
  heroku login

Or deploy with the button instead — no CLI needed:
  https://heroku.com/deploy?template=https://github.com/GT56GT/hotmail-checker
EOF
  exit 1
fi

APP="${1:-}"
if [[ -z "$APP" ]]; then
  APP="hotmail-checker-$(openssl rand -hex 3)"
fi

echo "==> App: $APP"

if heroku apps:info -a "$APP" >/dev/null 2>&1; then
  echo "    exists — reusing"
else
  heroku create "$APP" --stack heroku-24
  heroku buildpacks:set heroku/go -a "$APP"
fi

echo "==> Config vars"
heroku config:set -a "$APP" BOT_TOKEN="$BOT_TOKEN" >/dev/null
if [[ -n "${ADMIN_IDS:-}" ]]; then
  heroku config:set -a "$APP" ADMIN_IDS="$ADMIN_IDS" >/dev/null
fi
if [[ -n "${DATABASE_URL:-}" ]]; then
  heroku config:set -a "$APP" DATABASE_URL="$DATABASE_URL" >/dev/null
fi
if [[ -n "${PROXY_LIST:-}" ]]; then
  heroku config:set -a "$APP" PROXY_LIST="$PROXY_LIST" >/dev/null
fi
if [[ -n "${PROXY_LIST_URL:-}" ]]; then
  heroku config:set -a "$APP" PROXY_LIST_URL="$PROXY_LIST_URL" >/dev/null
fi
if [[ -n "${LOG_LEVEL:-}" ]]; then
  heroku config:set -a "$APP" LOG_LEVEL="$LOG_LEVEL" >/dev/null
fi

echo "==> Remote"
heroku git:remote -a "$APP"

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
echo "==> Pushing $BRANCH -> heroku/main"
git push heroku "$BRANCH:main"

echo "==> Scale"
heroku ps:scale worker=1 web=0 -a "$APP"

echo "==> Waiting for first boot"
sleep 12
heroku logs -a "$APP" --num 50 || true

cat <<EOF

Deployed.
  dashboard : https://dashboard.heroku.com/apps/$APP
  logs      : heroku logs --tail -a $APP
  selftest  : heroku run --type=worker bin/hotmail-checker -selftest -a $APP
EOF
