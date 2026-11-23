# Streamed pair

Users send a gRPC call (or gRPC-Web, Connect, server-sent events, NDJSON) through `proxy_listener`. Marasi carries it as a streamed pair: each stream message goes on as soon as it is whole and is stored as its own row. Users read the messages over the CLI, and hold and edit one in Checkpoint while the call is in flight. Everything about streamed pairs is in `marasi skill` (the `stream` and `checkpoint` rows); this file is the proof recipe.

## Sub-features

- Carry a gRPC call as a streamed pair, over HTTP/2 or HTTP/1.1.
- Watch it with `events`: `streamed_pair.started`, `stream_message`, `traffic.response`, `streamed_pair.ended`.
- Page its messages with `stream messages PAIR_ID`, and read one with `stream get PAIR_ID MESSAGE_ID`, including its protobuf text form.
- Hold its messages and trailers with `checkpoint stream-intercept on`; list them with `checkpoint list --kind stream` or `--kind trailer`.
- Edit a held message's text form with `checkpoint forward ID --text --file FILE`.
- Inject into or cancel a running pair with `stream inject` and `stream cancel`.
- Print a pair's request with its request messages joined back into the body with `traffic request PAIR_ID`, ready for `launchpad launch --raw-file`.
- Edit a message from Lua in `processStreamMessage` (type `streammsg`; `marasi extension api streammsg`).
- A project switch cancels every live streamed pair ([A project switch and a shutdown cancel live streamed pairs](../../../../docs/adr/2026-11-21-project-switch-and-shutdown-cancel-live-streamed-pairs.md)).

## Driving it with shell and curl

The origin is grpc-go's interop server, the same version the `grpc-client` conformance suite pins. It speaks h2c on the port you give it:

```bash
go run google.golang.org/grpc/interop/server@v1.85.0 -port "$ORIGIN_PORT"
```

Pick `$ORIGIN_PORT` free yourself and record the PID. It is ready when `lsof -nP -iTCP:$ORIGIN_PORT -sTCP:LISTEN` shows it. The first `go run` downloads and compiles, so give it a minute.

The client is curl, which writes a gRPC call by hand: `UnaryCall` with a `SimpleRequest` whose field 2, `response_size`, is 4. The body is the 5-byte envelope (flag 0, length 2) and the two protobuf bytes:

```bash
printf '\x00\x00\x00\x00\x02\x10\x04' > req.bin
curl -sS -m 120 --noproxy '' --proxy "http://$PROXY_LISTENER" -p --http2-prior-knowledge \
  -X POST -H 'content-type: application/grpc' -H 'te: trailers' --data-binary @req.bin \
  "http://127.0.0.1:$ORIGIN_PORT/grpc.testing.TestService/UnaryCall" -D headers.txt -o body.bin
```

`-p` opens a `CONNECT` tunnel and `--http2-prior-knowledge` speaks h2c inside it. `-D` writes the CONNECT's `200`, the response head and then the trailers.

**Read.** With `events` attached, send the call once with `checkpoint stream-intercept` off. Require curl to exit 0, `grpc-status: 0` in `headers.txt`, and `body.bin` to be `00 00000008 0a06 1204 00000000` (a `SimpleResponse` whose payload body is 4 zero bytes). Require events stdout to show, for one `pair_id`, `traffic.request`, `streamed_pair.started` with `framings.request` `grpc`, one `stream_message` per direction, `traffic.response` with `metadata.streamed`, then `streamed_pair.ended` whose `directions.response.trailers` include `grpc-status` `0`. Run `stream messages "$PAIR_ID" --json`: require two items, oldest first, `request` with `payload` `EAQ=` and `response` with `payload` `CgYSBAAAAAA=`, both `framing` `grpc` and `flags` 0, and `next_cursor` null. Run `stream get "$PAIR_ID" "$REQUEST_MESSAGE_ID" --json` and require `text` `"2:VARINT 4\n"`. Human `stream get` of the response message must end with:

```text
text:
1:LEN {
  2:LEN {0 0 0 0}
}
```

`traffic get "$PAIR_ID" --json` must show `client_protocol` and `origin_protocol` `h2`, `metadata.streamed.request` and `.response` each `ending` `complete` with `messages` 1, and `streamed` `{"request":{"framing":"grpc","ending":"complete"},"response":{"framing":"grpc","ending":"complete"}}`. `traffic list -q 'streamed:* AND request_framing = "grpc"' --json` must list the pair. `traffic request "$PAIR_ID" > req.txt` must print the head and the request message in its envelope (stderr `joined 1 request messages, 0 left out`), and `launchpad launch "$LAUNCHPAD_ID" --scheme http --raw-file req.txt --json` must replay the call: `status` `completed`, and the launched pair `h2` to the origin with `grpc-status` `0` in its `metadata.streamed.response.trailers`. The interop server speaks h2c, so `--scheme http` sends the `HTTP/2` request text by prior knowledge; `--scheme https` fails with `upstream_error`. Human `traffic get "$PAIR_ID"` prints `streamed: request grpc (complete), response grpc (complete)`. `traffic list -q 'NOT streamed:*'` must not list the pair.

**Hold and edit.** Run `checkpoint stream-intercept on --json` and require `stream_intercept` true. Start the same curl in the background. Retry `checkpoint list --kind stream --json` until it holds one item: `type` `stream`, `direction` `request`, `text` `"2:VARINT 4\n"`, `metadata.intercepted` true. Write `2:VARINT 7` to a file and run `checkpoint forward "$ID" --text --file FILE --json`. The response message is held next (`direction` `response`, `text` with seven zeros), then the response's trailer block (`type` `trailer`, `raw` base64 of `grpc-status: 0` and the other trailer lines); forward each with `checkpoint forward "$ID" --json </dev/null`. Require curl to exit 0, `grpc-status: 0`, and `body.bin` to carry 7 zero bytes (`00 0000000b 0a09 1207 00000000000000`): the origin read the edited message. `stream messages "$PAIR_ID" --json` must show the request message with `payload` `EAc=` and `metadata` `{"edited":true,"intercepted":true}`. Events must carry three `checkpoint.held` and three `checkpoint.forwarded` for that pair. Run `checkpoint stream-intercept off --json` afterwards.

**Inject and cancel.** Hold a `FullDuplexCall` open: run the same curl against `/grpc.testing.TestService/FullDuplexCall` with `-N -T -` instead of `--data-binary`, its stdin a pipe you keep open, and write one message, `\x00\x00\x00\x00\x04\x12\x02\x08\x02` (`response_parameters { size: 2 }`). Once `stream messages` shows the first response message, `traffic request "$PAIR_ID"` must exit 1 with `stream_request_open`. Write `2:LEN {1:VARINT 3}` to a file and run `stream inject "$PAIR_ID" --direction request --text --file FILE --json`: it returns the stored message with `metadata` `{"injected":true}`, and the origin answers it with a second response message. `stream inject "$PAIR_ID" --direction response --file /dev/null --json` sends an empty message to the client. `stream cancel "$PAIR_ID" --json` returns `{"pair_id":…,"cancelled":true}`, curl exits non-zero, and `traffic get --json` shows `streamed` with both directions `ending` `cancelled`. A second `stream cancel` exits 1 with `stream_not_open`.

**Lua.** `extension update` the workshop with a `processStreamMessage(m)` that, for a `request` message, prints `m:text()`, calls `m:set_text("2:VARINT 5")` and `m:set_metadata({seen = true})`. The unary call must then return five zero bytes (`00 00000009 0a07 1205 0000000000`), `extension logs` must show `2:VARINT 4`, and the stored request message must have `payload` `EAU=` with `workshop` `{"seen":true}` in its `metadata`.

**Project switch.** With a `FullDuplexCall` held open as above, `project open --name "$OTHER"` must return at once without waiting for the call. The client's stream ends, `streamed_pair.ended` with the request `ending` `cancelled` is published before `project.opened`, the new project has no such pair, and after switching back `traffic get "$PAIR_ID" --json` shows `streamed.request.ending` `cancelled`. The response direction is `null` there when the origin had sent no response head.

## Gotchas

- With `stream-intercept on`, every message and each direction's trailers are held, so the call waits for each forward in turn: request message, response message, trailers. The heads are held only when `checkpoint intercept` is on too.
- While a message is held, nothing more is read from its sender, so the client or origin that sent it waits; the other direction flows.
- `stream get` human output prints `payload: binary N bytes`; read the bytes with `--json` (base64, without the 5-byte envelope).
- The text form has no schema: bytes that read as packed varints show as `{0 0 0 0}`, not as a bytes literal. The text re-encodes to the same bytes either way.
- `--text` on a message with no text form (server-sent events, NDJSON) is refused with `invalid_checkpoint_request`, and so is text that does not parse, with its `line L column C`; the item stays held.
- `streamed_pair.started` names only the directions known at the first head, here the request; the response's framing follows in `streamed_pair.updated` once its head is admitted, and in `streamed_pair.ended`.
- Stop the interop server by its recorded PID in cleanup. `go run` leaves a child binary: kill the PID that `lsof` shows listening, not only the `go run` process.
