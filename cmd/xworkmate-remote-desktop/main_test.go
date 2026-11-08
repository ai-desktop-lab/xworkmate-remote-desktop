package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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
	req := httptest.NewRequest(http.MethodPost, "/api/session", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rr.Code)
	}

	// Invalid token
	req = httptest.NewRequest(http.MethodPost, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", rr.Code)
	}
}
