package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-workspace-lab/xworkmate-remote-desktop/server/desktop"
)

var version = "dev"

//go:embed web/index.html
var indexHTML []byte

type Config struct {
	ListenAddr string
	Display    string
	TokenFile  string
	Token      string
	ICEServers []string
	UDPPortMin uint16
	UDPPortMax uint16
	NAT1To1IP  string
	RTPPort    int
	Width      int
	Height     int
	FPS        int
	Bitrate    int
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

func parseFlagsAndEnv(args []string) (Config, bool, error) {
	fs := flag.NewFlagSet("xworkmate-remote-desktop", flag.ContinueOnError)

	var (
		listenFlag     = fs.String("listen", getEnv("XRD_LISTEN", "127.0.0.1:8090"), "HTTP listen address")
		displayFlag    = fs.String("display", getEnv("XRD_DISPLAY", ":1"), "X11 display to capture")
		tokenFileFlag  = fs.String("token-file", getEnv("XRD_TOKEN_FILE", ""), "Path to token file")
		tokenFlag      = fs.String("token", getEnv("XRD_TOKEN", ""), "Bearer token for API access")
		iceServersFlag = fs.String("ice-servers", getEnv("XRD_ICE_SERVERS", ""), "Comma-separated STUN/TURN servers")
		udpMinFlag     = fs.Int("udp-port-min", int(parseUint16(os.Getenv("XRD_UDP_PORT_MIN"), 0)), "Pion SettingEngine min ephemeral UDP port")
		udpMaxFlag     = fs.Int("udp-port-max", int(parseUint16(os.Getenv("XRD_UDP_PORT_MAX"), 0)), "Pion SettingEngine max ephemeral UDP port")
		natIPFlag      = fs.String("nat1to1-ip", getEnv("XRD_NAT1TO1_IP", ""), "Public IP for NAT 1:1 host candidates")
		rtpPortFlag    = fs.Int("rtp-port", parseInt(os.Getenv("XRD_RTP_PORT"), desktop.DefaultRTPPort), "Local UDP port for GStreamer RTP")
		widthFlag      = fs.Int("width", parseInt(os.Getenv("XRD_WIDTH"), 1280), "Capture frame width")
		heightFlag     = fs.Int("height", parseInt(os.Getenv("XRD_HEIGHT"), 720), "Capture frame height")
		fpsFlag        = fs.Int("fps", parseInt(os.Getenv("XRD_FPS"), 30), "Capture frames per second")
		bitrateFlag    = fs.Int("bitrate", parseInt(os.Getenv("XRD_BITRATE"), 2000), "Video bitrate in kbps")
		versionFlag    = fs.Bool("version", false, "Print version and exit")
	)

	if err := fs.Parse(args); err != nil {
		return Config{}, false, err
	}

	if *versionFlag {
		return Config{}, true, nil
	}

	cfg := Config{
		ListenAddr: *listenFlag,
		Display:    *displayFlag,
		TokenFile:  *tokenFileFlag,
		Token:      *tokenFlag,
		UDPPortMin: uint16(*udpMinFlag),
		UDPPortMax: uint16(*udpMaxFlag),
		NAT1To1IP:  *natIPFlag,
		RTPPort:    *rtpPortFlag,
		Width:      *widthFlag,
		Height:     *heightFlag,
		FPS:        *fpsFlag,
		Bitrate:    *bitrateFlag,
	}

	if *iceServersFlag != "" {
		for _, s := range strings.Split(*iceServersFlag, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				cfg.ICEServers = append(cfg.ICEServers, s)
			}
		}
	}

	// Resolve token from file if provided
	if cfg.TokenFile != "" {
		content, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return Config{}, false, fmt.Errorf("failed to read token file: %w", err)
		}
		cfg.Token = strings.TrimSpace(string(content))
	}

	// Security validation: if token is empty and listening non-loopback, refuse
	if cfg.Token == "" {
		host, _, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			host = cfg.ListenAddr
		}
		ip := net.ParseIP(host)
		isLoopback := ip != nil && ip.IsLoopback()
		if !isLoopback && host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return Config{}, false, errors.New("refusing to bind non-loopback address without an authorization token")
		}
		log.Println("WARNING: Running without authorization token on loopback address!")
	}

	return cfg, false, nil
}

type Server struct {
	cfg        Config
	service    *desktop.Service
	mu         sync.Mutex
	activeSess string
}

func NewServer(cfg Config) *Server {
	return &Server{
		cfg:     cfg,
		service: desktop.GetService(),
	}
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			next(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		token := ""
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type SessionRequest struct {
	SDP string `json:"sdp"`
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
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.SDP) == "" {
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

func (s *Server) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/session", s.authMiddleware(s.handleCreateSession))
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
