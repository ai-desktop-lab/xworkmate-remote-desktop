package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-workspace-lab/xworkmate-remote-desktop/server/desktop"
	"golang.org/x/net/websocket"
)

func accountsSessionMock(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/session" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		cookie, _ := r.Cookie(sessionCookieName)
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == "" && cookie != nil {
			token = cookie.Value
		}
		switch token {
		case "valid-sess-token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":{"id":"u-1","email":"operator@svc.plus","name":"Operator","role":"operator"}}`))
		case "expired-sess-token", "invalid-sess-token":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid or expired accounts session"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid session"}`))
		}
	}))
}

func accountsOnlyControlServer(t *testing.T, accountsURL string) *httptest.Server {
	t.Helper()
	// Keep production accounts/login hosts for cookie-domain and Origin checks,
	// then point introspection at the mock (same pattern as other accounts tests).
	srv := NewServer(Config{
		AccountsURL:  "https://accounts.svc.plus",
		LoginURL:     "https://console.svc.plus/login",
		AuthCacheTTL: 30 * time.Second,
	})
	srv.cfg.AccountsURL = accountsURL
	return httptest.NewServer(srv.setupRoutes())
}

func controlUpgradeRequest(url string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return req
}

func assertNoCredentialDisclosure(t *testing.T, raw []byte, secrets ...string) {
	t.Helper()
	body := strings.ToLower(string(raw))
	if strings.Contains(body, `"token"`) && strings.Contains(body, "sess") {
		t.Fatalf("response appears to disclose a session token: %s", raw)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatalf("response disclosed secret %q: %s", secret, raw)
		}
	}
}

func TestControlWebSocketFailClosedAnonymousAndInvalidSessions(t *testing.T) {
	mock := accountsSessionMock(t)
	defer mock.Close()
	ts := accountsOnlyControlServer(t, mock.URL)
	defer ts.Close()

	paths := []string{"/api/v1/control", "/ws/v1/connect"}
	cases := []struct {
		name   string
		header func(*http.Request)
		want   int
	}{
		{name: "anonymous_no_headers", want: http.StatusUnauthorized},
		{name: "invalid_bearer", header: func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid-sess-token") }, want: http.StatusUnauthorized},
		{name: "expired_cookie", header: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired-sess-token"}) }, want: http.StatusUnauthorized},
		{name: "expired_bearer", header: func(r *http.Request) { r.Header.Set("Authorization", "Bearer expired-sess-token") }, want: http.StatusUnauthorized},
	}

	client := ts.Client()
	for _, path := range paths {
		for _, tc := range cases {
			t.Run(tc.name+"_"+strings.TrimPrefix(path, "/"), func(t *testing.T) {
				req := controlUpgradeRequest(ts.URL + path)
				if tc.header != nil {
					tc.header(req)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("upgrade request: %v", err)
				}
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode != tc.want {
					t.Fatalf("expected HTTP %d, got %d (%s)", tc.want, resp.StatusCode, body)
				}
				if resp.StatusCode == http.StatusSwitchingProtocols {
					t.Fatal("anonymous/invalid session must not complete a WebSocket upgrade")
				}
				assertNoCredentialDisclosure(t, body, "valid-sess-token", "expired-sess-token", "invalid-sess-token")
			})
		}
	}
}

func TestControlWebSocketAccountsSessionCookieAndBearer(t *testing.T) {
	mock := accountsSessionMock(t)
	defer mock.Close()
	ts := accountsOnlyControlServer(t, mock.URL)
	defer ts.Close()

	// Status must advertise accounts SSO only and must not include a static token field.
	statusResp, err := ts.Client().Get(ts.URL + "/api/v1/auth/status")
	if err != nil {
		t.Fatalf("auth status: %v", err)
	}
	statusBody, _ := io.ReadAll(statusResp.Body)
	statusResp.Body.Close()
	var status map[string]any
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if status["accounts_enabled"] != true || status["token_enabled"] != false || status["authenticated"] != false {
		t.Fatalf("unexpected auth status: %s", statusBody)
	}
	if _, ok := status["token"]; ok {
		t.Fatalf("auth status disclosed a token field: %s", statusBody)
	}

	paths := []string{"/api/v1/control", "/ws/v1/connect"}
	for _, path := range paths {
		t.Run("cookie"+path, func(t *testing.T) {
			wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + path
			config, err := websocket.NewConfig(wsURL, "http://localhost")
			if err != nil {
				t.Fatalf("config: %v", err)
			}
			config.Header.Set("Cookie", sessionCookieName+"=valid-sess-token")
			config.Header.Set("Origin", "https://grok-bot-desktop.svc.plus")
			// Host of the test server is loopback; allow via Origin localhost alternative too if needed.
			// accounts parent domain is not in mock URL, so use localhost origin for the allowed path as well in bearer test.
			// Cookie + public origin should be accepted because LoginURL host is console.svc.plus / parent svc.plus.
			ws, err := websocket.DialConfig(config)
			if err != nil {
				t.Fatalf("dial with xc_session cookie: %v", err)
			}
			defer ws.Close()
			assertHelloAckWithoutSecrets(t, ws, "cookie-host", "valid-sess-token")
		})
		t.Run("bearer"+path, func(t *testing.T) {
			wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + path
			config, err := websocket.NewConfig(wsURL, "http://localhost")
			if err != nil {
				t.Fatalf("config: %v", err)
			}
			config.Header.Set("Authorization", "Bearer valid-sess-token")
			ws, err := websocket.DialConfig(config)
			if err != nil {
				t.Fatalf("dial with accounts bearer: %v", err)
			}
			defer ws.Close()
			assertHelloAckWithoutSecrets(t, ws, "bearer-host", "valid-sess-token")
		})
	}
}

func TestControlWebSocketRejectsCookieFromDisallowedOrigin(t *testing.T) {
	mock := accountsSessionMock(t)
	defer mock.Close()
	ts := accountsOnlyControlServer(t, mock.URL)
	defer ts.Close()

	req := controlUpgradeRequest(ts.URL + "/ws/v1/connect")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-sess-token"})
	req.Header.Set("Origin", "https://evil.example")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cookie+disallowed origin, got %d (%s)", resp.StatusCode, body)
	}
	assertNoCredentialDisclosure(t, body, "valid-sess-token")
}

func TestControlWebSocketAnonymousFailsWhenStaticTokenConfigured(t *testing.T) {
	srv := NewServer(Config{Token: "test-token"})
	ts := httptest.NewServer(srv.setupRoutes())
	defer ts.Close()

	req := controlUpgradeRequest(ts.URL + "/api/v1/control")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d (%s)", resp.StatusCode, body)
	}
	assertNoCredentialDisclosure(t, body, "test-token")
}

func assertHelloAckWithoutSecrets(t *testing.T, ws *websocket.Conn, deviceID, secret string) {
	t.Helper()
	hello := desktop.ControlFrame{
		V:    1,
		Type: "hello",
		Payload: map[string]any{
			"role":          "host",
			"deviceId":      deviceID,
			"clientVersion": "1.0.0",
		},
	}
	if err := websocket.JSON.Send(ws, hello); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ack desktop.ControlFrame
	if err := websocket.JSON.Receive(ws, &ack); err != nil {
		t.Fatalf("receive hello.ack: %v", err)
	}
	if ack.Type != "hello.ack" {
		t.Fatalf("expected hello.ack, got %s payload=%v", ack.Type, ack.Payload)
	}
	raw, err := json.Marshal(ack)
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	assertNoCredentialDisclosure(t, raw, secret)
}

func TestAuthorizeControlOriginAllowsSvcPlusAndLocalhost(t *testing.T) {
	s := NewServer(Config{
		AccountsURL: "https://accounts.svc.plus",
		LoginURL:    "https://console.svc.plus/login",
	})
	cases := []struct {
		origin string
		host   string
		ok     bool
	}{
		{origin: "", host: "grok-bot-desktop.svc.plus", ok: true},
		{origin: "https://grok-bot-desktop.svc.plus", host: "grok-bot-desktop.svc.plus", ok: true},
		{origin: "https://console.svc.plus", host: "grok-bot-desktop.svc.plus", ok: true},
		{origin: "https://accounts.svc.plus", host: "grok-bot-desktop.svc.plus", ok: true},
		{origin: "http://localhost:18093", host: "127.0.0.1:18093", ok: true},
		{origin: "https://evil.example", host: "grok-bot-desktop.svc.plus", ok: false},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://"+tc.host+"/ws/v1/connect", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			err := s.authorizeControlOrigin(req)
			if tc.ok && err != nil {
				t.Fatalf("expected origin %q to be allowed, got %v", tc.origin, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected origin %q to be rejected", tc.origin)
			}
		})
	}
}
