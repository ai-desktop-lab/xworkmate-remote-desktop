# XWorkmate Remote Desktop

Standalone remote-desktop transport extracted from `xworkmate-bridge` and
`xworkmate-app` without removing the original implementations yet.

## Layout

- `cmd/xworkmate-remote-desktop`: standalone HTTP/WebRTC server entrypoint and
  the embedded browser client (`web/index.html`).
- `server/desktop`: X11 capture, H.264 RTP pipeline, Pion WebRTC peer, input
  injection, session lifecycle, and their original tests.
- `protocol/schema`: versioned signaling and input-event contracts.
- `client/flutter/legacy`: copied Flutter/WebRTC client adapter and tests. This
  first snapshot still imports XWorkmate App types and is not yet a standalone
  Flutter package.
- `docs`: extraction plan and copied operational runbooks.
- `docs/test-cases.md`: runnable baseline, protocol, integration, and future
  cross-client verification matrix.

## Running the server

```bash
go build -o xworkmate-remote-desktop ./cmd/xworkmate-remote-desktop
XRD_TOKEN_FILE=/etc/xworkmate-remote-desktop/token ./xworkmate-remote-desktop
```

The server captures an existing X11 display, so a running X session (for
example Xvfb plus a window manager) must already exist. Runtime dependencies
are GStreamer (for `ximagesrc`/H.264 encoding) and `xdotool` (input injection).
`./xworkmate-remote-desktop --version` prints the version.

### Configuration

Every flag has an environment variable; the flag wins when both are set.

| Flag | Environment | Default | Purpose |
| :--- | :--- | :--- | :--- |
| `--listen` | `XRD_LISTEN` | `127.0.0.1:8090` | HTTP listen address |
| `--display` | `XRD_DISPLAY` | `:1` | X11 display to capture |
| `--token-file` | `XRD_TOKEN_FILE` | empty | File containing the access token |
| `--token` | `XRD_TOKEN` | empty | Access token (prefer `--token-file`) |
| `--ice-servers` | `XRD_ICE_SERVERS` | empty | Comma-separated STUN/TURN servers |
| `--udp-port-min` / `--udp-port-max` | `XRD_UDP_PORT_MIN` / `XRD_UDP_PORT_MAX` | `0` | UDP port range for WebRTC media |
| `--nat1to1-ip` | `XRD_NAT1TO1_IP` | empty | Public IP advertised as host candidate |
| `--rtp-port` | `XRD_RTP_PORT` | library default | Local UDP port for GStreamer RTP |
| `--width` / `--height` | `XRD_WIDTH` / `XRD_HEIGHT` | `1280` / `720` | Capture size |
| `--fps` | `XRD_FPS` | `30` | Capture frame rate |
| `--bitrate` | `XRD_BITRATE` | `2000` | Video bitrate in kbps |

## HTTP API

| Endpoint | Auth | Description |
| :--- | :--- | :--- |
| `GET /healthz` | none | Liveness probe |
| `GET /` | none | Embedded web client |
| `POST /api/session` | token | Body `{"sdp": "...", "token": "..."}`; returns `{"session_id", "sdp"}` |
| `DELETE /api/session/{id}` | token | Ends a session |

## Authentication

Send the token **in the request**, never in the URL:

- `Authorization: Bearer <token>` header, or
- `"token"` field in the `POST /api/session` JSON body (used by the web client).

Tokens are compared in constant time. Do not put a token in a query string: it
ends up in browser history, proxy logs, and the `Referer` header. If the web
client is opened with `?token=` or `#token=`, it moves the value into the
request body and removes it from the address bar with `history.replaceState()`,
but a link that already carried the token has still been exposed once, so treat
that token as leaked and rotate it.

Token handling rules:

- Prefer `XRD_TOKEN_FILE` (mode `0600`, owned by the service user). When both
  are set, the token file takes precedence over `XRD_TOKEN`.
- Do not put the token in a systemd `Environment=` line, a unit file, Git, or
  CI logs; those are readable by other users or end up in logs.
- With no token, the server only starts on a loopback address and logs a
  warning. It refuses to bind a non-loopback address without a token.

## Deployment notes

- Terminate TLS in front of the server (Caddy or Traefik) and keep the server on
  `127.0.0.1:8090`. Use `0.0.0.0` only when something else already restricts
  access to the port.
- WebRTC media uses UDP. Set `XRD_UDP_PORT_MIN`/`XRD_UDP_PORT_MAX` to a fixed
  range, open it in the firewall, and set `XRD_NAT1TO1_IP` when the host sits
  behind NAT or has only a private address.
- If the stream is a white screen, see
  `docs/runbooks/webrtc-remote-desktop-white-screen-runbook.md`.

## Tests

```bash
go test ./...
```

## Current status

This repository is an extraction baseline, not a production replacement. The
source projects remain authoritative while client package boundaries,
packaging, and deployment are completed. The standalone server now provides the
signaling endpoint, token authentication, and a browser client; integrating
identity from `accounts.svc.plus` is tracked separately.

The server currently uses X11 (`ximagesrc`/`x11grab`) and `xdotool`. Long term,
platform backends will move behind capture and input interfaces so the server
can add PipeWire/portal capture and libei input while `xworkmate-bridge` becomes
fully independent of X11.
