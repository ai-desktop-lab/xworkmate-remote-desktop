package desktop

import (
	"encoding/json"
	"errors"
	"net/http"
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

func TestControlGatewayCapabilityNegotiation(t *testing.T) {
	gw := NewControlGateway()
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Test 1: Restricted capabilities (Host without WebRTC / forceRelay)
	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create ws config: %v", err)
	}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	hello := ControlFrame{
		V:    1,
		Type: "hello",
		Payload: map[string]any{
			"role":          "host",
			"deviceId":      "relay-only-host",
			"clientVersion": "1.0.0",
			"capabilities":  []string{"transport.relay.resume.v1", "transport.relay", "transport.relay.attach"},
		},
	}
	if err := websocket.JSON.Send(ws, hello); err != nil {
		t.Fatalf("failed to send hello: %v", err)
	}

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack ControlFrame
	if err := websocket.JSON.Receive(ws, &ack); err != nil {
		t.Fatalf("failed to receive hello.ack: %v", err)
	}
	caps, ok := ack.Payload["capabilities"].([]any)
	if !ok {
		t.Fatalf("expected capabilities slice in hello.ack, got %T (%v)", ack.Payload["capabilities"], ack.Payload["capabilities"])
	}
	if len(caps) != 1 || caps[0] != "transport.relay" {
		t.Fatalf("expected strictly ['transport.relay'], got %v", caps)
	}
}

func TestControlGatewayRejectsUnauthenticatedHandshake(t *testing.T) {
	gw := NewControlGateway()
	gw.Authenticate = func(r *http.Request) error {
		return errors.New("authentication required")
	}
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create ws config: %v", err)
	}
	if _, err := websocket.DialConfig(config); err == nil {
		t.Fatal("expected handshake to fail without auth")
	}
}

func TestControlGatewayAllowsAuthenticatedHandshake(t *testing.T) {
	gw := NewControlGateway()
	var seenAuth bool
	gw.Authenticate = func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer good" {
			return errors.New("authentication required")
		}
		seenAuth = true
		return nil
	}
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		t.Fatalf("failed to create ws config: %v", err)
	}
	config.Header.Set("Authorization", "Bearer good")
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("expected authenticated dial to succeed: %v", err)
	}
	defer ws.Close()
	if !seenAuth {
		t.Fatal("expected authenticator to run")
	}

	hello := ControlFrame{V: 1, Type: "hello", Payload: map[string]any{"role": "host", "deviceId": "auth-host"}}
	if err := websocket.JSON.Send(ws, hello); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack ControlFrame
	if err := websocket.JSON.Receive(ws, &ack); err != nil {
		t.Fatalf("receive ack: %v", err)
	}
	if ack.Type != "hello.ack" {
		t.Fatalf("expected hello.ack, got %s", ack.Type)
	}
	raw, _ := json.Marshal(ack)
	if strings.Contains(strings.ToLower(string(raw)), "bearer") || strings.Contains(string(raw), "good") {
		t.Fatalf("hello.ack unexpectedly disclosed a credential: %s", raw)
	}
}
