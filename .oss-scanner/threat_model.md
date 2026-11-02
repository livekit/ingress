# LiveKit Ingress threat model

`github.com/livekit/ingress` accepts media from outside a LiveKit deployment and
republishes it into a room. It takes RTMP and WHIP publishes from the public
internet and pulls media from URLs that API callers supply. It decodes and
transcodes that media with GStreamer through cgo, or forwards it as is when
transcoding is bypassed.

One binary runs in two roles. The long-lived service (`ingress`) runs the RTMP
listener, the WHIP HTTP server, and the localhost HTTP relay, and it starts a
handler subprocess (`ingress run-handler`) for each session. The handler runs
the GStreamer pipeline and publishes into the room. A fault in the service
affects every session on the host. A fault in a handler affects one session.

## Scope and trust boundaries

Audit the Go on `main`. Prioritise:

- `pkg/rtmp/`: the RTMP server and its handler (`OnPublish`, `OnAudio`,
  `OnVideo`, `OnSetDataFrame`), FLV tag decoding and re-encoding, the preroll
  buffer, and relay association by resource ID and relay token.
- `pkg/whip/`: the WHIP HTTP routes (`POST /{app}`, `POST /{app}/{stream_key}`,
  `DELETE` and `PATCH /{app}/{stream_key}/{resource_id}`), SDP offer handling,
  pion peer connections, RTP depacketizing and the jitter buffer, and the relay
  and proxy track handlers.
- `pkg/media/`: the GStreamer pipeline (`decodebin3` input, transcoding
  outputs) and the RTMP, WHIP, and URL-pull sources, including the caps
  allowlist in `pkg/media/urlpull`.
- `pkg/service/`: stream-key lookup, session and process management, the
  relay, and the debug handlers.
- `pkg/params/`, `pkg/config/`: per-session parameters, redaction of stream
  keys and tokens, and feature flags such as `enable_udp_url_pull`.
- `cmd/server`: how the service hands config, tokens, and ingress info to
  the handler subprocess.

Out of scope: generated code (`pkg/ipc/*.pb.go`), test harnesses under `test/`
and `cmd/whiptest`, `cmd/rtmprelay`, and `magefile.go`. Out of scope also are
bugs inside upstream dependencies: `go-rtmp`, `go-flv`, pion, GStreamer and its
plugins. Report how this repository uses them, for example a pipeline that
exposes a decoder to input it should not reach, but not a parser bug that
reproduces without this code.

Untrusted input:

- **Publishers (no credentials, or a stream key only).** Anyone who can reach
  the RTMP port or the WHIP port, before and after they present a stream key.
  This covers RTMP handshakes, chunks, AMF commands, and FLV audio, video, and
  script tags. It also covers WHIP request bodies (SDP offers) and the ICE,
  DTLS, SRTP, RTP, and RTCP that follow. All of the audio and video payloads
  (H.264, VP8, Opus, AAC, and the rest) are decoded by GStreamer.
- **API callers (hold the API key and secret).** They create ingresses and pick
  URL-pull sources: `http(s)://`, `srt://`, and `udp://` when it is enabled.
  The handler fetches those URLs (`souphttpsrc`, `srtclientsrc`, `udpsrc`)
  and everything they lead to, such as redirects and HLS playlists and
  segments. API callers are trusted to manage
  their own project's ingresses. They are not trusted with the ingress host's
  network: the `enable_udp_url_pull` documentation already treats a caller who
  can reach the handler's local network as a risk.
- **Remote media servers.** Responses to URL pulls are as untrusted as
  publisher media.

Trusted: the operator's config file and command-line flags, the API secret, the
redis message bus and the RPC responses that arrive over it (ingress info,
tokens, and room URLs from the LiveKit server), and the local handler
subprocess and its IPC socket.

LiveKit server (`livekit/livekit`) is enrolled separately, and
`livekit/protocol` has its own threat model. Report bugs in how this service validates stream keys, uses tokens,
or parses media here. Report bugs in ingress creation, stream-key issuance, or
the shared protocol helpers in those repositories.

## Security properties

- **Stream-key authentication:** media must reach a room only after its stream
  key resolves to an ingress, and only into that ingress's room as that
  ingress's participant. A publisher must not attach to, replace, or receive
  another session's stream.
- **Resource endpoints:** a WHIP `DELETE` or `PATCH` must act only for the
  holder of the stream key that created the resource. A resource ID alone must
  not be enough to end or alter someone else's session.
- **Relay:** only the handler that owns a session, holding its relay token,
  may read that session's media from the localhost relay.
- **Parsing:** malformed RTMP, FLV, SDP, RTP, or RTCP from a publisher must
  not crash the service, hang it, or allocate memory without bound. Request
  bodies and buffered media must have limits.
- **URL pull:** a caller-supplied URL must reach only the schemes the code
  allows, and `udp://` only when the operator enables it. It must not read
  local files, and must not reach the ingress host's own listeners (relay,
  debug, Prometheus, IPC) in a way that leaks data or changes state.
- **Isolation:** one session must not read, disrupt, or inject into another
  session, whether through shared buffers, resource IDs, relay tokens, or
  handler processes.
- **Secrets:** stream keys, relay tokens, room tokens, and the API secret must
  not appear in logs, errors, or state updates, except where the code redacts
  them on purpose.

## Severity guidance

- **Critical:** code execution on the ingress host from publisher input or
  from a URL-pull response, caused by this repository's code or its pipeline
  setup. Also critical: publishing into a room, or taking over another
  session, without a valid stream key.
- **High:** a crash or hang of the service process (all sessions) from one
  unauthenticated connection or request; ending or hijacking another
  publisher's session; reading another session's media; URL pull that reads
  local files or returns internal response bodies to the caller.
- **Medium:** unbounded memory or CPU from one unauthenticated client; a
  handler-only crash from publisher media; URL pull that reaches internal
  network addresses or the host's own listeners without returning their
  contents; a stream key or token in logs.
- **Low / not a finding:** behaviour that needs operator config (debug and
  Prometheus ports, `enable_udp_url_pull`, `development`) or the API secret
  used within its own project. Not a finding either: a resource limit that
  only the API caller can exhaust, bugs in generated or test code, or an
  upstream parser bug that reproduces without this code.

## Testing

The checkout is at `/src`. Go modules and the build cache are in the image, and
`GOPROXY=off` and `GOSUMDB=off` prevent downloads. GStreamer is installed
system-wide, and the service binary is at `/usr/local/bin/ingress`.

These are the package tests CI runs. They need no redis, room server, or
network:

```sh
cd /src
go test -count=1 -timeout 20m ./pkg/...
```

The integration suite in `test/` (build tag `integration`) needs a LiveKit
server, a WHIP publisher, and network media, so it does not run here.

Write reproducers as package tests that drive the handlers directly:
`pkg/whip/server_test.go` builds a `WHIPServer` with a stubbed publish
callback, and `pkg/service/service_url_pull_test.go` uses an in-process
message bus. The service can also run against the local redis:

```sh
redis-server --daemonize yes
ingress --config /src/test/config-sample.yaml
```

With no LiveKit server, stream-key lookup fails, so only the surface before it
is reachable: the RTMP handshake, chunking, and AMF commands on `:1935`, and
WHIP routing and request-body reads on `:8080`. This setup has not been
verified offline; if the config fails validation, fall back to package tests.

## Reports

State the attacker's required access (none, a valid stream key, or the API
secret), the entry point (RTMP, WHIP route, or URL-pull scheme), the process
affected (service or handler), and the demonstrated impact. Provide a minimal
reproducer, preferably a Go test in the affected package or a captured byte
stream.
