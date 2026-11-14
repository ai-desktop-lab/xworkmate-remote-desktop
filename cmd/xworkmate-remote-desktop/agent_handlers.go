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

// handleDeviceRegister handles DSH Remote device registration
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	token := extractRequestToken(r, "")
	if token == "" {
		token = "dev-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceToken":  token,
		"refreshToken": token,
		"expiresAt":    time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
	})
}

// handleDevicesList lists this desktop host as an available remote device
func (s *Server) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	wWidth, wHeight := s.agentCtrl.GetGeometry()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": []map[string]any{
			{
				"deviceId":       "desktop-host",
				"name":           fmt.Sprintf("XWorkmate WebRTC Desktop (%dx%d)", wWidth, wHeight),
				"role":           "host",
				"platform":       "linux",
				"online":         true,
				"membershipId":   "desktop-host",
				"harnessVersion": "1.0.0",
				"clientVersion":  "1.0.0",
			},
		},
	})
}

// handleDeviceItem handles device sub-paths (/ice-servers, /presence, /self)
func (s *Server) handleDeviceItem(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
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
			"online": true,
		})
		return
	}
	wWidth, wHeight := s.agentCtrl.GetGeometry()
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId":       "desktop-host",
		"name":           fmt.Sprintf("XWorkmate WebRTC Desktop (%dx%d)", wWidth, wHeight),
		"role":           "host",
		"platform":       "linux",
		"online":         true,
		"membershipId":   "desktop-host",
		"harnessVersion": "1.0.0",
		"clientVersion":  "1.0.0",
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

	opts := desktop.ScreenshotOptions{
		Format: format,
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
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "XWorkmate Remote Desktop & AI Agent Control API",
			"description": "Unified WebRTC remote streaming, human interactive desktop, and AI Agent actuation/perception API.",
			"version":     "1.0.0",
		},
		"paths": map[string]any{
			"/api/v1/desktop/info": map[string]any{
				"get": map[string]any{
					"summary":     "Get display geometry, mouse position, and capabilities",
					"description": "Used by AI agents to perceive the viewport and check supported actions.",
				},
			},
			"/api/v1/desktop/screenshot": map[string]any{
				"get": map[string]any{
					"summary":     "Capture a screenshot of the X11 desktop",
					"description": "Returns raw binary image (image/png) or JSON with base64 encoded image.",
				},
			},
			"/api/v1/desktop/action": map[string]any{
				"post": map[string]any{
					"summary":     "Execute mouse and keyboard actions",
					"description": "Executes single action or batch of actions with optional immediate screenshot return.",
				},
			},
			"/api/v1/desktop/clipboard": map[string]any{
				"get": map[string]any{
					"summary": "Read clipboard text",
				},
				"post": map[string]any{
					"summary": "Write clipboard text",
				},
			},
			"/api/v1/desktop/mcp/manifest": map[string]any{
				"get": map[string]any{
					"summary": "Get Model Context Protocol (MCP) tool manifest",
				},
			},
		},
	}
	writeJSON(w, http.StatusOK, spec)
}
