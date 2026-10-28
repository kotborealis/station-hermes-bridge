#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT="$ROOT/dist"
mkdir -p "$OUT"
(cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w -buildid=' -o "$OUT/station-hermes-arm64" ./cmd/station-hermes)
gzip -9 -c "$OUT/station-hermes-arm64" > "$OUT/station-hermes-arm64.gz"
ls -lh "$OUT/station-hermes-arm64" "$OUT/station-hermes-arm64.gz"
