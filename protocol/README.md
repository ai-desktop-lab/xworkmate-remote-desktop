# Remote Desktop Protocol v1

The media plane is WebRTC H.264 video. Reliable keyboard, button, and scroll
events use the ordered `input` data channel. High-frequency mouse movement uses
the unordered, short-lived `input-move` channel.

Signaling is intentionally transport-neutral. The schemas define payloads that
may be carried over HTTP, WebSocket, ACP during migration, or another gateway.

The HTTP agent/control API (screenshot, action, clipboard, windows) is documented
in the repository README and in `GET /api/v1/openapi.json`. Those routes require
an Authorization Bearer token. The media plane described here remains WebRTC H.264
(default capture 1280×720 @ 30fps software x264enc unless flags change it).
