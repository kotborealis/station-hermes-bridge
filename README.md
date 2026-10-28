# Station Hermes Bridge

Small on-device bridge for a rooted Yandex Station Mini 2. The station keeps its normal Yandex speech recognition and speech playback. This service proxies the station's UniWS connection and sends recognized assistant requests to a persistent Hermes Agent session through the OpenAI-compatible API and Hermes Runs API.

The project does not need a Mac, OpenCode, Entware, or a second always-on host once installed. The Hermes API server must remain reachable over HTTPS from the station.

## Target and constraints

- Target: Yandex Station Mini 2 (Amlogic AXG, Cortex-A53, Linux `aarch64`).
- Build: static `linux/arm64`, `CGO_ENABLED=0`; this avoids depending on the station's 32-bit Buildroot userspace libraries.
- Install path: `/data/opt/station-hermes`.
- Runtime: one Go process, no external packages on the station.
- Current build: about 12.4 MiB; gzip transfer artifact about 4.2 MiB. The installer stages an uncompressed copy and requires at least 14 MiB free on `/data`.
- Hermes API token is read from a separate file with mode `0600`; do not commit it.

The station's speech recognition and TTS stay on Yandex. Every recognized text request goes to Hermes; there is no classifier or rule that sends ordinary questions back to Alice. Non-text protocol events still pass through to Yandex. Hermes session continuity is provided by the configured stable `session_id`; the session identifier and API token are not logged.

## Build

Requires Go 1.27.3 or newer (the source uses `encoding/json/v2`). On a Mac or Linux build host:

```sh
./scripts/build-arm64.sh
```

The output is `dist/station-hermes-arm64` and `dist/station-hermes-arm64.gz`. The compressed file is convenient for transfer; unpack it on the station under `/data/opt/station-hermes`.

## Configuration

Copy `config.example.json` to `/data/opt/station-hermes/config.json`, then set a stable Hermes session ID and key file path. Store the API token separately in `/data/opt/station-hermes/hermes-api-key` with permissions `0600`. The binary contains no token; the service reads it from this root-only file at startup, and the secret must not be committed to the repository.

The example uses `https://hermes.example/v1` and model `hermes-agent`; replace the URL with your reachable Hermes API endpoint. The default UniWS upstream is `wss://uniproxy.alice.yandex.net/uni.ws`. The service binds only to `127.0.0.1:8443`, so its HTTP health endpoint and WebSocket listener are not exposed to the LAN.

Before installation, verify that the station can resolve the Hermes hostname and establish trusted HTTPS. Hackalice notes that the station's BusyBox `wget` does not support HTTPS on this firmware; this project's Go runtime uses the system CA bundle. Run `station-hermes -check-hermes-tls https://your-hermes-host/v1` on the station before changing the Quasar endpoint. This checks trusted HTTPS without sending the API token. A successful DNS lookup or HTTP redirect alone does not prove TLS works.

## Install and activate

Build and stage the binary first. The device service file is `packaging/S98station-hermes`; it starts the process from `/data/opt/station-hermes` and appends startup diagnostics to the service log. Install it only after config and token are present.

The binary includes a guarded endpoint switch. It points `aliced.uniProxyUrl` to `ws://127.0.0.1:8443/uni.ws`, disables QUIC for this endpoint, and stores the exact prior endpoint values at `/data/opt/station-hermes/endpoint-backup.json`. It refuses to overwrite an unrelated backup or roll back after an external config change. Use the device's writable-root procedure and save the whole original config off-device too. Restart the voice service only after `/healthz` works locally. The staged install and rollback steps are in [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

Do not apply the endpoint change until the service's HTTPS connectivity to Hermes has been verified from the station. If the bridge is stopped, restore the original endpoint before expecting voice requests to work.

## Verify

Run `go test ./...`, `go vet ./...`, and `./scripts/build-arm64.sh` on the development host. CI runs the same checks and publishes the compressed ARM64 build as a workflow artifact.

On the station, `station-hermes -check-hermes-tls URL` checks HTTPS trust without credentials, `-check-hermes-api` makes a harmless one-word API request, and `-check-hermes-stream` checks Runs plus sentence SSE in a separate diagnostic session. `/healthz` checks only that the local listener is alive. None of these checks requires speaking to the station.

## API behavior and approvals

Voice turns use Hermes Runs and one persistent session. If Hermes pauses for an approval, the bridge asks aloud; say “разрешаю” for the pending action once or “отмена”. The decision is single-use and bound to the pending request. The bridge does not enable Hermes unattended approval mode. Hermes server policy still controls which actions can be auto-approved, rejected, or exposed for approval.

This is a voice interface, not an authentication factor. Anyone able to speak to the station can interact with the configured Hermes session. Keep the device on a trusted network and configure Hermes permissions accordingly.

Voice responses use a short-answer instruction. If Hermes has not completed a reply after two seconds, the station says one brief acknowledgement. The bridge subscribes to the Hermes Runs SSE events and can synthesize complete assistant sentences while the run is still active; the final run output remains authoritative, and text already spoken is omitted from the final audio segment. See `docs/VOICE-LATENCY.md` for event handling and fallback behavior.

## Scope

Intentionally omitted: OpenCode, MCP servers, Home Assistant, music queue, web reader, Mac runtime dependency, UI, local databases, and broad routing configuration. All recognized assistant requests go to Hermes; Yandex remains the station's ASR/TTS and low-level protocol service.

## License

Source is based on the Quasar Bridge UniWS proxy and Yandex Station protocol handling. See `LICENSE` and `THIRD_PARTY_NOTICES`.
