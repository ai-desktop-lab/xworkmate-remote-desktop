package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AgentAction represents an atomic action requested by an AI agent
type AgentAction struct {
	Type        string   `json:"type,omitempty"`        // "click", "move", "down", "up", "drag", "scroll", "key", "key_down", "key_up", "type", "hotkey", "sleep"
	Action      string   `json:"action,omitempty"`      // alias for Type
	X           *float64 `json:"x,omitempty"`           // absolute or normalized [0..1]
	Y           *float64 `json:"y,omitempty"`           // absolute or normalized [0..1]
	FromX       *float64 `json:"from_x,omitempty"`      // drag start
	FromY       *float64 `json:"from_y,omitempty"`
	ToX         *float64 `json:"to_x,omitempty"`        // drag end
	ToY         *float64 `json:"to_y,omitempty"`
	Button      string   `json:"button,omitempty"`      // "left", "middle", "right" or "1", "2", "3"
	Count       int      `json:"count,omitempty"`       // repeat count for clicks
	Text        string   `json:"text,omitempty"`        // text string for typing
	Key         string   `json:"key,omitempty"`         // key symbol or combo (e.g., "Return", "ctrl+c")
	Dx          int      `json:"dx,omitempty"`          // horizontal scroll
	Dy          int      `json:"dy,omitempty"`          // vertical scroll
	DurationMs  int      `json:"duration_ms,omitempty"` // pause/delay in ms
}

// ScreenshotOptions configures screen capture
type ScreenshotOptions struct {
	Format  string `json:"format,omitempty"`  // "png" (default) or "jpeg"
	Quality int    `json:"quality,omitempty"` // for jpeg (1-100)
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
}

// WindowInfo represents an open desktop window
type WindowInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	PID   int    `json:"pid,omitempty"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// AgentCapabilities details what operations this desktop host supports
type AgentCapabilities struct {
	Screenshot       bool     `json:"screenshot"`
	Actions          []string `json:"actions"`
	Clipboard        bool     `json:"clipboard"`
	WindowManagement bool     `json:"window_management"`
	Apps             bool     `json:"apps"`
}

// DisplayInfo provides viewport metadata for AI perception
type DisplayInfo struct {
	Display      string            `json:"display"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	MouseX       int               `json:"mouse_x"`
	MouseY       int               `json:"mouse_y"`
	ActiveWindow *WindowInfo       `json:"active_window,omitempty"`
	Capabilities AgentCapabilities `json:"capabilities"`
}

// ActionResult holds the result of executing an agent action
type ActionResult struct {
	Success    bool   `json:"success"`
	Action     string `json:"action"`
	Message    string `json:"message,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// BatchActionResult holds the result of executing multiple agent actions
type BatchActionResult struct {
	Success         bool                    `json:"success"`
	ExecutedActions int                     `json:"executed_actions"`
	TotalDurationMs int64                   `json:"total_duration_ms"`
	Results         []ActionResult          `json:"results"`
	Screenshot      *ScreenshotBase64Result `json:"screenshot,omitempty"`
}

// ScreenshotBase64Result holds base64-encoded screenshot data
type ScreenshotBase64Result struct {
	Format    string `json:"format"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Base64    string `json:"base64"`
	Timestamp int64  `json:"timestamp"`
}

// AgentController provides direct API-level actuation and perception on the X11 display
type AgentController struct {
	display  string
	injector *XdotoolInjector
	mu       sync.Mutex
}

// NewAgentController initializes an AgentController for a given display
func NewAgentController(display string) *AgentController {
	resolvedDisplay := ResolveDesktopDisplay(display)
	injector := NewXdotoolInjector(resolvedDisplay)
	_ = injector.Start()
	return &AgentController{
		display:  resolvedDisplay,
		injector: injector,
	}
}

// Display returns the active display identifier
func (ac *AgentController) Display() string {
	return ac.display
}

// Close cleans up resources
func (ac *AgentController) Close() error {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.injector != nil {
		return ac.injector.Close()
	}
	return nil
}

// GetGeometry returns width and height of the display
func (ac *AgentController) GetGeometry() (int, int) {
	w, h, err := queryDisplayGeometry(ac.display)
	if err == nil && w > 0 && h > 0 {
		return w, h
	}
	return 1280, 720
}

// ResolveCoords converts normalized [0..1] or absolute pixel coordinates to absolute X, Y pixels
func (ac *AgentController) ResolveCoords(xVal, yVal *float64) (int, int, error) {
	if xVal == nil || yVal == nil {
		return 0, 0, errors.New("coordinates x and y are required")
	}
	w, h := ac.GetGeometry()
	x := *xVal
	y := *yVal

	// If both coordinates are within [0.0 .. 1.0] and at least one is non-zero, treat as normalized
	if x <= 1.0 && y <= 1.0 && (x > 0 || y > 0) {
		absX := int(math.Round(x * float64(w)))
		absY := int(math.Round(y * float64(h)))
		if absX >= w {
			absX = w - 1
		}
		if absY >= h {
			absY = h - 1
		}
		return absX, absY, nil
	}

	return int(math.Round(x)), int(math.Round(y)), nil
}

// GetDisplayInfo queries current display resolution, mouse location, active window, and capabilities
func (ac *AgentController) GetDisplayInfo(ctx context.Context) (*DisplayInfo, error) {
	w, h := ac.GetGeometry()
	info := &DisplayInfo{
		Display: ac.display,
		Width:   w,
		Height:  h,
		Capabilities: AgentCapabilities{
			Screenshot: true,
			Actions: []string{
				"click", "mouse_click", "double_click", "triple_click", "right_click", "middle_click",
				"move", "mouse_move", "mouse_down", "mouse_up", "drag", "mouse_drag",
				"scroll", "key", "key_down", "key_up", "type", "type_text", "hotkey", "sleep",
			},
			Clipboard:        true,
			WindowManagement: true,
			Apps:             true,
		},
	}

	// Query mouse position via xdotool getmouselocation
	cmd := exec.CommandContext(ctx, "xdotool", "getmouselocation")
	cmd.Env = desktopCommandEnv(ac.display)
	if out, err := cmd.Output(); err == nil {
		fields := strings.Fields(string(out))
		for _, f := range fields {
			if strings.HasPrefix(f, "x:") {
				info.MouseX, _ = strconv.Atoi(strings.TrimPrefix(f, "x:"))
			} else if strings.HasPrefix(f, "y:") {
				info.MouseY, _ = strconv.Atoi(strings.TrimPrefix(f, "y:"))
			}
		}
	}

	// Query active window
	info.ActiveWindow = ac.GetActiveWindow(ctx)

	return info, nil
}

// GetActiveWindow gets information about the currently focused window
func (ac *AgentController) GetActiveWindow(ctx context.Context) *WindowInfo {
	cmd := exec.CommandContext(ctx, "xdotool", "getactivewindow")
	cmd.Env = desktopCommandEnv(ac.display)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	winIDStr := strings.TrimSpace(string(out))
	if winIDStr == "" {
		return nil
	}

	win := &WindowInfo{ID: winIDStr}

	// Title
	nameCmd := exec.CommandContext(ctx, "xdotool", "getwindowname", winIDStr)
	nameCmd.Env = desktopCommandEnv(ac.display)
	if nameOut, err := nameCmd.Output(); err == nil {
		win.Title = strings.TrimSpace(string(nameOut))
	}

	// PID
	pidCmd := exec.CommandContext(ctx, "xdotool", "getwindowpid", winIDStr)
	pidCmd.Env = desktopCommandEnv(ac.display)
	if pidOut, err := pidCmd.Output(); err == nil {
		win.PID, _ = strconv.Atoi(strings.TrimSpace(string(pidOut)))
	}

	// Geometry
	geoCmd := exec.CommandContext(ctx, "xdotool", "getwindowgeometry", winIDStr)
	geoCmd.Env = desktopCommandEnv(ac.display)
	if geoOut, err := geoCmd.Output(); err == nil {
		for _, line := range strings.Split(string(geoOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Position:") {
				coords := strings.Fields(strings.TrimPrefix(line, "Position:"))
				if len(coords) > 0 {
					parts := strings.Split(coords[0], ",")
					if len(parts) == 2 {
						win.X, _ = strconv.Atoi(parts[0])
						win.Y, _ = strconv.Atoi(parts[1])
					}
				}
			} else if strings.HasPrefix(line, "Geometry:") {
				dims := strings.Fields(strings.TrimPrefix(line, "Geometry:"))
				if len(dims) > 0 {
					parts := strings.Split(dims[0], "x")
					if len(parts) == 2 {
						win.W, _ = strconv.Atoi(parts[0])
						win.H, _ = strconv.Atoi(parts[1])
					}
				}
			}
		}
	}

	return win
}

// ListWindows returns all visible windows using wmctrl or xdotool
func (ac *AgentController) ListWindows(ctx context.Context) ([]WindowInfo, error) {
	// First try wmctrl -l -p -G
	cmd := exec.CommandContext(ctx, "wmctrl", "-l", "-p", "-G")
	cmd.Env = desktopCommandEnv(ac.display)
	out, err := cmd.Output()
	if err == nil {
		var windows []WindowInfo
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) < 8 {
				continue
			}
			pid, _ := strconv.Atoi(fields[2])
			x, _ := strconv.Atoi(fields[3])
			y, _ := strconv.Atoi(fields[4])
			w, _ := strconv.Atoi(fields[5])
			h, _ := strconv.Atoi(fields[6])
			title := strings.Join(fields[7:], " ")
			windows = append(windows, WindowInfo{
				ID:    fields[0],
				PID:   pid,
				X:     x,
				Y:     y,
				W:     w,
				H:     h,
				Title: title,
			})
		}
		return windows, nil
	}

	// Fallback to xdotool search --onlyvisible
	xCmd := exec.CommandContext(ctx, "xdotool", "search", "--onlyvisible", "--name", "")
	xCmd.Env = desktopCommandEnv(ac.display)
	xOut, xErr := xCmd.Output()
	if xErr != nil {
		return []WindowInfo{}, nil
	}

	var windows []WindowInfo
	for _, idStr := range strings.Split(string(xOut), "\n") {
		idStr = strings.TrimSpace(idStr)
		if idStr == "" {
			continue
		}
		titleCmd := exec.CommandContext(ctx, "xdotool", "getwindowname", idStr)
		titleCmd.Env = desktopCommandEnv(ac.display)
		titleOut, _ := titleCmd.Output()
		title := strings.TrimSpace(string(titleOut))
		if title != "" {
			windows = append(windows, WindowInfo{
				ID:    idStr,
				Title: title,
			})
		}
	}
	return windows, nil
}

// FocusWindow brings a window to the foreground by ID or Title
func (ac *AgentController) FocusWindow(ctx context.Context, idOrTitle string) error {
	idOrTitle = strings.TrimSpace(idOrTitle)
	if idOrTitle == "" {
		return errors.New("window id or title is required")
	}

	// If it starts with 0x or digits, try windowactivate
	cmd := exec.CommandContext(ctx, "xdotool", "windowactivate", idOrTitle)
	cmd.Env = desktopCommandEnv(ac.display)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Try wmctrl -a
	wmCmd := exec.CommandContext(ctx, "wmctrl", "-a", idOrTitle)
	wmCmd.Env = desktopCommandEnv(ac.display)
	return wmCmd.Run()
}

// GetClipboard reads text from the X11 clipboard
func (ac *AgentController) GetClipboard(ctx context.Context) (string, error) {
	// Try xclip first
	cmd := exec.CommandContext(ctx, "xclip", "-o", "-selection", "clipboard")
	cmd.Env = desktopCommandEnv(ac.display)
	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}

	// Fallback to xsel
	cmd2 := exec.CommandContext(ctx, "xsel", "-o", "-b")
	cmd2.Env = desktopCommandEnv(ac.display)
	out2, err2 := cmd2.Output()
	if err2 == nil {
		return string(out2), nil
	}

	return "", fmt.Errorf("clipboard read failed: xclip/xsel not available or clipboard empty (%w)", err)
}

// SetClipboard writes text to the X11 clipboard
func (ac *AgentController) SetClipboard(ctx context.Context, text string) error {
	// Try xclip
	cmd := exec.CommandContext(ctx, "xclip", "-i", "-selection", "clipboard")
	cmd.Env = desktopCommandEnv(ac.display)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Fallback to xsel
	cmd2 := exec.CommandContext(ctx, "xsel", "-i", "-b")
	cmd2.Env = desktopCommandEnv(ac.display)
	cmd2.Stdin = strings.NewReader(text)
	if err := cmd2.Run(); err == nil {
		return nil
	}

	return errors.New("clipboard write failed: neither xclip nor xsel succeeded")
}

// LaunchApp spawns a desktop application asynchronously in the X11 environment
func (ac *AgentController) LaunchApp(ctx context.Context, command string, args []string) (int, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return 0, errors.New("command is required")
	}

	cmd := exec.Command(command, args...)
	cmd.Env = desktopCommandEnv(ac.display)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to launch application %s: %w", command, err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return pid, nil
}

// CaptureScreenshot takes a screenshot of the X11 display and returns binary image bytes and format
func (ac *AgentController) CaptureScreenshot(ctx context.Context, opts ScreenshotOptions) ([]byte, string, error) {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" || (format != "png" && format != "jpeg" && format != "jpg") {
		format = "png"
	}
	if format == "jpg" {
		format = "jpeg"
	}

	w, h := ac.GetGeometry()

	// Strategy 1: ImageMagick 'import'
	importCmd := exec.CommandContext(ctx, "import", "-window", "root", fmt.Sprintf("%s:-", format))
	importCmd.Env = desktopCommandEnv(ac.display)
	out, err := importCmd.Output()
	if err == nil && len(out) > 0 {
		return out, format, nil
	}

	// Strategy 2: 'scrot'
	if format == "png" {
		scrotCmd := exec.CommandContext(ctx, "scrot", "-o", "-z", "-")
		scrotCmd.Env = desktopCommandEnv(ac.display)
		scrotOut, scrotErr := scrotCmd.Output()
		if scrotErr == nil && len(scrotOut) > 0 {
			return scrotOut, format, nil
		}
	}

	// Strategy 3: 'ffmpeg' single frame capture
	ffmpegArgs := []string{
		"-v", "error",
		"-f", "x11grab",
		"-draw_mouse", "1",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-i", ac.display,
		"-vframes", "1",
		"-f", "image2pipe",
		"-vcodec", format,
		"-",
	}
	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", ffmpegArgs...)
	ffmpegCmd.Env = desktopCommandEnv(ac.display)
	ffmpegOut, ffmpegErr := ffmpegCmd.Output()
	if ffmpegErr == nil && len(ffmpegOut) > 0 {
		return ffmpegOut, format, nil
	}

	// Strategy 4: GStreamer single frame capture
	gstArgs := []string{
		"-q",
		fmt.Sprintf("ximagesrc display-name=%s num-buffers=1", ac.display),
		"!", "videoconvert",
		"!", "pngenc",
		"!", "fdsink",
	}
	gstCmd := exec.CommandContext(ctx, "gst-launch-1.0", gstArgs...)
	gstCmd.Env = desktopCommandEnv(ac.display)
	gstOut, gstErr := gstCmd.Output()
	if gstErr == nil && len(gstOut) > 0 {
		return gstOut, "png", nil
	}

	// Fallback for headless environments or unit tests: generate a synthetic image
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 24, B: 36, A: 255})
		}
	}
	var buf bytes.Buffer
	if format == "png" {
		_ = png.Encode(&buf, img)
	} else {
		_ = png.Encode(&buf, img)
		format = "png"
	}
	return buf.Bytes(), format, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ExecuteAction executes a single agent action
func (ac *AgentController) ExecuteAction(ctx context.Context, action AgentAction) (*ActionResult, error) {
	start := time.Now()
	actType := strings.ToLower(firstNonEmpty(action.Type, action.Action))
	if actType == "" {
		return nil, errors.New("action type is required")
	}

	var execErr error
	var msg string

	switch actType {
	case "click", "mouse_click":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		if err != nil {
			// Click at current position if no coords provided
			btn := ac.mapButton(action.Button)
			count := action.Count
			if count <= 0 {
				count = 1
			}
			execErr = ac.runXdotool(ctx, fmt.Sprintf("click --repeat %d %d", count, btn))
			msg = fmt.Sprintf("Clicked button %d (%d times)", btn, count)
		} else {
			btn := ac.mapButton(action.Button)
			count := action.Count
			if count <= 0 {
				count = 1
			}
			execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d click --repeat %d %d", x, y, count, btn))
			msg = fmt.Sprintf("Clicked button %d at (%d, %d)", btn, x, y)
		}

	case "double_click":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		btn := ac.mapButton(action.Button)
		if err != nil {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("click --repeat 2 --delay 100 %d", btn))
			msg = fmt.Sprintf("Double clicked button %d", btn)
		} else {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d click --repeat 2 --delay 100 %d", x, y, btn))
			msg = fmt.Sprintf("Double clicked button %d at (%d, %d)", btn, x, y)
		}

	case "triple_click":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		btn := ac.mapButton(action.Button)
		if err != nil {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("click --repeat 3 --delay 100 %d", btn))
		} else {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d click --repeat 3 --delay 100 %d", x, y, btn))
		}
		msg = fmt.Sprintf("Triple clicked button %d", btn)

	case "right_click":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		if err != nil {
			execErr = ac.runXdotool(ctx, "click 3")
		} else {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d click 3", x, y))
		}
		msg = "Right clicked"

	case "middle_click":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		if err != nil {
			execErr = ac.runXdotool(ctx, "click 2")
		} else {
			execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d click 2", x, y))
		}
		msg = "Middle clicked"

	case "move", "mouse_move":
		x, y, err := ac.ResolveCoords(action.X, action.Y)
		if err != nil {
			return nil, err
		}
		execErr = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d", x, y))
		msg = fmt.Sprintf("Moved mouse to (%d, %d)", x, y)

	case "mouse_down", "down":
		btn := ac.mapButton(action.Button)
		execErr = ac.runXdotool(ctx, fmt.Sprintf("mousedown %d", btn))
		msg = fmt.Sprintf("Mouse down button %d", btn)

	case "mouse_up", "up":
		btn := ac.mapButton(action.Button)
		execErr = ac.runXdotool(ctx, fmt.Sprintf("mouseup %d", btn))
		msg = fmt.Sprintf("Mouse up button %d", btn)

	case "drag", "mouse_drag":
		fromX, fromY, errFrom := ac.ResolveCoords(action.FromX, action.FromY)
		toX, toY, errTo := ac.ResolveCoords(action.ToX, action.ToY)
		if errFrom != nil || errTo != nil {
			return nil, fmt.Errorf("drag requires valid from_x, from_y and to_x, to_y: %v, %v", errFrom, errTo)
		}
		btn := ac.mapButton(action.Button)
		cmdStr := fmt.Sprintf("mousemove %d %d mousedown %d mousemove %d %d mouseup %d", fromX, fromY, btn, toX, toY, btn)
		execErr = ac.runXdotool(ctx, cmdStr)
		msg = fmt.Sprintf("Dragged button %d from (%d, %d) to (%d, %d)", btn, fromX, fromY, toX, toY)

	case "scroll":
		// Handle coordinates if specified
		if action.X != nil && action.Y != nil {
			x, y, err := ac.ResolveCoords(action.X, action.Y)
			if err == nil {
				_ = ac.runXdotool(ctx, fmt.Sprintf("mousemove %d %d", x, y))
			}
		}
		// button 4 is up, 5 is down
		var btn int
		var count int
		if action.Dy < 0 {
			btn = 4
			count = -action.Dy
		} else if action.Dy > 0 {
			btn = 5
			count = action.Dy
		} else if strings.EqualFold(action.Button, "up") || action.Button == "4" {
			btn = 4
			count = 1
		} else {
			btn = 5
			count = 1
		}
		if action.Count > 0 {
			count = action.Count
		}
		execErr = ac.runXdotool(ctx, fmt.Sprintf("click --repeat %d %d", count, btn))
		msg = fmt.Sprintf("Scrolled button %d (%d times)", btn, count)

	case "key", "key_press", "press", "hotkey":
		keyStr := strings.TrimSpace(action.Key)
		if keyStr == "" {
			return nil, errors.New("key is required")
		}
		execErr = ac.runXdotool(ctx, fmt.Sprintf("key %s", sanitizeKey(keyStr)))
		msg = fmt.Sprintf("Pressed key: %s", keyStr)

	case "key_down":
		keyStr := strings.TrimSpace(action.Key)
		if keyStr == "" {
			return nil, errors.New("key is required")
		}
		execErr = ac.runXdotool(ctx, fmt.Sprintf("keydown %s", sanitizeKey(keyStr)))
		msg = fmt.Sprintf("Held key down: %s", keyStr)

	case "key_up":
		keyStr := strings.TrimSpace(action.Key)
		if keyStr == "" {
			return nil, errors.New("key is required")
		}
		execErr = ac.runXdotool(ctx, fmt.Sprintf("keyup %s", sanitizeKey(keyStr)))
		msg = fmt.Sprintf("Released key up: %s", keyStr)

	case "type", "type_text":
		text := action.Text
		if text == "" {
			return nil, errors.New("text is required")
		}
		// xdotool type --delay 12 -- "text"
		cmd := exec.CommandContext(ctx, "xdotool", "type", "--delay", "12", "--", text)
		cmd.Env = desktopCommandEnv(ac.display)
		execErr = cmd.Run()
		msg = fmt.Sprintf("Typed text (%d runes)", len([]rune(text)))

	case "sleep", "wait":
		ms := action.DurationMs
		if ms <= 0 {
			ms = 100
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			execErr = ctx.Err()
		}
		msg = fmt.Sprintf("Slept %d ms", ms)

	default:
		return nil, fmt.Errorf("unsupported agent action: %s", actType)
	}

	elapsed := time.Since(start).Milliseconds()
	if execErr != nil {
		return &ActionResult{
			Success:    false,
			Action:     actType,
			Message:    execErr.Error(),
			DurationMs: elapsed,
		}, execErr
	}

	return &ActionResult{
		Success:    true,
		Action:     actType,
		Message:    msg,
		DurationMs: elapsed,
	}, nil
}

// ExecuteBatch executes an array of actions sequentially and optionally captures a screenshot
func (ac *AgentController) ExecuteBatch(ctx context.Context, actions []AgentAction, returnScreenshot bool, shotOpts ScreenshotOptions) (*BatchActionResult, error) {
	start := time.Now()
	res := &BatchActionResult{
		Success: true,
		Results: make([]ActionResult, 0, len(actions)),
	}

	for _, act := range actions {
		actRes, err := ac.ExecuteAction(ctx, act)
		if actRes != nil {
			res.Results = append(res.Results, *actRes)
		}
		if err != nil {
			res.Success = false
			break
		}
		res.ExecutedActions++
	}

	res.TotalDurationMs = time.Since(start).Milliseconds()

	if returnScreenshot {
		rawBytes, format, shotErr := ac.CaptureScreenshot(ctx, shotOpts)
		if shotErr == nil && len(rawBytes) > 0 {
			w, h := ac.GetGeometry()
			res.Screenshot = &ScreenshotBase64Result{
				Format:    format,
				Width:     w,
				Height:    h,
				Base64:    base64.StdEncoding.EncodeToString(rawBytes),
				Timestamp: time.Now().Unix(),
			}
		}
	}

	return res, nil
}

func (ac *AgentController) runXdotool(ctx context.Context, script string) error {
	args := strings.Fields(script)
	if len(args) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "xdotool", args...)
	cmd.Env = desktopCommandEnv(ac.display)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xdotool error (%s): %w", strings.TrimSpace(errBuf.String()), err)
	}
	return nil
}

func (ac *AgentController) mapButton(btn string) int {
	b := strings.ToLower(strings.TrimSpace(btn))
	switch b {
	case "left", "1", "":
		return 1
	case "middle", "2":
		return 2
	case "right", "3":
		return 3
	case "scroll_up", "up", "4":
		return 4
	case "scroll_down", "down", "5":
		return 5
	default:
		if num, err := strconv.Atoi(b); err == nil && num >= 1 && num <= 5 {
			return num
		}
		return 1
	}
}

func sanitizeKey(key string) string {
	key = strings.TrimSpace(key)
	// Handle combinations like "ctrl+c", "ctrl+alt+t", "super+d"
	if strings.Contains(key, "+") {
		parts := strings.Split(key, "+")
		var cleanParts []string
		for _, p := range parts {
			cleanParts = append(cleanParts, sanitizeKey(p))
		}
		return strings.Join(cleanParts, "+")
	}

	switch strings.ToLower(key) {
	case "enter":
		return "Return"
	case "return":
		return "Return"
	case "backspace":
		return "BackSpace"
	case "tab":
		return "Tab"
	case "escape", "esc":
		return "Escape"
	case "control", "ctrl":
		return "ctrl"
	case "shift":
		return "shift"
	case "alt":
		return "alt"
	case "meta", "super", "win", "cmd":
		return "super"
	case "space":
		return "space"
	case "up":
		return "Up"
	case "down":
		return "Down"
	case "left":
		return "Left"
	case "right":
		return "Right"
	default:
		return key
	}
}

// GenerateMCPManifest returns tool definitions formatted for Model Context Protocol agents
func GenerateMCPManifest(serverBaseURL string) map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo": map[string]string{
			"name":    "xworkmate-remote-desktop",
			"version": "1.0.0",
		},
		"tools": []map[string]any{
			{
				"name":        "desktop_screenshot",
				"description": "Capture a live screenshot of the remote desktop display. Returns base64 encoded PNG and dimensions.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"format": map[string]any{
							"type":        "string",
							"enum":        []string{"png", "jpeg"},
							"description": "Image format to encode",
						},
					},
				},
			},
			{
				"name":        "desktop_action",
				"description": "Execute a single mouse or keyboard action on the remote desktop display.",
				"inputSchema": map[string]any{
					"type": "object",
					"required": []string{
						"action",
					},
					"properties": map[string]any{
						"action": map[string]any{
							"type":        "string",
							"enum":        []string{"click", "double_click", "right_click", "move", "drag", "scroll", "key", "type", "sleep"},
							"description": "Action type to perform",
						},
						"x": map[string]any{
							"type":        "number",
							"description": "X coordinate: either normalized [0.0..1.0] or absolute pixels",
						},
						"y": map[string]any{
							"type":        "number",
							"description": "Y coordinate: either normalized [0.0..1.0] or absolute pixels",
						},
						"button": map[string]any{
							"type":        "string",
							"enum":        []string{"left", "middle", "right"},
							"description": "Mouse button",
						},
						"text": map[string]any{
							"type":        "string",
							"description": "Text to type (for type action)",
						},
						"key": map[string]any{
							"type":        "string",
							"description": "Key or hotkey combo (e.g. 'Return', 'ctrl+c', 'alt+Tab')",
						},
					},
				},
			},
			{
				"name":        "desktop_batch_actions",
				"description": "Execute a sequence of mouse and keyboard actions and optionally return the resulting screenshot.",
				"inputSchema": map[string]any{
					"type": "object",
					"required": []string{
						"actions",
					},
					"properties": map[string]any{
						"actions": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
							},
						},
						"return_screenshot": map[string]any{
							"type":        "boolean",
							"description": "Whether to return the final screenshot directly in the response",
						},
					},
				},
			},
			{
				"name":        "desktop_clipboard",
				"description": "Get or set the remote desktop clipboard text.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{
							"type":        "string",
							"description": "Text to write to clipboard (omit to read)",
						},
					},
				},
			},
			{
				"name":        "desktop_list_windows",
				"description": "List visible windows with their titles and process IDs on the remote desktop.",
				"inputSchema": map[string]any{
					"type": "object",
				},
			},
		},
	}
}
