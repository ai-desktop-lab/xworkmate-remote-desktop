package desktop

// GenerateOpenAPI returns an OpenAPI 3.0.3 document that matches the HTTP API.
// The document itself contains no secrets, live display state, or host-specific
// credentials, so GET /api/v1/openapi.json is intentionally left public for
// client discovery. All described agent/desktop operations require bearerAuth.
func GenerateOpenAPI() map[string]any {
	bearer := []map[string]any{{"bearerAuth": []string{}}}
	coordDesc := "Non-integer values in [0,1] are treated as normalized viewport fractions (0.5 is center). Integer values such as 0, 1, or 1.0 are pixels unless coord_mode or normalized is set."
	actionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":      map[string]any{"type": "string", "description": "Action type or alias of type"},
			"type":        map[string]any{"type": "string", "description": "Alias of action"},
			"x":           map[string]any{"type": "number", "description": "X coordinate. " + coordDesc},
			"y":           map[string]any{"type": "number", "description": "Y coordinate. " + coordDesc},
			"from_x":      map[string]any{"type": "number"},
			"from_y":      map[string]any{"type": "number"},
			"to_x":        map[string]any{"type": "number"},
			"to_y":        map[string]any{"type": "number"},
			"button":      map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}},
			"count":       map[string]any{"type": "integer"},
			"clicks":      map[string]any{"type": "integer", "description": "Alias for count"},
			"text":        map[string]any{"type": "string"},
			"key":         map[string]any{"type": "string"},
			"keys":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Alias for key, joined with +"},
			"dx":          map[string]any{"type": "integer"},
			"dy":          map[string]any{"type": "integer"},
			"direction":   map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
			"amount":      map[string]any{"type": "integer", "description": "Alias for scroll count"},
			"duration_ms": map[string]any{"type": "integer"},
			"normalized":  map[string]any{"type": "boolean"},
			"coord_mode":  map[string]any{"type": "string", "enum": []string{"auto", "normalized", "pixel"}},
			"mode":        map[string]any{"type": "string", "enum": []string{"auto", "type", "paste"}, "description": "Type injection mode"},
		},
	}

	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "XWorkmate Remote Desktop & AI Agent Control API",
			"description": "WebRTC remote streaming and AI agent actuation/perception API. Agent/desktop endpoints require Authorization: Bearer. The human web UI may authenticate WebRTC session routes with the xc_session SSO cookie. Default capture is 1280x720 @ 30fps software H.264 (x264enc); this is not a 1080p60 or VP8 service unless explicitly reconfigured. End-to-end WebRTC latency is not a published SLA.",
			"version":     "1.0.0",
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "token",
					"description":  "Static XRD token or accounts session token in the Authorization header. Required for all agent and desktop APIs. Cookie-only requests are rejected on those paths.",
				},
				"sessionCookie": map[string]any{
					"type":        "apiKey",
					"in":          "cookie",
					"name":        "xc_session",
					"description": "SSO session cookie for the human web UI (WebRTC viewing/takeover, auth status, logout). Not sufficient for agent/desktop APIs.",
				},
			},
			"schemas": map[string]any{
				"AgentAction": actionSchema,
			},
		},
		"paths": map[string]any{
			"/healthz": map[string]any{
				"get": map[string]any{
					"summary":   "Liveness probe",
					"security":  []any{},
					"responses": map[string]any{"200": map[string]any{"description": "ok"}},
				},
			},
			"/api/v1/desktop/info": map[string]any{
				"get": map[string]any{
					"summary":   "Display geometry, mouse position, active window, and capabilities",
					"security":  bearer,
					"responses": map[string]any{"200": map[string]any{"description": "Display info"}, "401": map[string]any{"description": "Missing or invalid Bearer token"}},
				},
			},
			"/api/v1/desktop/screenshot": map[string]any{
				"get": map[string]any{
					"summary":  "Capture a screenshot (raw PNG when raw=true, otherwise JSON with base64)",
					"security": bearer,
					"parameters": []map[string]any{
						{"name": "raw", "in": "query", "schema": map[string]any{"type": "boolean"}},
						{"name": "format", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"png", "jpeg"}}},
						{"name": "scale", "in": "query", "schema": map[string]any{"type": "number"}},
					},
					"responses": map[string]any{"200": map[string]any{"description": "Screenshot"}, "401": map[string]any{"description": "Unauthorized"}},
				},
				"post": map[string]any{
					"summary":     "Capture a screenshot with a JSON body (format, base64, scale)",
					"security":    bearer,
					"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"format": map[string]any{"type": "string"}, "base64": map[string]any{"type": "boolean"}, "scale": map[string]any{"type": "number"}, "raw": map[string]any{"type": "boolean"}}}}}},
					"responses":   map[string]any{"200": map[string]any{"description": "Screenshot"}, "401": map[string]any{"description": "Unauthorized"}},
				},
			},
			"/api/v1/desktop/action": map[string]any{
				"post": map[string]any{
					"summary":     "Execute a single action or a batch (actions[]) with optional return_screenshot",
					"security":    bearer,
					"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"allOf": []any{map[string]any{"$ref": "#/components/schemas/AgentAction"}, map[string]any{"type": "object", "properties": map[string]any{"actions": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/AgentAction"}}, "return_screenshot": map[string]any{"type": "boolean"}, "format": map[string]any{"type": "string"}}}}}}}},
					"responses":   map[string]any{"200": map[string]any{"description": "Action or batch result"}, "401": map[string]any{"description": "Unauthorized"}},
				},
			},
			"/api/v1/desktop/clipboard": map[string]any{
				"get":  map[string]any{"summary": "Read clipboard text", "security": bearer, "responses": map[string]any{"200": map[string]any{"description": "Clipboard text"}, "401": map[string]any{"description": "Unauthorized"}}},
				"post": map[string]any{"summary": "Write clipboard text", "security": bearer, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}}}, "responses": map[string]any{"200": map[string]any{"description": "Write result"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/desktop/windows": map[string]any{
				"get": map[string]any{"summary": "List visible windows", "security": bearer, "responses": map[string]any{"200": map[string]any{"description": "Window list"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/desktop/windows/focus": map[string]any{
				"post": map[string]any{"summary": "Focus a window by id or title", "security": bearer, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "id_or_title": map[string]any{"type": "string"}}}}}}, "responses": map[string]any{"200": map[string]any{"description": "Focused"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/desktop/launch": map[string]any{
				"post": map[string]any{"summary": "Launch a desktop application", "security": bearer, "requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "required": []string{"command"}}}}}, "responses": map[string]any{"200": map[string]any{"description": "Launched"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/desktop/mcp/manifest": map[string]any{
				"get": map[string]any{"summary": "HTTP JSON tool catalog inspired by MCP (not a full MCP transport)", "security": bearer, "responses": map[string]any{"200": map[string]any{"description": "Manifest"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/openapi.json": map[string]any{
				"get": map[string]any{"summary": "OpenAPI 3.0 document (public; contains no secrets or live state)", "security": []any{}, "responses": map[string]any{"200": map[string]any{"description": "OpenAPI document"}}},
			},
			"/api/v1/session": map[string]any{
				"post": map[string]any{"summary": "Create a WebRTC desktop session (human UI; Bearer or xc_session cookie)", "security": []map[string]any{{"bearerAuth": []string{}}, {"sessionCookie": []string{}}}, "responses": map[string]any{"200": map[string]any{"description": "SDP answer"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/session/{id}": map[string]any{
				"delete": map[string]any{"summary": "End a WebRTC session", "security": []map[string]any{{"bearerAuth": []string{}}, {"sessionCookie": []string{}}}, "parameters": []map[string]any{{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}, "responses": map[string]any{"204": map[string]any{"description": "Ended"}, "401": map[string]any{"description": "Unauthorized"}}},
			},
			"/api/v1/auth/status": map[string]any{
				"get": map[string]any{"summary": "Auth status for the human web UI (cookie or Bearer)", "responses": map[string]any{"200": map[string]any{"description": "Status"}}},
			},
			"/api/v1/auth/login": map[string]any{
				"post": map[string]any{"summary": "SSO or static-token login; sets xc_session for the web UI", "security": []any{}, "responses": map[string]any{"200": map[string]any{"description": "Login result"}}},
			},
			"/api/v1/auth/logout": map[string]any{
				"post": map[string]any{"summary": "Logout and clear the SSO cookie", "responses": map[string]any{"200": map[string]any{"description": "Logged out"}}},
			},
		},
	}
}
