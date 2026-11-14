package desktop

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// Platform represents an operating system platform
type Platform string

const (
	PlatformLinux   Platform = "linux"
	PlatformMacOS   Platform = "darwin"
	PlatformWindows Platform = "windows"
	PlatformHarmony Platform = "harmonyos"
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformUnknown Platform = "unknown"
)

var (
	// ErrPlatformUnsupported is returned when a platform is not supported
	ErrPlatformUnsupported = errors.New("platform is not supported")

	// ErrHarmonyOSRejected is explicitly returned when HarmonyOS / OpenHarmony is specified
	ErrHarmonyOSRejected = errors.New("platform rejected: HarmonyOS / OpenHarmony is explicitly not supported by xworkmate-remote-desktop architecture")

	// ErrMobileDeferred is returned when mobile sandboxed platforms (iOS/Android) are specified
	ErrMobileDeferred = errors.New("platform deferred: iOS and Android mobile sandboxes are not supported as desktop hosts; reserved for future")
)

// CurrentPlatform returns the active host platform
func CurrentPlatform() Platform {
	switch runtime.GOOS {
	case "linux":
		return PlatformLinux
	case "darwin":
		return PlatformMacOS
	case "windows":
		return PlatformWindows
	default:
		return Platform(runtime.GOOS)
	}
}

// IsDesktopSupported returns whether the active operating system is one of the 3 supported desktop OSes
func IsDesktopSupported() bool {
	p := CurrentPlatform()
	return p == PlatformLinux || p == PlatformMacOS || p == PlatformWindows
}

// ValidatePlatform checks whether the target platform name is supported
func ValidatePlatform(osName string) error {
	normalized := strings.ToLower(strings.TrimSpace(osName))
	if normalized == "" {
		normalized = runtime.GOOS
	}

	switch normalized {
	case "linux":
		return nil
	case "darwin", "macos", "osx", "mac":
		return nil
	case "windows", "win32", "win64", "win":
		return nil
	case "harmonyos", "openharmony", "ohos", "harmony", "鸿蒙":
		return fmt.Errorf("%w: %s", ErrHarmonyOSRejected, osName)
	case "android", "ios", "iphone", "ipad":
		return fmt.Errorf("%w: %s", ErrMobileDeferred, osName)
	default:
		return fmt.Errorf("%w: %s (supported desktop platforms: Linux, macOS, Windows; HarmonyOS explicitly unsupported; iOS/Android reserved for future)", ErrPlatformUnsupported, osName)
	}
}

// PlatformCapabilities provides a manifest of cross-platform desktop capabilities
type PlatformCapabilities struct {
	Platform          string   `json:"platform"`
	OSFamily          string   `json:"os_family"`
	Supported         bool     `json:"supported"`
	ScreenCapture     string   `json:"screen_capture"`
	InputActuation    string   `json:"input_actuation"`
	ClipboardSupport  string   `json:"clipboard_support"`
	WindowManagement  string   `json:"window_management"`
	Notes             string   `json:"notes,omitempty"`
}

// GetPlatformCapabilities returns the capabilities for the current or specified platform
func GetPlatformCapabilities(osName string) (*PlatformCapabilities, error) {
	if err := ValidatePlatform(osName); err != nil {
		return &PlatformCapabilities{
			Platform:  osName,
			OSFamily:  osName,
			Supported: false,
			Notes:     err.Error(),
		}, err
	}

	plat := CurrentPlatform()
	if osName != "" {
		switch strings.ToLower(osName) {
		case "darwin", "macos", "osx", "mac":
			plat = PlatformMacOS
		case "linux":
			plat = PlatformLinux
		case "windows", "win32", "win64", "win":
			plat = PlatformWindows
		}
	}

	switch plat {
	case PlatformMacOS:
		return &PlatformCapabilities{
			Platform:         "macOS",
			OSFamily:         "darwin",
			Supported:        true,
			ScreenCapture:    "screencapture / AVFoundation (GStreamer avfvideosrc / FFmpeg avfoundation)",
			InputActuation:   "AppleScript System Events / CoreGraphics CGEvent",
			ClipboardSupport: "pbcopy / pbpaste",
			WindowManagement: "AppleScript System Events UI processes",
			Notes:            "Full tier-1 macOS desktop actuation and perception",
		}, nil
	case PlatformWindows:
		return &PlatformCapabilities{
			Platform:         "Windows",
			OSFamily:         "windows",
			Supported:        true,
			ScreenCapture:    "GDI / Desktop Duplication (GStreamer dshow / FFmpeg gdigrab)",
			InputActuation:   "Win32 SendInput / PowerShell SendKeys & mouse_event",
			ClipboardSupport: "clip.exe / PowerShell Set-Clipboard & Get-Clipboard",
			WindowManagement: "PowerShell Get-Process MainWindowTitle / Win32 EnumWindows",
			Notes:            "Full tier-1 Windows desktop actuation and perception",
		}, nil
	case PlatformLinux:
		return &PlatformCapabilities{
			Platform:         "Linux",
			OSFamily:         "linux",
			Supported:        true,
			ScreenCapture:    "GStreamer ximagesrc / FFmpeg x11grab / scrot / import",
			InputActuation:   "xdotool (X11) / ydotool",
			ClipboardSupport: "xclip / xsel",
			WindowManagement: "wmctrl / xdotool / xprop",
			Notes:            "Full tier-1 Linux X11 and container desktop actuation and perception",
		}, nil
	default:
		return nil, ErrPlatformUnsupported
	}
}
