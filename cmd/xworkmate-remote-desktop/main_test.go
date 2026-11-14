package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseFlagsAndEnvDefaults(t *testing.T) {
	cfg, printVer, err := parseFlagsAndEnv([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if printVer {
		t.Fatal("expected printVer=false")
	}
	if cfg.ListenAddr != "127.0.0.1:8090" {
		t.Errorf("expected default listen addr 127.0.0.1:8090, got %s", cfg.ListenAddr)
	}
	if cfg.Display != ":1" {
		t.Errorf("expected default display :1, got %s", cfg.Display)
	}
}

func TestParseFlagsAndEnvSecurityCheck(t *testing.T) {
	// Refuse non-loopback bind without token
	_, _, err := parseFlagsAndEnv([]string{"--listen=0.0.0.0:8090"})
	if err == nil {
		t.Fatal("expected error when binding non-loopback without token, got nil")
	}

	// Allowed non-loopback with token
	cfg, _, err := parseFlagsAndEnv([]string{"--listen=0.0.0.0:8090", "--token=secret-token"})
	if err != nil {
		t.Fatalf("unexpected error with token: %v", err)
	}
	if cfg.Token != "secret-token" {
		t.Errorf("expected token secret-token, got %s", cfg.Token)
	}
}

func TestParseFlagsAndEnvTokenFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "token-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	_, _ = tmpFile.WriteString("file-secret-123\n")
	_ = tmpFile.Close()

	cfg, _, err := parseFlagsAndEnv([]string{"--token-file=" + tmpFile.Name()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Token != "file-secret-123" {
		t.Errorf("expected file-secret-123, got %s", cfg.Token)
	}
}

func TestHealthzEndpoint(t *testing.T) {
	srv := NewServer(Config{Token: "test"})
	mux := srv.setupRoutes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok\n" {
		t.Errorf("expected ok\\n, got %q", rr.Body.String())
	}
}

func TestAuthMiddleware(t *testing.T) {
	srv := NewServer(Config{Token: "secret123"})
	mux := srv.setupRoutes()

	// Missing token on protected endpoint
	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"sdp":"test"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rr.Code)
	}

	// Invalid token in header
	req = httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"sdp":"test"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", rr.Code)
	}

	// Invalid token in body
	req = httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"sdp":"test","token":"wrong"}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid body token, got %d", rr.Code)
	}
}

func TestParseFlagsAndEnvAccountsConfig(t *testing.T) {
	cfg, _, err := parseFlagsAndEnv([]string{
		"--listen=0.0.0.0:8090",
		"--accounts-url=https://accounts.svc.plus/",
		"--login-url=https://console.svc.plus/login",
		"--allowed-emails=Admin@svc.plus, ops@svc.plus",
		"--allowed-roles=Admin, Operator",
		"--auth-cache-ttl=45s",
	})
	if err != nil {
		t.Fatalf("expected accounts-url to satisfy non-loopback check, got: %v", err)
	}
	if cfg.AccountsURL != "https://accounts.svc.plus" {
		t.Errorf("expected trimmed accounts URL, got %q", cfg.AccountsURL)
	}
	if cfg.LoginURL != "https://console.svc.plus/login" {
		t.Errorf("expected login URL, got %q", cfg.LoginURL)
	}
	if len(cfg.AllowedEmails) != 2 || cfg.AllowedEmails[0] != "admin@svc.plus" || cfg.AllowedEmails[1] != "ops@svc.plus" {
		t.Errorf("unexpected allowed emails: %#v", cfg.AllowedEmails)
	}
	if len(cfg.AllowedRoles) != 2 || cfg.AllowedRoles[0] != "admin" || cfg.AllowedRoles[1] != "operator" {
		t.Errorf("unexpected allowed roles: %#v", cfg.AllowedRoles)
	}
	if cfg.AuthCacheTTL != 45*time.Second {
		t.Errorf("expected 45s cache TTL, got %v", cfg.AuthCacheTTL)
	}

	if _, _, err := parseFlagsAndEnv([]string{"--accounts-url=not-a-url"}); err == nil {
		t.Fatal("expected error for invalid accounts-url, got nil")
	}
}

func TestAccountsSessionAuthAndCache(t *testing.T) {
	var sessionCalls atomic.Int32
	mockAccounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/session" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		sessionCalls.Add(1)
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-sess-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid session"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":{"id":"u-1","email":"operator@svc.plus","name":"Operator","role":"operator"}}`))
	}))
	defer mockAccounts.Close()

	srv := NewServer(Config{
		Token:        "breakglass-token",
		AccountsURL:  mockAccounts.URL,
		AuthCacheTTL: 30 * time.Second,
	})
	mux := srv.setupRoutes()

	// 1. Static break-glass token succeeds locally without calling accounts.
	req := httptest.NewRequest(http.MethodDelete, "/api/session/test-id", nil)
	req.Header.Set("Authorization", "Bearer breakglass-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for static token, got %d", rr.Code)
	}
	if got := sessionCalls.Load(); got != 0 {
		t.Fatalf("expected 0 accounts calls for static token, got %d", got)
	}

	// 2. xc_session cookie introspects accounts and passes auth (reaching payload check 400 on empty SDP).
	req = httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"sdp":""}`))
	req.AddCookie(&http.Cookie{Name: "xc_session", Value: "valid-sess-token"})
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (past auth) for valid xc_session cookie, got %d (%s)", rr.Code, rr.Body.String())
	}
	if got := sessionCalls.Load(); got != 1 {
		t.Fatalf("expected 1 accounts call, got %d", got)
	}

	// 3. Bearer accounts token reuses cache entry without another upstream call.
	req = httptest.NewRequest(http.MethodDelete, "/api/session/test-id", nil)
	req.Header.Set("Authorization", "Bearer valid-sess-token")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for cached accounts token, got %d", rr.Code)
	}
	if got := sessionCalls.Load(); got != 1 {
		t.Fatalf("expected cache hit keeping calls=1, got %d", got)
	}

	// 4. Invalid xc_session cookie returns 401.
	req = httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(`{"sdp":"v=0"}`))
	req.AddCookie(&http.Cookie{Name: "xc_session", Value: "expired-token"})
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid xc_session cookie, got %d", rr.Code)
	}
}

func TestAccountsRBACAllowlists(t *testing.T) {
	mockAccounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer viewer-tok":
			_, _ = w.Write([]byte(`{"user":{"id":"u-viewer","email":"viewer@svc.plus","role":"viewer"}}`))
		case "Bearer admin-wrong-email":
			_, _ = w.Write([]byte(`{"user":{"id":"u-admin2","email":"other@svc.plus","role":"admin"}}`))
		case "Bearer inactive-admin":
			_, _ = w.Write([]byte(`{"user":{"id":"u-inactive","email":"admin@svc.plus","role":"admin","active":false}}`))
		case "Bearer allowed-admin":
			_, _ = w.Write([]byte(`{"user":{"id":"u-admin","email":"Admin@svc.plus","roles":["User","Admin"]}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer mockAccounts.Close()

	srv := NewServer(Config{
		AccountsURL:   mockAccounts.URL,
		AllowedEmails: []string{"admin@svc.plus", "viewer@svc.plus"},
		AllowedRoles:  []string{"admin"},
	})
	mux := srv.setupRoutes()

	cases := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{"wrong_role", "viewer-tok", http.StatusForbidden},
		{"wrong_email", "admin-wrong-email", http.StatusForbidden},
		{"inactive_user", "inactive-admin", http.StatusForbidden},
		{"allowed_admin", "allowed-admin", http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodDelete, "/api/session/s1", nil)
			req.AddCookie(&http.Cookie{Name: "xc_session", Value: tc.token})
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("%s: expected HTTP %d, got %d (%s)", tc.name, tc.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestAuthEndpointsStatusLoginLogout(t *testing.T) {
	var deletedToken string
	mockAccounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth/login" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body["identifier"] {
			case "mfa@svc.plus":
				_, _ = w.Write([]byte(`{"mfaRequired":true,"mfa_method":"totp","mfa_ticket":"ticket-123"}`))
			case "denied@svc.plus":
				_, _ = w.Write([]byte(`{"token":"denied-tok","expires_in":3600,"user":{"id":"u-denied","email":"denied@svc.plus","role":"viewer"}}`))
			case "admin@svc.plus":
				_, _ = w.Write([]byte(`{"token":"good-tok","expires_in":3600,"user":{"id":"u-admin","email":"admin@svc.plus","role":"admin"}}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_credentials","message":"invalid credentials"}`))
			}
		case r.URL.Path == "/api/auth/mfa/verify" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["mfa_ticket"] == "ticket-123" && body["code"] == "123456" {
				_, _ = w.Write([]byte(`{"token":"mfa-tok","expires_in":3600,"user":{"id":"u-mfa","email":"admin@svc.plus","role":"admin"}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_mfa_code","message":"invalid totp code"}`))
		case r.URL.Path == "/api/auth/session" && r.Method == http.MethodGet:
			if r.Header.Get("Authorization") == "Bearer mfa-tok" || r.Header.Get("Authorization") == "Bearer good-tok" {
				_, _ = w.Write([]byte(`{"user":{"id":"u-admin","email":"admin@svc.plus","role":"admin"}}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/api/auth/session" && r.Method == http.MethodDelete:
			deletedToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockAccounts.Close()

	srv := NewServer(Config{
		Token:        "breakglass-tok",
		AccountsURL:  "https://accounts.svc.plus",
		LoginURL:     "https://console.svc.plus/login",
		AllowedRoles: []string{"admin"},
		AuthCacheTTL: 30 * time.Second,
	})
	// Route server HTTP calls to mockAccounts while preserving accounts.svc.plus URL for cookie domain matching.
	srv.cfg.AccountsURL = mockAccounts.URL
	mux := srv.setupRoutes()

	// 1. Unauthenticated status.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status expected 200, got %d", rr.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["authenticated"] != false || st["accounts_enabled"] != true || st["login_url"] != "https://console.svc.plus/login" {
		t.Fatalf("unexpected initial auth status: %v", st)
	}

	// 2. Static bearer status.
	req = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.Header.Set("Authorization", "Bearer breakglass-tok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	_ = json.Unmarshal(rr.Body.Bytes(), &st)
	if st["authenticated"] != true || st["method"] != "static_token" {
		t.Fatalf("expected static_token status, got %v", st)
	}

	// 3. Login MFA challenge flow.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"mfa@svc.plus","password":"pw"}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on MFA challenge, got %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &st)
	if st["mfa_required"] != true || st["mfa_ticket"] != "ticket-123" || len(rr.Result().Cookies()) != 0 {
		t.Fatalf("unexpected MFA challenge response: %v cookies=%v", st, rr.Result().Cookies())
	}

	// 4. Complete MFA on desktop.svc.plus -> verify HttpOnly + Secure cookie set (host-only when AccountsURL is loopback mock).
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"mfa_ticket":"ticket-123","code":"123456"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 after MFA verify, got %d (%s)", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "xc_session" || cookies[0].Value != "mfa-tok" || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected session cookies: %#v", cookies)
	}

	// 5. Rejected role on login returns 403, sets NO cookie, and revokes upstream session.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"denied@svc.plus","password":"pw"}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed role at login, got %d", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatalf("expected no cookie set for rejected user, got %#v", rr.Result().Cookies())
	}
	if deletedToken != "denied-tok" {
		t.Errorf("expected rejected session to be revoked upstream, got %q", deletedToken)
	}

	// 6. Status with valid xc_session cookie shows authenticated user.
	req = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: "xc_session", Value: "mfa-tok"})
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	_ = json.Unmarshal(rr.Body.Bytes(), &st)
	if st["authenticated"] != true || st["method"] != "accounts_session" {
		t.Fatalf("expected authenticated accounts_session status, got %v", st)
	}

	// 7. Logout revokes upstream session and clears xc_session cookie.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "xc_session", Value: "mfa-tok"})
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || deletedToken != "mfa-tok" {
		t.Fatalf("unexpected logout result: code=%d deletedToken=%q", rr.Code, deletedToken)
	}
	cleared := rr.Result().Cookies()
	if len(cleared) == 0 || cleared[0].MaxAge >= 0 {
		t.Fatalf("expected MaxAge < 0 clearing cookie, got %#v", cleared)
	}

	// 8. Cookie domain resolver matches shared parent .svc.plus only for svc.plus requests.
	prodSrv := NewServer(Config{AccountsURL: "https://accounts.svc.plus"})
	rSvc := httptest.NewRequest(http.MethodGet, "https://desktop.svc.plus/", nil)
	rSvc.Header.Set("X-Forwarded-Host", "desktop.svc.plus")
	if got := prodSrv.resolveCookieDomain(rSvc); got != ".svc.plus" {
		t.Errorf("expected .svc.plus cookie domain, got %q", got)
	}
	rSit := httptest.NewRequest(http.MethodGet, "https://desktop.sit.onwalk.net/", nil)
	rSit.Header.Set("X-Forwarded-Host", "desktop.sit.onwalk.net")
	if got := prodSrv.resolveCookieDomain(rSit); got != "" {
		t.Errorf("expected empty host-only cookie domain for onwalk.net, got %q", got)
	}
}

func TestAPINotFoundReturnsJSONNotHTML(t *testing.T) {
	srv := NewServer(Config{Token: "test"})
	mux := srv.setupRoutes()

	// 1. Root and web routes serve HTML
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("expected 200 text/html for /, got %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}

	// 2. Unmatched /api/ routes MUST return 404 JSON, NEVER HTML!
	req = httptest.NewRequest(http.MethodPost, "/api/v1/non_existent_route", strings.NewReader(`{}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unmatched /api route, got %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected application/json for unmatched /api route, got %s", rr.Header().Get("Content-Type"))
	}
	var errResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse JSON 404 response: %v", err)
	}
	if errResp["error"] != "not_found" {
		t.Errorf("expected error=not_found, got %v", errResp["error"])
	}
}

func TestV1AuthCompatibilityAndDeviceAPIs(t *testing.T) {
	srv := NewServer(Config{Token: "test-secret-token-123456"})
	mux := srv.setupRoutes()

	// 1. Login with static token via /api/v1/auth/login
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"admin@svc.plus","password":"test-secret-token-123456"}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, loginReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 login with static token, got %d (%s)", rr.Code, rr.Body.String())
	}
	var loginResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if loginResp["token"] != "test-secret-token-123456" {
		t.Errorf("expected token in login response, got %v", loginResp["token"])
	}
	if loginResp["expiresAt"] == nil {
		t.Errorf("expected numeric expiresAt in login response")
	}
	if loginResp["isAdmin"] != true {
		t.Errorf("expected isAdmin true, got %v", loginResp["isAdmin"])
	}

	// 2. OAuth providers endpoint returns empty list for standard auth
	provReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/providers", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, provReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for oauth providers, got %d", rr.Code)
	}

	// 3. Device register endpoint returns valid tokens
	regReq := httptest.NewRequest(http.MethodPost, "/api/v1/devices/register", strings.NewReader(`{"v":1,"device":{"name":"dsh"}}`))
	regReq.Header.Set("Authorization", "Bearer test-secret-token-123456")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, regReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for device registration, got %d", rr.Code)
	}
	var regResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &regResp)
	if regResp["deviceToken"] == nil {
		t.Errorf("expected deviceToken in response")
	}

	// 4. Device list returns desktop host
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	listReq.Header.Set("Authorization", "Bearer test-secret-token-123456")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, listReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for devices list, got %d", rr.Code)
	}
	var listResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	items, ok := listResp["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected at least 1 device in items, got %v", listResp["items"])
	}
}

func TestAIAgentDesktopEndpoints(t *testing.T) {
	srv := NewServer(Config{Token: "agent-token-123"})
	mux := srv.setupRoutes()

	// 1. Desktop Info
	infoReq := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/info", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, infoReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for desktop info, got %d", rr.Code)
	}
	var info map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info["width"] == nil || info["height"] == nil {
		t.Errorf("expected width and height in info, got %v", info)
	}

	// 2. Desktop Screenshot (JSON)
	shotReq := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/screenshot", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, shotReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for screenshot, got %d", rr.Code)
	}
	var shotResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &shotResp)
	if shotResp["base64"] == nil || shotResp["format"] != "png" {
		t.Errorf("expected base64 png in screenshot response, got %v", shotResp)
	}

	// 3. Desktop Screenshot (Raw Image)
	shotRawReq := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/screenshot?raw=true", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, shotRawReq)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("expected 200 image/png, got %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}

	// 4. Desktop Action (Single Sleep Action)
	actReq := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/action", strings.NewReader(`{"action":"sleep","duration_ms":10}`))
	actReq.Header.Set("Authorization", "Bearer agent-token-123")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, actReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for action, got %d (%s)", rr.Code, rr.Body.String())
	}
	var actResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &actResp)
	if actResp["success"] != true {
		t.Errorf("expected action success=true, got %v", actResp)
	}

	// 5. Desktop Action (Batch with Return Screenshot)
	batchReq := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/action", strings.NewReader(`{
		"actions": [
			{"action":"sleep","duration_ms":5},
			{"action":"sleep","duration_ms":5}
		],
		"return_screenshot": true
	}`))
	batchReq.Header.Set("Authorization", "Bearer agent-token-123")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, batchReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for batch action, got %d", rr.Code)
	}
	var batchResp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &batchResp)
	if batchResp["success"] != true || batchResp["screenshot"] == nil {
		t.Errorf("expected batch success with screenshot, got %v", batchResp)
	}

	// 6. MCP Manifest
	mcpReq := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/mcp/manifest", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, mcpReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for MCP manifest, got %d", rr.Code)
	}

	// 7. OpenAPI Document
	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, apiReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for OpenAPI spec, got %d", rr.Code)
	}
}
