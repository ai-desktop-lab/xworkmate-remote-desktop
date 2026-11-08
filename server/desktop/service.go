package desktop

import (
	"fmt"
	"log"
	"sync"

	"github.com/pion/webrtc/v4"
)

type DesktopSession struct {
	SessionID string
	Port      int
	Config    PipelineConfig
	Pipeline  *PipelineManager
	Injector  *XdotoolInjector
	WebRTC    *WebRTCServer
}

type Service struct {
	sessions map[string]*DesktopSession
	mu       sync.Mutex
}

var (
	instance *Service
	once     sync.Once
)

func GetService() *Service {
	once.Do(func() {
		instance = &Service{
			sessions: make(map[string]*DesktopSession),
		}
	})
	return instance
}

func (s *Service) StartSession(sessionID string, cfg PipelineConfig, iceServers []string) (*DesktopSession, error) {
	return s.StartSessionWithOptions(sessionID, cfg, iceServers, WebRTCOptions{})
}

func (s *Service) StartSessionWithOptions(sessionID string, cfg PipelineConfig, iceServers []string, opts WebRTCOptions) (*DesktopSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg.Port = normalizeRTPPort(cfg.Port)
	cfg.Display = ResolveDesktopDisplay(cfg.Display)

	s.stopSessionByIDLocked(sessionID)
	s.stopSessionsOnPortLocked(cfg.Port)

	log.Printf("Starting Remote Desktop session: %s", sessionID)

	// 1. Initialize input injector
	injector := NewXdotoolInjector(cfg.Display)
	if err := injector.Start(); err != nil {
		return nil, fmt.Errorf("failed to start input injector: %w", err)
	}

	// 2. Initialize WebRTC server
	webrtcSrv, err := NewWebRTCServer(injector)
	if err != nil {
		_ = injector.Close()
		return nil, fmt.Errorf("failed to create WebRTC server: %w", err)
	}

	if err := webrtcSrv.InitPeerConnectionWithOptions(iceServers, opts); err != nil {
		_ = injector.Close()
		return nil, fmt.Errorf("failed to init peer connection: %w", err)
	}

	// Start local UDP listener for GStreamer RTP packets
	if err := webrtcSrv.StartRTPReceiver(cfg.Port); err != nil {
		webrtcSrv.Close()
		_ = injector.Close()
		return nil, fmt.Errorf("failed to start RTP receiver: %w", err)
	}

	sess := &DesktopSession{
		SessionID: sessionID,
		Port:      cfg.Port,
		Config:    cfg,
		Pipeline:  NewPipelineManager(),
		Injector:  injector,
		WebRTC:    webrtcSrv,
	}

	s.sessions[sessionID] = sess
	return sess, nil
}

func (s *Service) StartCapture(sessionID string) error {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess.Pipeline == nil {
		sess.Pipeline = NewPipelineManager()
	}
	if sess.Pipeline.IsRunning() {
		return nil
	}
	if err := sess.Pipeline.Start(sess.Config); err != nil {
		return fmt.Errorf("failed to start capture pipeline: %w", err)
	}
	return nil
}

func (s *Service) GetSession(sessionID string) (*DesktopSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, exists := s.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	return sess, nil
}

func (s *Service) StopSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopSessionByIDLocked(sessionID)
}

func (s *Service) stopSessionByIDLocked(sessionID string) {
	if sess, exists := s.sessions[sessionID]; exists {
		s.stopSessionLocked(sess)
		delete(s.sessions, sessionID)
	}
}

func (s *Service) stopSessionsOnPortLocked(port int) {
	if port <= 0 {
		return
	}
	for sessionID, sess := range s.sessions {
		if sess.Port != port {
			continue
		}
		log.Printf("Stopping Remote Desktop session on RTP port %d: %s", port, sessionID)
		s.stopSessionLocked(sess)
		delete(s.sessions, sessionID)
	}
}

func (s *Service) stopSessionLocked(sess *DesktopSession) {
	log.Printf("Stopping Remote Desktop session: %s", sess.SessionID)
	if sess.Pipeline != nil {
		sess.Pipeline.Stop()
	}
	if sess.WebRTC != nil {
		sess.WebRTC.Close()
	}
	if sess.Injector != nil {
		_ = sess.Injector.Close()
	}
}

func (s *Service) AddICECandidate(sessionID string, candidate webrtc.ICECandidateInit) error {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	return sess.WebRTC.AddICECandidate(candidate)
}
