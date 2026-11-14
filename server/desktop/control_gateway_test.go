package desktop

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestControlGatewayHelloAndPing(t *testing.T) {
	gw := NewControlGateway()
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create ws config: %v", err)
	}

	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	// 1. Send hello
	hello := ControlFrame{
		V:    1,
		Type: "hello",
		Payload: map[string]any{
			"role":          "host",
			"deviceId":      "test-host-1",
			"clientVersion": "1.0.0",
		},
	}
	if err := websocket.JSON.Send(ws, hello); err != nil {
		t.Fatalf("failed to send hello: %v", err)
	}

	// 2. Expect hello.ack
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack ControlFrame
	if err := websocket.JSON.Receive(ws, &ack); err != nil {
		t.Fatalf("failed to receive hello.ack: %v", err)
	}
	if ack.Type != "hello.ack" {
		t.Fatalf("expected hello.ack, got %s", ack.Type)
	}
	if ack.Payload["serverVersion"] == nil {
		t.Fatalf("expected serverVersion in hello.ack payload")
	}

	// 3. Send ping
	ping := ControlFrame{
		V:    1,
		Type: "ping",
		Payload: map[string]any{
			"nonce": "test-nonce",
		},
	}
	if err := websocket.JSON.Send(ws, ping); err != nil {
		t.Fatalf("failed to send ping: %v", err)
	}

	// 4. Expect pong
	var pong ControlFrame
	if err := websocket.JSON.Receive(ws, &pong); err != nil {
		t.Fatalf("failed to receive pong: %v", err)
	}
	if pong.Type != "pong" {
		t.Fatalf("expected pong, got %s", pong.Type)
	}
	if pong.Payload["nonce"] != "test-nonce" {
		t.Fatalf("expected nonce test-nonce, got %v", pong.Payload["nonce"])
	}
}
