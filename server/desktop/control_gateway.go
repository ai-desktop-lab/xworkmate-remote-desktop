package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type ControlFrame struct {
	V         int            `json:"v"`
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp int64          `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
}

type ControlPeer struct {
	ID           string
	Role         string
	Version      string
	Capabilities []string
	Conn         *websocket.Conn
	LastActive   time.Time
	mu           sync.Mutex
}

func (p *ControlPeer) Send(frame ControlFrame) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if frame.V == 0 {
		frame.V = 1
	}
	if frame.ID == "" {
		frame.ID = "cf-" + randomHex(8)
	}
	if frame.Timestamp == 0 {
		frame.Timestamp = time.Now().UnixMilli()
	}
	if frame.Payload == nil {
		frame.Payload = make(map[string]any)
	}
	return websocket.JSON.Send(p.Conn, frame)
}

type ControlLink struct {
	ID                  string
	HostID              string
	ClientID            string
	Created             time.Time
	PreferredTransports []string
}

type ControlGateway struct {
	mu    sync.RWMutex
	peers map[string]*ControlPeer
	links map[string]*ControlLink
}

func NewControlGateway() *ControlGateway {
	return &ControlGateway{
		peers: make(map[string]*ControlPeer),
		links: make(map[string]*ControlLink),
	}
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (g *ControlGateway) Handler() http.Handler {
	server := websocket.Server{
		Handshake: func(config *websocket.Config, req *http.Request) error {
			return nil
		},
		Handler: g.handleWebSocket,
	}
	return server
}

func (g *ControlGateway) handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()
	var currentPeer *ControlPeer

	for {
		var frame ControlFrame
		err := websocket.JSON.Receive(ws, &frame)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}

		if frame.Payload == nil {
			frame.Payload = make(map[string]any)
		}

		switch frame.Type {
		case "hello":
			role, _ := frame.Payload["role"].(string)
			deviceID, _ := frame.Payload["deviceId"].(string)
			version, _ := frame.Payload["clientVersion"].(string)
			if role == "" {
				role = "host"
			}
			if deviceID == "" {
				deviceID = "dev-" + randomHex(8)
			}

			peer := &ControlPeer{
				ID:         deviceID,
				Role:       role,
				Version:    version,
				Conn:       ws,
				LastActive: time.Now(),
			}

			g.mu.Lock()
			g.peers[deviceID] = peer
			g.mu.Unlock()
			currentPeer = peer

			log.Printf("[ControlGateway] Authenticated %s device: %s (version: %s)", role, deviceID, version)

			_ = peer.Send(ControlFrame{
				V:         1,
				ID:        "cf-" + randomHex(8),
				Type:      "hello.ack",
				Timestamp: time.Now().UnixMilli(),
				Payload: map[string]any{
					"protocol":                1,
					"serverVersion":           "xworkmate/1.0.0",
					"connectionSessionId":     randomHex(16),
					"heartbeatIntervalMs":     25000,
					"maxControlFrameBytes":    131072,
					"maxRelayFrameBytes":      1048576,
					"capabilities":            []string{"transport.lan", "transport.p2p", "transport.turn", "transport.relay"},
					"webrtcEnabled":           true,
					"webrtcFallbackTimeoutMs": 12000,
				},
			})

		case "ping":
			if currentPeer != nil {
				currentPeer.LastActive = time.Now()
				nonce, _ := frame.Payload["nonce"].(string)
				if nonce == "" {
					nonce = randomHex(8)
				}
				_ = currentPeer.Send(ControlFrame{
					V:         1,
					ID:        "cf-" + randomHex(8),
					Type:      "pong",
					Timestamp: time.Now().UnixMilli(),
					Payload: map[string]any{
						"nonce": nonce,
					},
				})
			}

		case "pong":
			if currentPeer != nil {
				currentPeer.LastActive = time.Now()
			}

		case "connect.request":
			targetID, _ := frame.Payload["hostDeviceId"].(string)
			g.mu.RLock()
			targetHost, ok := g.peers[targetID]
			g.mu.RUnlock()

			if targetHost == nil && (targetID == "desktop-host" || targetID == "") {
				linkID := randomHex(16)
				if currentPeer != nil {
					_ = currentPeer.Send(ControlFrame{
						V:         1,
						ID:        "cf-" + randomHex(8),
						Type:      "connect.accepted",
						Timestamp: time.Now().UnixMilli(),
						Payload: map[string]any{
							"connectionId": linkID,
						},
					})
				}
				continue
			}

			if !ok || targetHost == nil {
				if currentPeer != nil {
					_ = currentPeer.Send(ControlFrame{
						V:         1,
						ID:        "cf-" + randomHex(8),
						Type:      "error",
						Timestamp: time.Now().UnixMilli(),
						Payload: map[string]any{
							"code":      "HOST_OFFLINE",
							"message":   "Host is offline.",
							"retryable": true,
						},
					})
				}
				continue
			}

			linkID := randomHex(16)
			link := &ControlLink{
				ID:       linkID,
				HostID:   targetID,
				ClientID: currentPeer.ID,
				Created:  time.Now(),
			}

			g.mu.Lock()
			g.links[linkID] = link
			g.mu.Unlock()

			_ = targetHost.Send(ControlFrame{
				V:         1,
				ID:        "cf-" + randomHex(8),
				Type:      "connect.incoming",
				Timestamp: time.Now().UnixMilli(),
				Payload: map[string]any{
					"connectionId":        linkID,
					"clientDeviceId":      currentPeer.ID,
					"clientIdentityKey":   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
					"authorization":       "account",
					"preferredTransports": []string{"lan", "p2p", "turn", "relay"},
				},
			})

		default:
			// Route through active link if connectionId is present
			connID, _ := frame.Payload["connectionId"].(string)
			if connID != "" {
				g.mu.RLock()
				link, ok := g.links[connID]
				g.mu.RUnlock()
				if ok && link != nil && currentPeer != nil {
					var otherID string
					if currentPeer.ID == link.HostID {
						otherID = link.ClientID
					} else {
						otherID = link.HostID
					}
					g.mu.RLock()
					otherPeer := g.peers[otherID]
					g.mu.RUnlock()
					if otherPeer != nil {
						if frame.V == 0 {
							frame.V = 1
						}
						if frame.ID == "" {
							frame.ID = "cf-" + randomHex(8)
						}
						if frame.Timestamp == 0 {
							frame.Timestamp = time.Now().UnixMilli()
						}
						_ = otherPeer.Send(frame)
					}
				}
			}
		}
	}

	if currentPeer != nil {
		g.mu.Lock()
		if g.peers[currentPeer.ID] == currentPeer {
			delete(g.peers, currentPeer.ID)
		}
		g.mu.Unlock()
		log.Printf("[ControlGateway] Disconnected %s device: %s", currentPeer.Role, currentPeer.ID)
	}
}
