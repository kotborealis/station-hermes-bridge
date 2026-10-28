# Development guide for agents

## Project purpose

This repository contains a small Go service that proxies a rooted Yandex Station Mini 2's UniWS protocol to Hermes Agent. The station retains wake-word detection, speech recognition, and speech synthesis. Recognized text goes to Hermes Runs; assistant sentence deltas may be synthesized before the final run status arrives.

## Before changing code

- Read `README.md`, `docs/DEPLOYMENT.md`, and `docs/VOICE-LATENCY.md` for product behavior and device constraints.
- Keep the device target static `linux/arm64` with `CGO_ENABLED=0`; the station has limited RAM and storage and does not have a Go runtime.
- Preserve Hermes run status polling as the authority for completion, errors, and approvals. SSE is best-effort text delivery; ignore tool events, interim commentary, and reasoning.
- Keep the local listener on loopback. Never log API tokens, full user utterances, full assistant output, or raw Hermes error bodies.
- Do not add credentials, private endpoint files, device identifiers, or deployment snapshots to the public tree.

## Development checks

Use Go 1.27.3 or newer:

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
./scripts/build-arm64.sh
```

Add focused tests for protocol parsing, routing, or state changes. Avoid tests that require a live station, real API credentials, or network listeners; use injected transports and protocol fakes.

## Deployment

- Follow `docs/DEPLOYMENT.md`. Do not assume rootfs is writable; use the device-specific Hackalice procedure and remount it read-only after rootfs edits.
- Check `/data` space before transfer and stage the complete binary before swapping it into place.
- Keep a dated previous binary until health, Hermes API, and Runs/SSE checks pass. Preserve a rollback path for the Quasar endpoint and init hook.
- Keep the API key in the configured root-only file. Never pass it in shell arguments, print it, or copy it into repository configuration.
- Do not change Hermes approval policy or enable unattended approvals. Voice approvals are single-run decisions handled by the bridge.

## Repository hygiene

Commit only source, tests, docs, build scripts, and required license notices. `dist/`, `private/`, local configuration, keys, logs, and generated binaries are intentionally ignored.
