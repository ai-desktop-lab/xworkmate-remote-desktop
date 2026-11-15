package desktop

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

func queryDisplayGeometryByOS(display string) (int, int, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("osascript", "-e", `tell application "Finder" to get bounds of window of desktop`).Output()
		if err != nil {
			return 0, 0, err
		}
		// Output format: "0, 0, 1728, 1117"
		parts := strings.Split(strings.TrimSpace(string(out)), ",")
		if len(parts) < 4 {
			return 0, 0, fmt.Errorf("invalid macOS geometry output: %s", string(out))
		}
		w, err1 := strconv.Atoi(strings.TrimSpace(parts[2]))
		h, err2 := strconv.Atoi(strings.TrimSpace(parts[3]))
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("failed to parse macOS geometry: %v, %v", err1, err2)
		}
		return w, h, nil

	case "windows":
		psCmd := `Add-Type -AssemblyName System.Windows.Forms; $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds; Write-Output "$($b.Width) $($b.Height)"`
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", psCmd).Output()
		if err != nil {
			return 0, 0, err
		}
		parts := strings.Fields(string(out))
		if len(parts) < 2 {
			return 0, 0, fmt.Errorf("invalid Windows geometry output: %s", string(out))
		}
		w, err1 := strconv.Atoi(parts[0])
		h, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("failed to parse Windows geometry: %v, %v", err1, err2)
		}
		return w, h, nil

	default:
		cmd := exec.Command("xdotool", "getdisplaygeometry")
		cmd.Env = desktopCommandEnv(display)
		out, err := cmd.Output()
		if err != nil {
			return 0, 0, err
		}
		parts := strings.Fields(string(out))
		if len(parts) < 2 {
			return 0, 0, fmt.Errorf("invalid geometry output: %s", string(out))
		}
		w, err1 := strconv.Atoi(parts[0])
		h, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("failed to parse geometry: %w, %w", err1, err2)
		}
		return w, h, nil
	}
}

func injectNativeInputOS(event InputEvent, width, height int, lastX, lastY int) error {
	switch runtime.GOOS {
	case "darwin":
		return injectMacOSInput(event, width, height, lastX, lastY)
	case "windows":
		return injectWindowsInput(event, width, height)
	default:
		return fmt.Errorf("injectNativeInputOS should not be called on %s", runtime.GOOS)
	}
}

func injectMacOSInput(event InputEvent, width, height int, lastX, lastY int) error {
	absX := lastX
	absY := lastY
	if event.X > 0 || event.Y > 0 {
		absX = int(event.X * float64(width))
		absY = int(event.Y * float64(height))
	}

	var jxa string
	switch event.Type {
	case "mouse_move":
		jxa = fmt.Sprintf(`ObjC.import("CoreGraphics"); var p = $.CGPointMake(%d, %d); var e = $.CGEventCreateMouseEvent(null, $.kCGEventMouseMoved, p, $.kCGMouseButtonLeft); $.CGEventPost($.kCGHIDEventTap, e);`, absX, absY)
	case "mouse_down":
		evType, btn := macOSMouseEvent(event.Button, true)
		jxa = fmt.Sprintf(`ObjC.import("CoreGraphics"); var p = $.CGPointMake(%d, %d); var e = $.CGEventCreateMouseEvent(null, %s, p, %s); $.CGEventPost($.kCGHIDEventTap, e);`, absX, absY, evType, btn)
	case "mouse_up":
		evType, btn := macOSMouseEvent(event.Button, false)
		jxa = fmt.Sprintf(`ObjC.import("CoreGraphics"); var p = $.CGPointMake(%d, %d); var e = $.CGEventCreateMouseEvent(null, %s, p, %s); $.CGEventPost($.kCGHIDEventTap, e);`, absX, absY, evType, btn)
	case "scroll":
		delta := 3
		if event.Button == 5 {
			delta = -3
		}
		jxa = fmt.Sprintf(`ObjC.import("CoreGraphics"); var e = $.CGEventCreateScrollWheelEvent(null, $.kCGScrollEventUnitLine, 1, %d); $.CGEventPost($.kCGHIDEventTap, e);`, delta)
	case "key_down":
		key := sanitizeAppleScriptKey(event.Key)
		if key == "" {
			return nil
		}
		script := fmt.Sprintf(`tell application "System Events" to keystroke "%s"`, key)
		if isSpecialAppleScriptKey(event.Key) {
			script = fmt.Sprintf(`tell application "System Events" to key code %s`, key)
		}
		return exec.Command("osascript", "-e", script).Run()
	case "key_up":
		return nil
	default:
		return fmt.Errorf("unsupported macOS input type: %s", event.Type)
	}

	return exec.Command("osascript", "-l", "JavaScript", "-e", jxa).Run()
}

func macOSMouseEvent(btn int, down bool) (string, string) {
	switch btn {
	case 3:
		if down {
			return "$.kCGEventRightMouseDown", "$.kCGMouseButtonRight"
		}
		return "$.kCGEventRightMouseUp", "$.kCGMouseButtonRight"
	case 2:
		if down {
			return "$.kCGEventOtherMouseDown", "$.kCGMouseButtonCenter"
		}
		return "$.kCGEventOtherMouseUp", "$.kCGMouseButtonCenter"
	default:
		if down {
			return "$.kCGEventLeftMouseDown", "$.kCGMouseButtonLeft"
		}
		return "$.kCGEventLeftMouseUp", "$.kCGMouseButtonLeft"
	}
}

func isSpecialAppleScriptKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "enter", "return", "backspace", "tab", "escape":
		return true
	default:
		return false
	}
}

func sanitizeAppleScriptKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "enter", "return":
		return "36"
	case "backspace":
		return "51"
	case "tab":
		return "48"
	case "escape":
		return "53"
	default:
		clean := strings.ReplaceAll(key, `\`, `\\`)
		clean = strings.ReplaceAll(clean, `"`, `\"`)
		return clean
	}
}

func injectWindowsInput(event InputEvent, width, height int) error {
	absX := int(event.X * float64(width))
	absY := int(event.Y * float64(height))

	const winHeader = `$sig = '[DllImport("user32.dll")] public static extern bool SetCursorPos(int X, int Y); [DllImport("user32.dll")] public static extern void mouse_event(uint dwFlags, int dx, int dy, int dwData, int dwExtraInfo); [DllImport("user32.dll")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, int dwExtraInfo);'; $u = Add-Type -MemberDefinition $sig -Name "Win32Input" -Namespace "XWorkmate" -PassThru; `

	var body string
	switch event.Type {
	case "mouse_move":
		body = fmt.Sprintf(`$u::SetCursorPos(%d, %d);`, absX, absY)
	case "mouse_down":
		flag := windowsMouseFlag(event.Button, true)
		body = fmt.Sprintf(`$u::mouse_event(%d, 0, 0, 0, 0);`, flag)
	case "mouse_up":
		flag := windowsMouseFlag(event.Button, false)
		body = fmt.Sprintf(`$u::mouse_event(%d, 0, 0, 0, 0);`, flag)
	case "scroll":
		delta := 120
		if event.Button == 5 {
			delta = -120
		}
		body = fmt.Sprintf(`$u::mouse_event(0x0800, 0, 0, %d, 0);`, delta)
	case "key_down":
		key := sanitizeWindowsSendKey(event.Key)
		if key == "" {
			return nil
		}
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait("%s")`, key)
		return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Run()
	case "key_up":
		return nil
	default:
		return fmt.Errorf("unsupported Windows input type: %s", event.Type)
	}

	return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", winHeader+body).Run()
}

func windowsMouseFlag(btn int, down bool) uint32 {
	switch btn {
	case 3:
		if down {
			return 0x0008 // MOUSEEVENTF_RIGHTDOWN
		}
		return 0x0010 // MOUSEEVENTF_RIGHTUP
	case 2:
		if down {
			return 0x0020 // MOUSEEVENTF_MIDDLEDOWN
		}
		return 0x0040 // MOUSEEVENTF_MIDDLEUP
	default:
		if down {
			return 0x0002 // MOUSEEVENTF_LEFTDOWN
		}
		return 0x0004 // MOUSEEVENTF_LEFTUP
	}
}

func sanitizeWindowsSendKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "enter", "return":
		return "{ENTER}"
	case "backspace":
		return "{BACKSPACE}"
	case "tab":
		return "{TAB}"
	case "escape":
		return "{ESC}"
	default:
		return strings.ReplaceAll(key, `"`, `""`)
	}
}
