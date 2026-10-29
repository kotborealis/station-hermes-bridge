# Voice response latency

The voice prompt asks Hermes for one or two concise spoken sentences. Runs API status polling remains authoritative for success, failure, and approval handling.

For voice turns, the bridge also subscribes to `GET /v1/runs/{run_id}/events`. It reads only `message.delta` events, buffers text until sentence punctuation arrives, and sends each completed sentence to the station TTS stream. Tool events, interim commentary, and reasoning are not spoken. If the final output starts with text already streamed, that prefix is removed from the final TTS segment; if it does not match, the full final output is retained.

The SSE reader is best-effort. An unavailable or unsupported events endpoint does not fail the run: the bridge continues polling `GET /v1/runs/{run_id}` and speaks the final output as before. Short replies remain a single ordinary response. For a longer reply, after the two-second progress threshold the station opens a deferred response and speaks one `Секунду.` acknowledgement. Later complete sentences use additional TTS segments in that same audio stream; empty periodic progress frames are not sent.

The station audio protocol keeps the stream open between synthesized segments. The bridge waits for each streamed sentence's TTS segment to close before accepting the next one, then sends any remaining final text and closes the stream. If all final text was already spoken, it closes the existing stream without generating an empty TTS request.

## Voice controls

“Стоп”, “останови ответ” and “хватит” call Hermes `POST /v1/runs/{run_id}/stop`. A Station cancellation event does the same. The local poll then finishes when Hermes reports the run as cancelled.

“Начни новый разговор” asks for confirmation. On “да, начни новый разговор”, the bridge atomically stores a new active session ID beside the config and uses it for later Runs, including after a service restart. This keeps the prior transcript in Hermes history but excludes it from the new conversation.

Timeout, authentication, rate-limit, server, and failed-run errors have separate concise spoken responses. Raw API response bodies are not spoken or logged.
