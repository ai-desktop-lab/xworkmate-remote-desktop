package desktop

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	desktopReliableInputChannelLabel = "input"
	desktopMoveInputChannelLabel     = "input-move"
	desktopICEGatheringTimeout       = 10 * time.Second
)

type WebRTCServer struct {
	peerConnection *webrtc.PeerConnection
	videoTrack     *webrtc.TrackLocalStaticRTP
	udpListener    net.PacketConn
	inputInjector  *XdotoolInjector
	mu             sync.Mutex
	isClosed       bool
	rtpPackets     uint64
	rtpBytes       uint64
	rtpWriteErrors uint64
}

func desktopWebRTCDiagnosticsEnabled() bool {
	value := os.Getenv("XWORKMATE_DESKTOP_WEBRTC_DEBUG")
	switch value {
	case "0", "false", "FALSE", "off", "OFF", "no", "NO":
		return false
	default:
		return true
	}
}

func NewWebRTCServer(injector *XdotoolInjector) (*WebRTCServer, error) {
	return &WebRTCServer{
		inputInjector: injector,
	}, nil
}

type WebRTCOptions struct {
	UDPPortMin uint16
	UDPPortMax uint16
	NAT1To1IP  string
}

// InitPeerConnection sets up the Pion PeerConnection and local H.264 video track
func (w *WebRTCServer) InitPeerConnection(iceServers []string) error {
	return w.InitPeerConnectionWithOptions(iceServers, WebRTCOptions{})
}

// InitPeerConnectionWithOptions sets up the Pion PeerConnection with custom options
func (w *WebRTCServer) InitPeerConnectionWithOptions(iceServers []string, opts WebRTCOptions) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var webrtcIceServers []webrtc.ICEServer
	for _, url := range iceServers {
		url = strings.TrimSpace(url)
		if url != "" {
			webrtcIceServers = append(webrtcIceServers, webrtc.ICEServer{
				URLs: []string{url},
			})
		}
	}
	if len(webrtcIceServers) == 0 {
		// Default STUN server
		webrtcIceServers = append(webrtcIceServers, webrtc.ICEServer{
			URLs: []string{"stun:stun.l.google.com:19302"},
		})
	}

	config := webrtc.Configuration{
		ICEServers: webrtcIceServers,
	}

	var pc *webrtc.PeerConnection
	var err error

	if (opts.UDPPortMin > 0 && opts.UDPPortMax >= opts.UDPPortMin) || opts.NAT1To1IP != "" {
		settingEngine := webrtc.SettingEngine{}
		if opts.UDPPortMin > 0 && opts.UDPPortMax >= opts.UDPPortMin {
			if err := settingEngine.SetEphemeralUDPPortRange(opts.UDPPortMin, opts.UDPPortMax); err != nil {
				return fmt.Errorf("failed to set ephemeral UDP port range: %w", err)
			}
		}
		if opts.NAT1To1IP != "" {
			settingEngine.SetNAT1To1IPs([]string{opts.NAT1To1IP}, webrtc.ICECandidateTypeHost)
		}
		api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))
		pc, err = api.NewPeerConnection(config)
	} else {
		pc, err = webrtc.NewPeerConnection(config)
	}
	if err != nil {
		return fmt.Errorf("failed to create WebRTC PeerConnection: %w", err)
	}

	// Create H.264 video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		"xworkmate-desktop",
	)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("failed to create video track: %w", err)
	}

	_, err = pc.AddTrack(videoTrack)
	if err != nil {
		_ = pc.Close()
		return fmt.Errorf("failed to add video track: %w", err)
	}

	// Handle Data Channel for inputs
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Printf("Data channel '%s'-'%d' opened", d.Label(), d.ID())
		label := d.Label()
		if !isDesktopInputDataChannelLabel(label) {
			return
		}
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			var event InputEvent
			if err := json.Unmarshal(msg.Data, &event); err != nil {
				log.Printf("Failed to unmarshal input event: %v", err)
				return
			}
			if label == desktopMoveInputChannelLabel && event.Type != "mouse_move" {
				log.Printf("Ignoring non-mouse_move input event on %s channel: %s", label, event.Type)
				return
			}
			if err := w.inputInjector.Inject(event); err != nil {
				log.Printf("Failed to inject input event: %v", err)
			}
		})
	})

	w.peerConnection = pc
	w.videoTrack = videoTrack

	return nil
}

func isDesktopInputDataChannelLabel(label string) bool {
	return label == desktopReliableInputChannelLabel || label == desktopMoveInputChannelLabel
}

// StartRTPReceiver listens on local UDP port for GStreamer RTP stream and forwards to WebRTC video track
func (w *WebRTCServer) StartRTPReceiver(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind UDP port %s: %w", addr, err)
	}

	w.mu.Lock()
	w.udpListener = conn
	w.mu.Unlock()

	go func() {
		buf := make([]byte, 2048)
		log.Printf("WebRTC RTP receiver listening on UDP %s", addr)
		statsDone := make(chan struct{})
		if desktopWebRTCDiagnosticsEnabled() {
			statsTicker := time.NewTicker(5 * time.Second)
			defer statsTicker.Stop()
			defer close(statsDone)
			go func() {
				var lastPackets uint64
				var lastBytes uint64
				for {
					select {
					case <-statsTicker.C:
						packets := atomic.LoadUint64(&w.rtpPackets)
						bytes := atomic.LoadUint64(&w.rtpBytes)
						errors := atomic.LoadUint64(&w.rtpWriteErrors)
						log.Printf(
							"WebRTC RTP stats: packets=%d bytes=%d packetDelta=%d byteDelta=%d writeErrors=%d",
							packets,
							bytes,
							packets-lastPackets,
							bytes-lastBytes,
							errors,
						)
						lastPackets = packets
						lastBytes = bytes
					case <-statsDone:
						return
					}
				}
			}()
		}
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				w.mu.Lock()
				closed := w.isClosed
				w.mu.Unlock()
				if !closed {
					log.Printf("UDP RTP read error: %v", err)
				}
				break
			}
			atomic.AddUint64(&w.rtpPackets, 1)
			atomic.AddUint64(&w.rtpBytes, uint64(n))

			// Forward packet directly to WebRTC track (zero-copy)
			w.mu.Lock()
			track := w.videoTrack
			w.mu.Unlock()

			if track != nil {
				if _, err := track.Write(buf[:n]); err != nil {
					atomic.AddUint64(&w.rtpWriteErrors, 1)
					log.Printf("Failed to write RTP packet to track: %v", err)
				}
			}
		}
	}()

	return nil
}

// ProcessOffer handles SDP offer, sets remote description, generates and returns SDP answer
func (w *WebRTCServer) ProcessOffer(sdpOffer string) (string, error) {
	w.mu.Lock()
	pc := w.peerConnection
	w.mu.Unlock()

	if pc == nil {
		return "", fmt.Errorf("peer connection not initialized")
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdpOffer,
	}

	if err := pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("failed to set remote description: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create SDP answer: %w", err)
	}

	// Gather ICE candidates
	gatherComplete := webrtc.GatheringCompletePromise(pc)

	if err := pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("failed to set local description: %w", err)
	}

	if err := waitForICEGatheringComplete(gatherComplete, desktopICEGatheringTimeout); err != nil {
		return "", err
	}

	localDesc := pc.LocalDescription()
	if localDesc == nil {
		return "", fmt.Errorf("local description is nil after gathering")
	}

	return localDesc.SDP, nil
}

func waitForICEGatheringComplete(done <-chan struct{}, timeout time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for ICE gathering after %s", timeout)
	}
}

// AddICECandidate adds a remote ICE candidate
func (w *WebRTCServer) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	w.mu.Lock()
	pc := w.peerConnection
	w.mu.Unlock()

	if pc == nil {
		return fmt.Errorf("peer connection not initialized")
	}

	if err := pc.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("failed to add remote ICE candidate: %w", err)
	}

	return nil
}

// Close terminates the WebRTC server
func (w *WebRTCServer) Close() {
	w.mu.Lock()
	if w.isClosed {
		w.mu.Unlock()
		return
	}
	w.isClosed = true
	pc := w.peerConnection
	conn := w.udpListener
	w.mu.Unlock()

	log.Println("Closing WebRTC server...")

	if conn != nil {
		_ = conn.Close()
	}

	if pc != nil {
		_ = pc.Close()
	}
	log.Printf(
		"WebRTC RTP final stats: packets=%d bytes=%d writeErrors=%d",
		atomic.LoadUint64(&w.rtpPackets),
		atomic.LoadUint64(&w.rtpBytes),
		atomic.LoadUint64(&w.rtpWriteErrors),
	)
}
