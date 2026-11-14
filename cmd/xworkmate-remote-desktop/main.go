package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-workspace-lab/xworkmate-remote-desktop/server/desktop"
)

const (
	sessionCookieName   = "xc_session"
	defaultAuthCacheTTL = 30 * time.Second
)

var version = "dev"

//go:embed web/index.html
var indexHTML []byte

type Config struct {
	ListenAddr    string
	Display       string
	TokenFile     string
	Token         string
	AccountsURL   string
	LoginURL      string
	AllowedEmails []string
	AllowedRoles  []string
	AuthCacheTTL  time.Duration
	ICEServers    []string
	UDPPortMin    uint16
	UDPPortMax    uint16
	NAT1To1IP     string
	RTPPort       int
	Width         int
	Height        int
	FPS           int
	Bitrate       int
}

func getEnv(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func parseUint16(s string, fallback uint16) uint16 {
	if s == "" {
		return fallback
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil {
		return fallback
	}
	return uint16(v)
}

func parseInt(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return v
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return fallback
	}
	return d
}

func parseCSV(s string, lower bool) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if lower {
			item = strings.ToLower(item)
		}
		if item != "" && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func parseFlagsAndEnv(args []string) (Config, bool, error) {
	fs := flag.NewFlagSet("xworkmate-remote-desktop", flag.ContinueOnError)

	var (
		listenFlag        = fs.String("listen", getEnv("XRD_LISTEN", "127.0.0.1:8090"), "HTTP listen address")
		displayFlag       = fs.String("display", getEnv("XRD_DISPLAY", ":1"), "X11 display to capture")
		tokenFileFlag     = fs.String("token-file", getEnv("XRD_TOKEN_FILE", ""), "Path to token file")
		tokenFlag         = fs.String("token", getEnv("XRD_TOKEN", ""), "Bearer token for API access")
		accountsURLFlag   = fs.String("accounts-url", getEnv("XRD_ACCOUNTS_URL", ""), "Base URL of accounts.svc.plus for SSO/session introspection")
		loginURLFlag      = fs.String("login-url", getEnv("XRD_LOGIN_URL", ""), "Web login URL for browser SSO redirect")
		allowedEmailsFlag = fs.String("allowed-emails", getEnv("XRD_ALLOWED_EMAILS", ""), "Comma-separated allowed account emails")
		allowedRolesFlag  = fs.String("allowed-roles", getEnv("XRD_ALLOWED_ROLES", ""), "Comma-separated allowed account roles")
		authCacheTTLFlag  = fs.Duration("auth-cache-ttl", parseDuration(os.Getenv("XRD_AUTH_CACHE_TTL"), defaultAuthCacheTTL), "Cache TTL for accounts session introspection")
		iceServersFlag    = fs.String("ice-servers", getEnv("XRD_ICE_SERVERS", ""), "Comma-separated STUN/TURN servers")
		udpMinFlag        = fs.Int("udp-port-min", int(parseUint16(os.Getenv("XRD_UDP_PORT_MIN"), 0)), "Pion SettingEngine min ephemeral UDP port")
		udpMaxFlag        = fs.Int("udp-port-max", int(parseUint16(os.Getenv("XRD_UDP_PORT_MAX"), 0)), "Pion SettingEngine max ephemeral UDP port")
		natIPFlag         = fs.String("nat1to1-ip", getEnv("XRD_NAT1TO1_IP", ""), "Public IP for NAT 1:1 host candidates")
		rtpPortFlag       = fs.Int("rtp-port", parseInt(os.Getenv("XRD_RTP_PORT"), desktop.DefaultRTPPort), "Local UDP port for GStreamer RTP")
		widthFlag         = fs.Int("width", parseInt(os.Getenv("XRD_WIDTH"), 1280), "Capture frame width")
		heightFlag        = fs.Int("height", parseInt(os.Getenv("XRD_HEIGHT"), 720), "Capture frame height")
		fpsFlag           = fs.Int("fps", parseInt(os.Getenv("XRD_FPS"), 30), "Capture frames per second")
		bitrateFlag       = fs.Int("bitrate", parseInt(os.Getenv("XRD_BITRATE"), 2000), "Video bitrate in kbps")
		versionFlag       = fs.Bool("version", false, "Print version and exit")
	)

	if err := fs.Parse(args); err != nil {
		return Config{}, false, err
	}

	if *versionFlag {
		return Config{}, true, nil
	}

	cfg := Config{
		ListenAddr:    *listenFlag,
		Display:       *displayFlag,
		TokenFile:     *tokenFileFlag,
		Token:         *tokenFlag,
		AccountsURL:   strings.TrimRight(strings.TrimSpace(*accountsURLFlag), "/"),
		LoginURL:      strings.TrimSpace(*loginURLFlag),
		AllowedEmails: parseCSV(*allowedEmailsFlag, true),
		AllowedRoles:  parseCSV(*allowedRolesFlag, true),
		AuthCacheTTL:  *authCacheTTLFlag,
		UDPPortMin:    uint16(*udpMinFlag),
		UDPPortMax:    uint16(*udpMaxFlag),
		NAT1To1IP:     *natIPFlag,
		RTPPort:       *rtpPortFlag,
		Width:         *widthFlag,
		Height:        *heightFlag,
		FPS:           *fpsFlag,
		Bitrate:       *bitrateFlag,
	}

	if cfg.AccountsURL != "" {
		u, err := url.Parse(cfg.AccountsURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, false, fmt.Errorf("invalid accounts-url %q", cfg.AccountsURL)
		}
	}

	if *iceServersFlag != "" {
		cfg.ICEServers = parseCSV(*iceServersFlag, false)
	}

	// Resolve token from file if provided
	if cfg.TokenFile != "" {
		content, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return Config{}, false, fmt.Errorf("failed to read token file: %w", err)
		}
		cfg.Token = strings.TrimSpace(string(content))
	}

	// Security validation: if both token and accounts auth are empty and listening non-loopback, refuse
	if cfg.Token == "" && cfg.AccountsURL == "" {
		host, _, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			host = cfg.ListenAddr
		}
		ip := net.ParseIP(host)
		isLoopback := ip != nil && ip.IsLoopback()
		if !isLoopback && host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return Config{}, false, errors.New("refusing to bind non-loopback address without an authorization token or accounts URL")
		}
		log.Println("WARNING: Running without authorization token or accounts URL on loopback address!")
	}

	return cfg, false, nil
}

type AuthIdentity struct {
	Method string   `json:"method"`
	UserID string   `json:"user_id,omitempty"`
	Email  string   `json:"email,omitempty"`
	Name   string   `json:"name,omitempty"`
	Role   string   `json:"role,omitempty"`
	Roles  []string `json:"roles,omitempty"`
}

type authCacheEntry struct {
	identity  AuthIdentity
	expiresAt time.Time
}

type accountsSessionUser struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	Name       string   `json:"name"`
	Username   string   `json:"username"`
	Role       string   `json:"role"`
	Roles      []string `json:"roles"`
	Active     *bool    `json:"active"`
	ArchivedAt *string  `json:"archivedAt"`
}

type Server struct {
	cfg        Config
	service    *desktop.Service
	httpClient *http.Client
	mu         sync.Mutex
	activeSess string
	authMu     sync.Mutex
	authCache  map[string]authCacheEntry
}

func NewServer(cfg Config) *Server {
	return &Server{
		cfg:        cfg,
		service:    desktop.GetService(),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		authCache:  make(map[string]authCacheEntry),
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func extractRequestToken(r *http.Request, bodyToken string) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authHeader) > len("Bearer ") && strings.EqualFold(authHeader[:len("Bearer ")], "Bearer ") {
		if tok := strings.TrimSpace(authHeader[len("Bearer "):]); tok != "" {
			return tok
		}
	}
	if tok := strings.TrimSpace(bodyToken); tok != "" {
		return tok
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if tok := strings.TrimSpace(c.Value); tok != "" {
			return tok
		}
	}
	return ""
}

func identityFromAccountUser(u *accountsSessionUser) (*AuthIdentity, int, error) {
	if u == nil || (strings.TrimSpace(u.ID) == "" && strings.TrimSpace(u.Email) == "") {
		return nil, http.StatusBadGateway, errors.New("accounts service returned empty user payload")
	}
	if u.Active != nil && !*u.Active {
		return nil, http.StatusForbidden, errors.New("account is inactive")
	}
	if u.ArchivedAt != nil && strings.TrimSpace(*u.ArchivedAt) != "" {
		return nil, http.StatusForbidden, errors.New("account is archived")
	}

	var roles []string
	if role := strings.ToLower(strings.TrimSpace(u.Role)); role != "" {
		roles = append(roles, role)
	}
	for _, rawRole := range u.Roles {
		if role := strings.ToLower(strings.TrimSpace(rawRole)); role != "" && !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}

	return &AuthIdentity{
		Method: "accounts_session",
		UserID: strings.TrimSpace(u.ID),
		Email:  strings.TrimSpace(u.Email),
		Name:   firstNonEmpty(u.Name, u.Username, u.Email),
		Role:   strings.TrimSpace(u.Role),
		Roles:  roles,
	}, http.StatusOK, nil
}

func cloneIdentity(id *AuthIdentity) *AuthIdentity {
	if id == nil {
		return nil
	}
	cp := *id
	if len(id.Roles) > 0 {
		cp.Roles = append([]string(nil), id.Roles...)
	}
	return &cp
}

func (s *Server) authorizeIdentity(id *AuthIdentity) (int, error) {
	if id == nil {
		return http.StatusUnauthorized, errors.New("missing authenticated identity")
	}
	if len(s.cfg.AllowedEmails) > 0 {
		email := strings.ToLower(strings.TrimSpace(id.Email))
		if email == "" || !slices.Contains(s.cfg.AllowedEmails, email) {
			return http.StatusForbidden, errors.New("account email is not allowed")
		}
	}
	if len(s.cfg.AllowedRoles) > 0 {
		allowed := false
		for _, r := range id.Roles {
			if slices.Contains(s.cfg.AllowedRoles, strings.ToLower(strings.TrimSpace(r))) {
				allowed = true
				break
			}
		}
		if !allowed && id.Role != "" && slices.Contains(s.cfg.AllowedRoles, strings.ToLower(strings.TrimSpace(id.Role))) {
			allowed = true
		}
		if !allowed {
			return http.StatusForbidden, errors.New("account role is not allowed")
		}
	}
	return http.StatusOK, nil
}

func (s *Server) cacheIdentity(token string, id *AuthIdentity) {
	if s.cfg.AuthCacheTTL <= 0 || token == "" || id == nil {
		return
	}
	now := time.Now()
	key := hashToken(token)
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.authCache == nil {
		s.authCache = make(map[string]authCacheEntry)
	}
	if len(s.authCache) > 256 {
		for k, entry := range s.authCache {
			if !now.Before(entry.expiresAt) {
				delete(s.authCache, k)
			}
		}
	}
	s.authCache[key] = authCacheEntry{
		identity:  *cloneIdentity(id),
		expiresAt: now.Add(s.cfg.AuthCacheTTL),
	}
}

func (s *Server) invalidateCachedToken(token string) {
	if token == "" {
		return
	}
	key := hashToken(token)
	s.authMu.Lock()
	delete(s.authCache, key)
	s.authMu.Unlock()
}

func (s *Server) introspectAccountsSession(ctx context.Context, token string) (*AuthIdentity, int, error) {
	if s.cfg.AuthCacheTTL > 0 {
		key := hashToken(token)
		now := time.Now()
		s.authMu.Lock()
		entry, ok := s.authCache[key]
		if ok && !now.Before(entry.expiresAt) {
			delete(s.authCache, key)
			ok = false
		}
		s.authMu.Unlock()
		if ok {
			id := cloneIdentity(&entry.identity)
			if status, err := s.authorizeIdentity(id); err != nil {
				return nil, status, err
			}
			return id, http.StatusOK, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.AccountsURL+"/api/auth/session", nil)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to build accounts request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("accounts service unreachable: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("failed reading accounts response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, http.StatusUnauthorized, errors.New("invalid or expired accounts session")
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, http.StatusForbidden, errors.New("accounts access denied")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, http.StatusBadGateway, fmt.Errorf("accounts service returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		User *accountsSessionUser `json:"user"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("invalid accounts session payload: %w", err)
	}

	id, status, err := identityFromAccountUser(payload.User)
	if err != nil {
		return nil, status, err
	}

	s.cacheIdentity(token, id)

	if status, err := s.authorizeIdentity(id); err != nil {
		return nil, status, err
	}
	return id, http.StatusOK, nil
}

func (s *Server) authenticateRequest(r *http.Request, bodyToken string) (*AuthIdentity, int, error) {
	if s.cfg.Token == "" && s.cfg.AccountsURL == "" {
		return &AuthIdentity{Method: "none"}, http.StatusOK, nil
	}

	token := extractRequestToken(r, bodyToken)
	if token == "" {
		return nil, http.StatusUnauthorized, errors.New("authentication required")
	}

	if s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) == 1 {
		return &AuthIdentity{Method: "static_token"}, http.StatusOK, nil
	}

	if s.cfg.AccountsURL != "" {
		return s.introspectAccountsSession(r.Context(), token)
	}

	return nil, http.StatusUnauthorized, errors.New("invalid authorization token")
}

func (s *Server) checkAuth(r *http.Request, bodyToken string) bool {
	_, status, err := s.authenticateRequest(r, bodyToken)
	return err == nil && status == http.StatusOK
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, status, err := s.authenticateRequest(r, "")
		if err != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		next(w, r)
	}
}

type SessionRequest struct {
	SDP   string `json:"sdp"`
	Token string `json:"token,omitempty"`
}

type SessionResponse struct {
	SessionID string `json:"session_id"`
	SDP       string `json:"sdp"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req SessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		if _, status, authErr := s.authenticateRequest(r, ""); authErr != nil {
			http.Error(w, http.StatusText(status), status)
			return
		}
		http.Error(w, "Invalid SDP payload", http.StatusBadRequest)
		return
	}

	if _, status, authErr := s.authenticateRequest(r, req.Token); authErr != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}

	if strings.TrimSpace(req.SDP) == "" {
		http.Error(w, "Invalid SDP payload", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.activeSess != "" {
		log.Printf("Stopping existing session: %s", s.activeSess)
		s.service.StopSession(s.activeSess)
		s.activeSess = ""
	}
	newSessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	s.activeSess = newSessionID
	s.mu.Unlock()

	pipelineCfg := desktop.PipelineConfig{
		Display:  s.cfg.Display,
		Port:     s.cfg.RTPPort,
		Width:    s.cfg.Width,
		Height:   s.cfg.Height,
		FPS:      s.cfg.FPS,
		Bitrate:  s.cfg.Bitrate,
		ToolType: "gstreamer",
	}

	opts := desktop.WebRTCOptions{
		UDPPortMin: s.cfg.UDPPortMin,
		UDPPortMax: s.cfg.UDPPortMax,
		NAT1To1IP:  s.cfg.NAT1To1IP,
	}

	sess, err := s.service.StartSessionWithOptions(newSessionID, pipelineCfg, s.cfg.ICEServers, opts)
	if err != nil {
		log.Printf("Failed to start session: %v", err)
		http.Error(w, fmt.Sprintf("Failed to initialize session: %v", err), http.StatusInternalServerError)
		return
	}

	answerSDP, err := sess.WebRTC.ProcessOffer(req.SDP)
	if err != nil {
		log.Printf("Failed to process offer: %v", err)
		s.service.StopSession(newSessionID)
		http.Error(w, fmt.Sprintf("Failed to process SDP offer: %v", err), http.StatusBadRequest)
		return
	}

	// Start GStreamer capture
	if err := s.service.StartCapture(newSessionID); err != nil {
		log.Printf("Failed to start capture: %v", err)
		s.service.StopSession(newSessionID)
		http.Error(w, fmt.Sprintf("Failed to start screen capture: %v", err), http.StatusInternalServerError)
		return
	}

	resp := SessionResponse{
		SessionID: newSessionID,
		SDP:       answerSDP,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var id string
	if len(pathParts) >= 3 && pathParts[1] == "session" {
		id = pathParts[2]
	}

	s.mu.Lock()
	if id == "" || id == s.activeSess {
		if s.activeSess != "" {
			s.service.StopSession(s.activeSess)
			s.activeSess = ""
		}
	} else {
		s.service.StopSession(id)
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"authenticated": false,
		"error":         code,
		"message":       msg,
	})
}

func normalizeRequestHost(r *http.Request) string {
	for _, headerName := range []string{"X-Forwarded-Host", "X-Original-Host", "X-Host"} {
		if raw := strings.TrimSpace(r.Header.Get(headerName)); raw != "" {
			if comma := strings.Index(raw, ","); comma >= 0 {
				raw = strings.TrimSpace(raw[:comma])
			}
			if h, _, err := net.SplitHostPort(raw); err == nil {
				raw = h
			}
			return strings.ToLower(strings.Trim(raw, "."))
		}
	}
	raw := strings.TrimSpace(r.Host)
	if h, _, err := net.SplitHostPort(raw); err == nil {
		raw = h
	}
	return strings.ToLower(strings.Trim(raw, "."))
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if comma := strings.Index(proto, ","); comma >= 0 {
		proto = strings.TrimSpace(proto[:comma])
	}
	return strings.EqualFold(proto, "https")
}

func (s *Server) resolveCookieDomain(r *http.Request) string {
	if s.cfg.AccountsURL == "" {
		return ""
	}
	u, err := url.Parse(s.cfg.AccountsURL)
	if err != nil {
		return ""
	}
	acctHost := strings.ToLower(u.Hostname())
	if acctHost == "" || acctHost == "localhost" || net.ParseIP(acctHost) != nil {
		return ""
	}
	parts := strings.Split(acctHost, ".")
	if len(parts) < 2 {
		return ""
	}
	parent := "." + strings.Join(parts[len(parts)-2:], ".")
	reqHost := normalizeRequestHost(r)
	if reqHost == "" {
		return ""
	}
	suffix := strings.TrimPrefix(parent, ".")
	if reqHost == suffix || strings.HasSuffix(reqHost, parent) {
		return parent
	}
	return ""
}

func (s *Server) setSessionCookies(w http.ResponseWriter, r *http.Request, value string, expires time.Time, maxAge int) {
	domain := s.resolveCookieDomain(r)
	secure := isHTTPSRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Domain:   domain,
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	if maxAge < 0 && domain != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func (s *Server) revokeAccountsSession(ctx context.Context, token string) {
	if s.cfg.AccountsURL == "" || token == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.cfg.AccountsURL+"/api/auth/session", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	resp, err := s.httpClient.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
	}
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var authModes []string
	if s.cfg.Token != "" {
		authModes = append(authModes, "static_token")
	}
	if s.cfg.AccountsURL != "" {
		authModes = append(authModes, "accounts_session")
	}

	resp := map[string]any{
		"authenticated":    false,
		"auth_modes":       authModes,
		"token_enabled":    s.cfg.Token != "",
		"accounts_enabled": s.cfg.AccountsURL != "",
		"login_url":        s.cfg.LoginURL,
	}

	if s.cfg.Token == "" && s.cfg.AccountsURL == "" {
		resp["authenticated"] = true
		resp["method"] = "none"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	hasCreds := extractRequestToken(r, "") != ""
	id, status, err := s.authenticateRequest(r, "")
	if err == nil && id != nil {
		resp["authenticated"] = true
		resp["method"] = id.Method
		if id.UserID != "" || id.Email != "" || id.Name != "" {
			resp["user"] = id
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if hasCreds && err != nil {
		resp["error"] = err.Error()
		if status == http.StatusForbidden {
			resp["forbidden"] = true
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.AccountsURL == "" {
		writeJSONError(w, http.StatusNotFound, "accounts_not_configured", "Accounts authentication is not configured on this desktop host")
		return
	}

	rawReq, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Identifier     string `json:"identifier"`
		Email          string `json:"email"`
		Username       string `json:"username"`
		Password       string `json:"password"`
		TOTPCodeCamel  string `json:"totpCode"`
		TOTPCodeSnake  string `json:"totp_code"`
		MFATicketCamel string `json:"mfaTicket"`
		MFATicketSnake string `json:"mfa_ticket"`
		Code           string `json:"code"`
	}
	if err := json.Unmarshal(rawReq, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", "Invalid login JSON payload")
		return
	}

	mfaTicket := firstNonEmpty(req.MFATicketSnake, req.MFATicketCamel)
	totpCode := firstNonEmpty(req.Code, req.TOTPCodeSnake, req.TOTPCodeCamel)

	var upstreamPath string
	var upstreamPayload any
	if mfaTicket != "" {
		if totpCode == "" {
			writeJSONError(w, http.StatusBadRequest, "mfa_code_required", "Authenticator code is required")
			return
		}
		upstreamPath = "/api/auth/mfa/verify"
		upstreamPayload = map[string]string{
			"mfa_ticket": mfaTicket,
			"code":       totpCode,
			"method":     "totp",
		}
	} else {
		identifier := firstNonEmpty(req.Identifier, req.Email, req.Username)
		if identifier == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_identifier", "Email or username is required")
			return
		}
		if strings.TrimSpace(req.Password) == "" && totpCode == "" {
			writeJSONError(w, http.StatusBadRequest, "missing_credentials", "Password or authenticator code is required")
			return
		}
		upstreamPath = "/api/auth/login"
		upstreamPayload = map[string]string{
			"identifier": identifier,
			"email":      strings.TrimSpace(req.Email),
			"password":   req.Password,
			"totpCode":   totpCode,
		}
	}

	bodyBytes, err := json.Marshal(upstreamPayload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "encode_error", "Failed to encode upstream login request")
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.AccountsURL+upstreamPath, bytes.NewReader(bodyBytes))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "request_error", "Failed to build accounts login request")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "application/json")

	upResp, err := s.httpClient.Do(upReq)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "accounts_unreachable", "Unable to reach accounts authentication service")
		return
	}
	defer upResp.Body.Close()

	upRaw, err := io.ReadAll(io.LimitReader(upResp.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream_read_failed", "Failed reading accounts response")
		return
	}

	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		var errBody struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(upRaw, &errBody)
		status := upResp.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		writeJSONError(w, status, firstNonEmpty(errBody.Error, "login_failed"), firstNonEmpty(errBody.Message, http.StatusText(status)))
		return
	}

	var loginData struct {
		MFARequiredCamel bool                 `json:"mfaRequired"`
		MFARequiredSnake bool                 `json:"mfa_required"`
		MFAMethodCamel   string               `json:"mfaMethod"`
		MFAMethodSnake   string               `json:"mfa_method"`
		MFATicketCamel   string               `json:"mfaTicket"`
		MFATicketSnake   string               `json:"mfa_ticket"`
		Token            string               `json:"token"`
		AccessToken      string               `json:"access_token"`
		ExpiresAt        string               `json:"expiresAt"`
		ExpiresIn        int64                `json:"expires_in"`
		User             *accountsSessionUser `json:"user"`
	}
	if err := json.Unmarshal(upRaw, &loginData); err != nil {
		writeJSONError(w, http.StatusBadGateway, "invalid_upstream_json", "Invalid JSON returned by accounts service")
		return
	}

	if loginData.MFARequiredCamel || loginData.MFARequiredSnake {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			"mfa_required":  true,
			"mfa_method":    firstNonEmpty(loginData.MFAMethodSnake, loginData.MFAMethodCamel, "totp"),
			"mfa_ticket":    firstNonEmpty(loginData.MFATicketSnake, loginData.MFATicketCamel),
			"message":       "MFA verification required",
		})
		return
	}

	tok := firstNonEmpty(loginData.Token, loginData.AccessToken)
	if tok == "" {
		writeJSONError(w, http.StatusBadGateway, "missing_token", "Accounts service login did not return a session token")
		return
	}

	var id *AuthIdentity
	var status int
	if loginData.User != nil {
		id, status, err = identityFromAccountUser(loginData.User)
		if err == nil {
			status, err = s.authorizeIdentity(id)
		}
	} else {
		id, status, err = s.introspectAccountsSession(r.Context(), tok)
	}
	if err != nil {
		s.revokeAccountsSession(r.Context(), tok)
		writeJSONError(w, status, "access_denied", err.Error())
		return
	}

	maxAge := 86400
	expTime := time.Now().Add(24 * time.Hour)
	if loginData.ExpiresAt != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, loginData.ExpiresAt); parseErr == nil {
			expTime = parsed
			if secs := int(time.Until(parsed).Seconds()); secs > 0 {
				maxAge = secs
			}
		}
	} else if loginData.ExpiresIn > 0 {
		maxAge = int(loginData.ExpiresIn)
		expTime = time.Now().Add(time.Duration(loginData.ExpiresIn) * time.Second)
	}

	s.cacheIdentity(tok, id)
	s.setSessionCookies(w, r, tok, expTime, maxAge)

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"method":        id.Method,
		"user":          id,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if tok := extractRequestToken(r, ""); tok != "" {
		s.invalidateCachedToken(tok)
		if s.cfg.Token == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.Token)) != 1 {
			s.revokeAccountsSession(r.Context(), tok)
		}
	}

	s.setSessionCookies(w, r, "", time.Unix(0, 0), -1)
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": false,
	})
}

func (s *Server) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("/api/session", s.handleCreateSession)
	mux.HandleFunc("/api/session/", s.authMiddleware(s.handleDeleteSession))
	return mux
}

func main() {
	cfg, printVer, err := parseFlagsAndEnv(os.Args[1:])
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	if printVer {
		fmt.Printf("xworkmate-remote-desktop %s\n", version)
		os.Exit(0)
	}

	srv := NewServer(cfg)
	mux := srv.setupRoutes()

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: mux,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		log.Printf("Starting xworkmate-remote-desktop %s listening on %s (display %s)", version, cfg.ListenAddr, cfg.Display)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down gracefully...")

	srv.mu.Lock()
	if srv.activeSess != "" {
		srv.service.StopSession(srv.activeSess)
		srv.activeSess = ""
	}
	srv.mu.Unlock()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	log.Println("Shutdown complete.")
}
