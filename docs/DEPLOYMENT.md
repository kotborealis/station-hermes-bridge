# Rooted Yandex Station deployment checklist

On a rooted Yandex Station with a compatible UniWS endpoint and ARM64 Linux userspace, this deployment sends every recognized text request to Hermes. Yandex continues to provide wake-word handling, ASR, TTS and the low-level UniWS connection. Firmware paths and service ordering vary by model; this checklist is verified on Mini 2, so adapt those steps to the target firmware.

1. Check `/data` free space (at least 14 MiB for a staged binary replacement) and save the current `/system/vendor/quasar/quasar.cfg` off-device. Keep the endpoint backup under `/data/opt/station-hermes/` as a second rollback copy.
2. Copy the compressed build, `packaging/install-device.sh`, and `config.example.json` to one directory on the station, then run `sh install-device.sh station-hermes-arm64.gz` there. The uncompressed binary is about 12 MiB; the script requires at least 14 MiB free on `/data`, stages the binary before replacing it, and does not install the boot hook or change the firmware endpoint.
3. Write `config.json` from the example, set the endpoint/model/session/prompt fields, and place the Hermes token in `hermes-api-key`; set the config and key to root-only access (`chmod 600`).
4. From the station, verify DNS and trusted HTTPS to your Hermes host with `/data/opt/station-hermes/station-hermes -check-hermes-tls https://your-hermes-host/v1`. The check sends no token and reports the HTTP status after TLS succeeds. Do not put the token in shell history or command-line arguments.
5. Start the service manually and verify `http://127.0.0.1:8443/healthz`; inspect the process and logs. The health check confirms only that the listener is alive, not that the Hermes API is reachable.
6. Install `S98station-hermes` in `/etc/init.d` only after manual startup is sound. On Mini 2 the hook starts before `S99quasar`; it binds loopback and connects upstream when Quasar opens UniWS. Check service ordering on other models.
7. With `/` remounted writable according to the Hackalice guide, run `/data/opt/station-hermes/station-hermes -speaker-endpoint apply`, then `sync` and remount `/` read-only. Install `S98station-hermes` into `/etc/init.d`, make it executable, start it manually, restart the voice service, and confirm a voice turn reaches Hermes and is spoken by the station.

## Verify without speaking to the station

After startup, check `wget -qO- http://127.0.0.1:8443/healthz`. This confirms only that the listener is alive. Check TLS separately with `station-hermes -check-hermes-tls https://your-hermes-host/v1`; it does not send the token. Then verify the Runs flow with an actual voice turn through the station, which exercises the same persistent session used in normal operation.

## Upgrade

Build `dist/station-hermes-arm64.gz` with `./scripts/build-arm64.sh`, transfer it to the station, and verify at least 14 MiB is free on `/data`. Stop the service, decompress into a temporary file beside the installed binary, check its executable bit, and rename the old binary to a dated rollback copy before atomically moving the staged file into place. Restart the service and repeat the health check and verify a voice turn reaches Hermes and is spoken by the station. Keep the old binary until those checks pass; remove older backups only when more deployment space is needed. Do not replace a running binary in place.

## Rollback

With `/` writable, run `/data/opt/station-hermes/station-hermes -speaker-endpoint rollback`, `sync`, and remount `/` read-only. Restart Quasar, then stop and remove `/etc/init.d/S98station-hermes`. Keep the install directory until the station is confirmed healthy. A factory reset clears `/data` and requires reinstallation.

The root filesystem is normally mounted read-only. Root layout, writable-root mechanics, init ordering, and recovery steps vary by Station model and firmware. Follow the matching Hackalice procedure for the target model and remount `/` read-only after any rootfs edit. This deployment flow has been verified on Mini 2; other models need their UniWS endpoint and boot flow checked individually.
