package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-workspace-lab/xworkmate-remote-desktop/server/desktop"
)

// handleOAuthProviders returns supported OAuth discovery items (empty for standard password/token auth)
func (s *Server) handleOAuthProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"items": []any{},
	})
}

// handleAuthMe returns current account profile conforming to DSH Remote spec
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	id, _, _ := s.authenticateRequest(r, "")
	account := "guest@svc.plus"
	isAdmin := false
	var roles []string
	if id != nil {
		account = firstNonEmpty(id.Email, id.Name, id.UserID, "user@svc.plus")
		roles = id.Roles
		for _, role := range roles {
			if strings.EqualFold(role, "admin") {
				isAdmin = true
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account": account,
		"isAdmin": isAdmin,
		"email":   account,
		"roles":   roles,
	})
}

// cleanDesktopNameID converts a device name/id into a clean, DNS-friendly name-id for desktop-<name-id>.svc.plus
func cleanDesktopNameID(deviceID, name, platform string) string {
	if deviceID == "desktop-host" {
		return "host"
	}
	src := strings.ToLower(name)
	src = strings.TrimSuffix(src, ".local")
	src = strings.TrimSuffix(src, ".lan")
	var b strings.Builder
	for _, r := range src {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '-' || r == '_' || r == '.' || r == ' ' {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteRune('-')
			}
		}
	}
	res := strings.Trim(b.String(), "-")
	if len(res) == 0 {
		if len(deviceID) >= 8 {
			res = "dev-" + deviceID[:8]
		} else if platform != "" {
			res = "dev-" + strings.ToLower(platform)
		} else {
			res = "dev"
		}
	}
	if len(res) > 32 {
		res = strings.TrimRight(res[:32], "-")
	}
	return res
}

// collectAllDevices aggregates desktop-host, registered devices, and active WebSocket control peers
func (s *Server) collectAllDevices() []map[string]any {
	wWidth, wHeight := s.agentCtrl.GetGeometry()
	items := []map[string]any{
		{
			"deviceId":       "desktop-host",
			"name":           fmt.Sprintf("XWorkmate WebRTC Desktop (%dx%d)", wWidth, wHeight),
			"role":           "host",
			"platform":       "linux",
			"identityKey":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"online":         true,
			"membershipId":   "desktop-host",
			"harnessVersion": "1.0.0",
			"clientVersion":  "1.0.0",
		},
	}
	seen := map[string]bool{"desktop-host": true}

	s.mu.Lock()
	for id, dev := range s.devices {
		if !seen[id] {
			seen[id] = true
			items = append(items, dev)
		}
	}
	s.mu.Unlock()

	if s.controlGw != nil {
		for _, peer := range s.controlGw.GetPeers() {
			if !seen[peer.ID] {
				seen[peer.ID] = true
				name := peer.ID
				platform := "darwin"
				s.mu.Lock()
				if cached, ok := s.devices[peer.ID]; ok {
					if cName, ok := cached["name"].(string); ok && cName != "" {
						name = cName
					}
					if cPlat, ok := cached["platform"].(string); ok && cPlat != "" {
						platform = cPlat
					}
				}
				s.mu.Unlock()
				items = append(items, map[string]any{
					"deviceId":       peer.ID,
					"name":           name,
					"role":           peer.Role,
					"platform":       platform,
					"identityKey":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
					"online":         true,
					"membershipId":   peer.ID,
					"harnessVersion": "1.0.0",
					"clientVersion":  peer.Version,
				})
			}
		}
	}

	// Assign an individual desktop name id and domain to every remote desktop
	usedNameIDs := make(map[string]int)
	for i := range items {
		devID, _ := items[i]["deviceId"].(string)
		devName, _ := items[i]["name"].(string)
		devPlat, _ := items[i]["platform"].(string)

		nameID := cleanDesktopNameID(devID, devName, devPlat)
		if count := usedNameIDs[nameID]; count > 0 {
			nameID = fmt.Sprintf("%s-%d", nameID, count+1)
		}
		usedNameIDs[nameID]++

		desktopDomain := fmt.Sprintf("desktop-%s.svc.plus", nameID)
		items[i]["desktopNameId"] = nameID
		items[i]["nameId"] = nameID
		if nameID == "host" || devID == "desktop-host" {
			items[i]["desktopDomain"] = "desktop.svc.plus"
			items[i]["desktopUrl"] = "https://desktop.svc.plus"
		} else {
			items[i]["desktopDomain"] = desktopDomain
			items[i]["desktopUrl"] = "https://" + desktopDomain
		}
		items[i]["rdmUrl"] = "https://rdm.svc.plus/app/hosts"
	}

	return items
}

// handleAccountDevices returns devices list with serverUrl and transport
func (s *Server) handleAccountDevices(w http.ResponseWriter, r *http.Request) {
	items := s.collectAllDevices()
	serverURL := "https://rdm.svc.plus"
	if s.cfg.LoginURL != "" {
		serverURL = s.cfg.LoginURL
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"serverUrl": serverURL,
		"rdmUrl":    "https://rdm.svc.plus",
		"transport": "webrtc",
	})
}

// handleDeviceRegister handles DSH Remote device registration
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	id, _, _ := s.authenticateRequest(r, "")
	token := extractRequestToken(r, "")
	if len(token) < 16 {
		token = "dev-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "0123456789abcdef"
	}
	if id != nil {
		s.cacheIdentity(token, id)
	}

	var req struct {
		V      int `json:"v"`
		Device struct {
			Role         string `json:"role"`
			DeviceID     string `json:"deviceId"`
			Name         string `json:"name"`
			Platform     string `json:"platform"`
			IdentityKey  string `json:"identityKey"`
			MembershipID string `json:"membershipId"`
		} `json:"device"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Device.DeviceID != "" {
		s.mu.Lock()
		if s.devices == nil {
			s.devices = make(map[string]map[string]any)
		}
		memID := firstNonEmpty(req.Device.MembershipID, req.Device.DeviceID)
		identKey := firstNonEmpty(req.Device.IdentityKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		role := firstNonEmpty(req.Device.Role, "client")
		platform := firstNonEmpty(req.Device.Platform, "darwin")
		name := firstNonEmpty(req.Device.Name, req.Device.DeviceID)
		s.devices[req.Device.DeviceID] = map[string]any{
			"deviceId":     req.Device.DeviceID,
			"name":         name,
			"role":         role,
			"platform":     platform,
			"identityKey":  identKey,
			"membershipId": memID,
			"online":       true,
			"lastSeenAt":   time.Now().UnixMilli(),
		}
		s.mu.Unlock()
	}

	expTime := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":           token,
		"accessTokenExpiresAt":  expTime,
		"refreshToken":          token,
		"refreshTokenExpiresAt": expTime,
	})
}

// handleAuthRefresh handles token rotation for DSH Remote
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	token := firstNonEmpty(body.RefreshToken, extractRequestToken(r, ""))
	if len(token) < 16 {
		token = "refresh-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "0123456789abcdef"
	}
	id, _, _ := s.authenticateRequest(r, token)
	if id != nil {
		s.cacheIdentity(token, id)
	}
	expTime := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":           token,
		"accessTokenExpiresAt":  expTime,
		"refreshToken":          token,
		"refreshTokenExpiresAt": expTime,
	})
}

// handleTurnCredentials returns ICE / TURN servers for WebRTC transport
func (s *Server) handleTurnCredentials(w http.ResponseWriter, r *http.Request) {
	var iceList []map[string]any
	for _, srv := range s.cfg.ICEServers {
		if strings.TrimSpace(srv) != "" {
			iceList = append(iceList, map[string]any{
				"urls": srv,
			})
		}
	}
	if iceList == nil {
		iceList = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iceServers": iceList,
	})
}

// handleDevicesList lists this desktop host as an available remote device
func (s *Server) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	items := s.collectAllDevices()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

// handleDeviceItem handles device sub-paths (/ice-servers, /presence, /self)
func (s *Server) handleDeviceItem(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if strings.Contains(path, "register") {
		s.handleDeviceRegister(w, r)
		return
	}
	if strings.HasSuffix(path, "/self") && r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasSuffix(path, "/ice-servers") {
		writeJSON(w, http.StatusOK, map[string]any{
			"iceServers": s.cfg.ICEServers,
		})
		return
	}
	if strings.HasSuffix(path, "/presence") {
		writeJSON(w, http.StatusOK, map[string]any{
			"online":     true,
			"lastSeenAt": time.Now().UnixMilli(),
		})
		return
	}

	parts := strings.Split(path, "/")
	targetDeviceID := parts[len(parts)-1]

	s.mu.Lock()
	cached, found := s.devices[targetDeviceID]
	s.mu.Unlock()

	if found && cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	if targetDeviceID == "desktop-host" {
		wWidth, wHeight := s.agentCtrl.GetGeometry()
		writeJSON(w, http.StatusOK, map[string]any{
			"deviceId":       "desktop-host",
			"name":           fmt.Sprintf("XWorkmate WebRTC Desktop (%dx%d)", wWidth, wHeight),
			"role":           "host",
			"platform":       "linux",
			"identityKey":    "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"online":         true,
			"membershipId":   "desktop-host",
			"harnessVersion": "1.0.0",
			"clientVersion":  "1.0.0",
			"lastSeenAt":     time.Now().UnixMilli(),
		})
		return
	}

	role := "client"
	if strings.Contains(targetDeviceID, "host") {
		role = "host"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId":     targetDeviceID,
		"name":         targetDeviceID,
		"role":         role,
		"platform":     "darwin",
		"identityKey":  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"membershipId": targetDeviceID,
		"online":       true,
		"lastSeenAt":   time.Now().UnixMilli(),
	})
}

// handleDesktopInfo returns display geometry, mouse position, active window, and capabilities
func (s *Server) handleDesktopInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.agentCtrl.GetDisplayInfo(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "display_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleDesktopScreenshot captures a screenshot of the X11 screen
func (s *Server) handleDesktopScreenshot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	raw := q.Get("raw") == "true" || strings.Contains(r.Header.Get("Accept"), "image/")
	scale := 0.0
	if scaleStr := strings.TrimSpace(q.Get("scale")); scaleStr != "" {
		if parsed, err := strconv.ParseFloat(scaleStr, 64); err == nil {
			scale = parsed
		}
	}

	if r.Method == http.MethodPost && r.Body != nil {
		var body struct {
			Format string   `json:"format"`
			Base64 *bool    `json:"base64"`
			Scale  *float64 `json:"scale"`
			Raw    bool     `json:"raw"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err == nil {
			if strings.TrimSpace(body.Format) != "" {
				format = body.Format
			}
			if body.Scale != nil && *body.Scale > 0 {
				scale = *body.Scale
			}
			if body.Raw {
				raw = true
			}
			if body.Base64 != nil && !*body.Base64 {
				raw = true
			}
		}
	}

	opts := desktop.ScreenshotOptions{
		Format: format,
		Scale:  scale,
	}

	bytes, outFormat, err := s.agentCtrl.CaptureScreenshot(r.Context(), opts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "screenshot_failed", err.Error())
		return
	}

	if raw && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "image/"+outFormat)
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes)
		return
	}

	wWidth, wHeight := s.agentCtrl.GetGeometry()
	if scale > 0 && scale != 1 {
		wWidth = int(float64(wWidth) * scale)
		wHeight = int(float64(wHeight) * scale)
		if wWidth < 1 {
			wWidth = 1
		}
		if wHeight < 1 {
			wHeight = 1
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"format":    outFormat,
		"width":     wWidth,
		"height":    wHeight,
		"base64":    base64.StdEncoding.EncodeToString(bytes),
		"timestamp": time.Now().Unix(),
	})
}

// handleDesktopAction executes single or batch actions for AI agents
func (s *Server) handleDesktopAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "Failed to read request body")
		return
	}
	defer r.Body.Close()

	var req struct {
		desktop.AgentAction
		Actions          []desktop.AgentAction `json:"actions,omitempty"`
		ReturnScreenshot bool                  `json:"return_screenshot,omitempty"`
		Format           string                `json:"format,omitempty"`
	}

	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", "Failed to parse action JSON")
		return
	}

	shotOpts := desktop.ScreenshotOptions{
		Format: req.Format,
	}

	// Case 1: Batch of actions
	if len(req.Actions) > 0 {
		batchRes, batchErr := s.agentCtrl.ExecuteBatch(r.Context(), req.Actions, req.ReturnScreenshot, shotOpts)
		if batchErr != nil {
			writeJSON(w, http.StatusInternalServerError, batchRes)
			return
		}
		writeJSON(w, http.StatusOK, batchRes)
		return
	}

	// Case 2: Single action
	actionRes, actErr := s.agentCtrl.ExecuteAction(r.Context(), req.AgentAction)
	if actErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   actErr.Error(),
			"result":  actionRes,
		})
		return
	}

	resp := map[string]any{
		"success": true,
		"result":  actionRes,
	}

	if req.ReturnScreenshot {
		bytes, format, shotErr := s.agentCtrl.CaptureScreenshot(r.Context(), shotOpts)
		if shotErr == nil && len(bytes) > 0 {
			wWidth, wHeight := s.agentCtrl.GetGeometry()
			resp["screenshot"] = map[string]any{
				"format":    format,
				"width":     wWidth,
				"height":    wHeight,
				"base64":    base64.StdEncoding.EncodeToString(bytes),
				"timestamp": time.Now().Unix(),
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleDesktopClipboard gets or sets the X11 clipboard
func (s *Server) handleDesktopClipboard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		text, err := s.agentCtrl.GetClipboard(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "clipboard_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"text":   text,
			"length": len(text),
		})

	case http.MethodPost:
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_json", "Failed to parse JSON body")
			return
		}
		if err := s.agentCtrl.SetClipboard(r.Context(), req.Text); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "clipboard_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"bytes_written": len(req.Text),
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDesktopWindows lists visible desktop windows
func (s *Server) handleDesktopWindows(w http.ResponseWriter, r *http.Request) {
	windows, err := s.agentCtrl.ListWindows(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "window_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"windows": windows,
		"count":   len(windows),
	})
}

// handleDesktopWindowFocus brings a window to the foreground
func (s *Server) handleDesktopWindowFocus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IDOrTitle string `json:"id_or_title"`
		ID        string `json:"id"`
		Title     string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	target := firstNonEmpty(req.IDOrTitle, req.ID, req.Title)
	if target == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_target", "id or title is required")
		return
	}
	if err := s.agentCtrl.FocusWindow(r.Context(), target); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "focus_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"focused": target,
	})
}

// handleDesktopLaunch spawns a desktop application in the X11 environment
func (s *Server) handleDesktopLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", "Failed to parse JSON")
		return
	}
	pid, err := s.agentCtrl.LaunchApp(r.Context(), req.Command, req.Args)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "launch_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"pid":     pid,
		"command": req.Command,
	})
}

// handleMCPManifest returns tool definitions formatted for Model Context Protocol
func (s *Server) handleMCPManifest(w http.ResponseWriter, r *http.Request) {
	manifest := desktop.GenerateMCPManifest("https://" + normalizeRequestHost(r))
	writeJSON(w, http.StatusOK, manifest)
}

// handleOpenAPI returns the OpenAPI 3.0 specification for AI Agent tool generation
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, desktop.GenerateOpenAPI())
}
