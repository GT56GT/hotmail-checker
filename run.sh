#!/usr/bin/env bash
#
# Heroku entrypoint.
#
# The Go buildpack installs binaries into bin/, but that has moved between
# buildpack versions. Rather than gamble on one path, find the binary and exec
# it — and print the tree if nothing matches so the log says exactly what went
# wrong instead of a bare "no such file".
#
set -euo pipefail

for candidate in \
  bin/hotmail-checker \
  ./hotmail-checker \
  "$HOME/bin/hotmail-checker" \
  bin/app
do
  if [ -x "$candidate" ]; then
    exec "$candidate" "$@"
  fi
done

echo "hotmail-checker: compiled binary not found" >&2
echo "pwd: $(pwd)" >&2
echo "--- . ---" >&2
ls -la . >&2 || true
echo "--- bin ---" >&2
ls -la bin >&2 || true
echo "--- go env ---" >&2
go env GOBIN GOPATH 2>/dev/null || true
exit 1
