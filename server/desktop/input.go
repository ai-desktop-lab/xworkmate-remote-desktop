package desktop

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// InputEvent represents a client mouse or keyboard action
type InputEvent struct {
	Type   string  `json:"type"`             // "mouse_move", "mouse_down", "mouse_up", "key_down", "key_up", "scroll"
	X      float64 `json:"x,omitempty"`      // normalized x coordinate (0.0 to 1.0)
	Y      float64 `json:"y,omitempty"`      // normalized y coordinate (0.0 to 1.0)
	Button int     `json:"button,omitempty"` // mouse button: 1=left, 2=middle, 3=right, 4=scroll_up, 5=scroll_down
	Key    string  `json:"key,omitempty"`    // key symbol or keycode
}

// XdotoolInjector injects inputs by writing to a persistent xdotool process stdin
type XdotoolInjector struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	mu        sync.Mutex
	display   string
	width     int
	height    int
	lastX     int
	lastY     int
	isStarted bool

	moveMu      sync.Mutex
	pendingMove *InputEvent
	stopChan    chan struct{}
}

func NewXdotoolInjector(display string) *XdotoolInjector {
	if display == "" {
		display = ":0.0"
	}
	return &XdotoolInjector{
		display: display,
		width:   1280, // Default fallbacks
		height:  720,
	}
}

// Start launches xdotool on Linux or initializes native input dispatch on macOS/Windows and queries screen resolution
func (xi *XdotoolInjector) Start() error {
	xi.mu.Lock()
	defer xi.mu.Unlock()

	if xi.isStarted {
		return nil
	}

	// 1. Resolve screen resolution
	w, h, err := xi.queryDisplayGeometry()
	if err == nil {
		xi.width = w
		xi.height = h
		log.Printf("Detected remote display geometry: %dx%d on %s (%s)", w, h, xi.display, runtime.GOOS)
	} else {
		log.Printf("Warning: Failed to query display geometry: %v. Using default: %dx%d", err, xi.width, xi.height)
	}

	// 2. On non-Linux (macOS / Windows), native input commands are invoked directly without xdotool
	if runtime.GOOS != "linux" {
		xi.isStarted = true
		if xi.stopChan == nil {
			xi.stopChan = make(chan struct{})
			go xi.mouseMoveWorker()
		}
		return nil
	}

	// 3. On Linux, launch persistent xdotool process
	cmd := exec.Command("xdotool", "-")
	cmd.Env = desktopCommandEnv(xi.display)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to open xdotool stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("failed to start xdotool process: %w", err)
	}

	xi.cmd = cmd
	xi.stdin = stdin
	xi.isStarted = true

	if xi.stopChan == nil {
		xi.stopChan = make(chan struct{})
		go xi.mouseMoveWorker()
	}

	return nil
}

// Inject sends a command to the persistent xdotool process on Linux or native OS handler on macOS/Windows
func (xi *XdotoolInjector) Inject(event InputEvent) error {
	xi.mu.Lock()
	if !xi.isStarted || (runtime.GOOS == "linux" && xi.stdin == nil) {
		xi.mu.Unlock()
		return fmt.Errorf("injector is not running")
	}

	if event.Type == "mouse_move" {
		xi.moveMu.Lock()
		xi.pendingMove = &event
		xi.moveMu.Unlock()
		xi.mu.Unlock()
		return nil
	}

	if runtime.GOOS != "linux" {
		w, h, lx, ly := xi.width, xi.height, xi.lastX, xi.lastY
		if event.X > 0 || event.Y > 0 {
			xi.lastX = int(event.X * float64(w))
			xi.lastY = int(event.Y * float64(h))
		}
		xi.mu.Unlock()
		return injectNativeInputOS(event, w, h, lx, ly)
	}

	var cmdStr string

	switch event.Type {
	case "mouse_down":
		btn := xi.mapButton(event.Button)
		cmdStr = fmt.Sprintf("mousedown %d\n", btn)

	case "mouse_up":
		btn := xi.mapButton(event.Button)
		cmdStr = fmt.Sprintf("mouseup %d\n", btn)

	case "key_down":
		key := xi.sanitizeKey(event.Key)
		cmdStr = fmt.Sprintf("keydown %s\n", key)

	case "key_up":
		key := xi.sanitizeKey(event.Key)
		cmdStr = fmt.Sprintf("keyup %s\n", key)

	case "scroll":
		// xdotool maps scroll up to button 4 and scroll down to button 5
		if event.Button == 4 || event.Button == 5 {
			cmdStr = fmt.Sprintf("click %d\n", event.Button)
		}

	default:
		xi.mu.Unlock()
		return fmt.Errorf("unsupported input type: %s", event.Type)
	}

	if cmdStr != "" {
		_, err := xi.stdin.Write([]byte(cmdStr))
		if err != nil {
			// Try to restart if pipe is broken
			log.Printf("xdotool write error: %v. Attempting to restart injector.", err)
			xi.isStarted = false
			if xi.stdin != nil {
				_ = xi.stdin.Close()
			}
			xi.stdin = nil
			xi.cmd = nil
			xi.mu.Unlock()
			if restartErr := xi.Start(); restartErr == nil {
				xi.mu.Lock()
				_, _ = xi.stdin.Write([]byte(cmdStr))
				xi.mu.Unlock()
			}
			return err
		}
	}

	xi.mu.Unlock()
	return nil
}

// Close terminates the xdotool process
func (xi *XdotoolInjector) Close() error {
	xi.mu.Lock()
	defer xi.mu.Unlock()

	if !xi.isStarted {
		return nil
	}

	xi.isStarted = false
	if xi.stdin != nil {
		_ = xi.stdin.Close()
	}
	if xi.cmd != nil {
		_ = xi.cmd.Process.Kill()
	}

	xi.stdin = nil
	xi.cmd = nil

	if xi.stopChan != nil {
		close(xi.stopChan)
		xi.stopChan = nil
	}
	return nil
}

func (xi *XdotoolInjector) mouseMoveWorker() {
	ticker := time.NewTicker(16 * time.Millisecond) // ~60fps
	defer ticker.Stop()

	for {
		select {
		case <-xi.stopChan:
			return
		case <-ticker.C:
			xi.moveMu.Lock()
			event := xi.pendingMove
			xi.pendingMove = nil
			xi.moveMu.Unlock()

			if event != nil {
				xi.mu.Lock()
				if xi.isStarted {
					absX := int(event.X * float64(xi.width))
					absY := int(event.Y * float64(xi.height))
					xi.lastX = absX
					xi.lastY = absY
					if runtime.GOOS != "linux" {
						w, h := xi.width, xi.height
						xi.mu.Unlock()
						_ = injectNativeInputOS(*event, w, h, absX, absY)
						continue
					} else if xi.stdin != nil {
						cmdStr := fmt.Sprintf("mousemove %d %d\n", absX, absY)
						if _, err := xi.stdin.Write([]byte(cmdStr)); err != nil {
							log.Printf("xdotool mousemove write error: %v", err)
						}
					}
				}
				xi.mu.Unlock()
			}
		}
	}
}

func (xi *XdotoolInjector) queryDisplayGeometry() (int, int, error) {
	return queryDisplayGeometry(xi.display)
}

func queryDisplayGeometry(display string) (int, int, error) {
	return queryDisplayGeometryByOS(display)
}

func ResolveDesktopDisplay(requested string) string {
	resolved, ok := resolveDesktopDisplayWithProber(
		requested,
		os.Getenv("DISPLAY"),
		x11SocketDisplays("/tmp/.X11-unix"),
		func(display string) bool {
			_, _, err := queryDisplayGeometry(display)
			return err == nil
		},
	)
	if ok {
		if strings.TrimSpace(requested) != "" && strings.TrimSpace(requested) != resolved {
			log.Printf("Resolved remote desktop display %q to active X11 display %s", requested, resolved)
		}
		return resolved
	}

	requested = strings.TrimSpace(requested)
	if requested != "" {
		return requested
	}
	if envDisplay := strings.TrimSpace(os.Getenv("DISPLAY")); envDisplay != "" {
		return envDisplay
	}
	return ":0.0"
}

func resolveDesktopDisplayWithProber(
	requested string,
	envDisplay string,
	socketDisplays []string,
	probe func(string) bool,
) (string, bool) {
	requested = strings.TrimSpace(requested)
	envDisplay = strings.TrimSpace(envDisplay)

	if requested != "" && !isAutoDesktopDisplay(requested) {
		return requested, true
	}

	candidates := make([]string, 0, len(socketDisplays)+3)
	if envDisplay != "" {
		candidates = append(candidates, envDisplay)
	}
	candidates = append(candidates, socketDisplays...)
	if requested != "" {
		candidates = append(candidates, requested)
	}
	candidates = append(candidates, ":0.0", ":0")

	for _, candidate := range uniqueDisplayCandidates(candidates) {
		if probe(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func isAutoDesktopDisplay(display string) bool {
	switch strings.TrimSpace(display) {
	case "", ":0", ":0.0":
		return true
	default:
		return false
	}
}

func x11SocketDisplays(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	displays := make([]int, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "X") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimPrefix(name, "X"))
		if err != nil {
			continue
		}
		displays = append(displays, value)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(displays)))

	result := make([]string, 0, len(displays))
	for _, display := range displays {
		result = append(result, fmt.Sprintf(":%d", display))
	}
	return result
}

func uniqueDisplayCandidates(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func desktopCommandEnv(display string) []string {
	env := os.Environ()
	if strings.TrimSpace(display) == "" {
		return env
	}
	filtered := make([]string, 0, len(env)+1)
	hasXauthority := false
	for _, item := range env {
		if strings.HasPrefix(item, "DISPLAY=") {
			continue
		}
		if strings.HasPrefix(item, "XAUTHORITY=") {
			hasXauthority = true
		}
		filtered = append(filtered, item)
	}
	filtered = append(filtered, "DISPLAY="+display)
	if !hasXauthority {
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
			xauthority := filepath.Join(home, ".Xauthority")
			if _, err := os.Stat(xauthority); err == nil {
				filtered = append(filtered, "XAUTHORITY="+xauthority)
			}
		}
	}
	return filtered
}

func (xi *XdotoolInjector) mapButton(btn int) int {
	// Standard mapping: 1=left, 2=middle, 3=right
	if btn <= 0 || btn > 3 {
		return 1
	}
	return btn
}

func (xi *XdotoolInjector) sanitizeKey(key string) string {
	// Clean or map keys if they need special handling in xdotool
	// For example, Flutter "Backspace" needs to be "BackSpace" or "Return" to "Return".
	key = strings.TrimSpace(key)
	switch strings.ToLower(key) {
	case "enter":
		return "Return"
	case "backspace":
		return "BackSpace"
	case "tab":
		return "Tab"
	case "escape":
		return "Escape"
	case "control":
		return "ctrl"
	case "shift":
		return "shift"
	case "alt":
		return "alt"
	case "meta":
		return "super"
	}
	return key
}
