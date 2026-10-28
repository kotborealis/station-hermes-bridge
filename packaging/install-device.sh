#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then echo "usage: $0 station-hermes-arm64.gz" >&2; exit 2; fi
SRC=$1
APP=/data/opt/station-hermes
BIN=$APP/station-hermes
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

[ -r "$SRC" ] || { echo "artifact is not readable: $SRC" >&2; exit 1; }
mkdir -p "$APP"
FREE_KB=$(df -k /data | awk 'END {print $4}')
[ "$FREE_KB" -ge 14000 ] || { echo "need at least 14 MiB free on /data; found ${FREE_KB} KiB" >&2; exit 1; }

TMP=$APP/station-hermes.new
trap 'rm -f "$TMP"' EXIT HUP INT TERM
gzip -dc "$SRC" > "$TMP"
chmod 755 "$TMP"
"$TMP" -h >/dev/null 2>&1 || true
mv "$TMP" "$BIN"
case "$SRC" in
    "$APP"/*) rm -f "$SRC" ;;
esac
if [ ! -f "$APP/config.json" ]; then
    EXAMPLE=$SCRIPT_DIR/config.example.json
    [ -f "$EXAMPLE" ] || EXAMPLE=$SCRIPT_DIR/../config.example.json
    [ -f "$EXAMPLE" ] || { echo "config.example.json must be beside this script or in its parent directory" >&2; exit 1; }
    cp "$EXAMPLE" "$APP/config.json"
fi
touch "$APP/hermes-api-key"
chmod 600 "$APP/config.json" "$APP/hermes-api-key"
echo "Installed binary. Set session_id and hermes-api-key, then start manually; init hook and endpoint are not changed."
df -h /data
