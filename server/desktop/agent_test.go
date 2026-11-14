package desktop

import (
	"context"
	"strings"
	"testing"
)

func TestResolveCoordsNormalizedAndAbsolute(t *testing.T) {
	ac := &AgentController{display: ":0.0"}

	// Test normalized coordinates [0.5, 0.5] on 1280x720 default
	x5 := 0.5
	y5 := 0.5
	x, y, err := ac.ResolveCoords(&x5, &y5)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if x != 640 || y != 360 {
		t.Errorf("expected 640, 360, got %d, %d", x, y)
	}

	// Test absolute coordinates [500, 300]
	xAbs := 500.0
	yAbs := 300.0
	x2, y2, err2 := ac.ResolveCoords(&xAbs, &yAbs)
	if err2 != nil {
		t.Fatalf("expected no error, got: %v", err2)
	}
	if x2 != 500 || y2 != 300 {
		t.Errorf("expected 500, 300, got %d, %d", x2, y2)
	}

	// Missing coordinates error
	_, _, errMissing := ac.ResolveCoords(nil, &yAbs)
	if errMissing == nil {
		t.Fatal("expected error on nil coordinate")
	}
}

func TestSanitizeKey(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"enter", "Return"},
		{"Return", "Return"},
		{"backspace", "BackSpace"},
		{"ctrl+c", "ctrl+c"},
		{"ctrl+alt+t", "ctrl+alt+t"},
		{"meta", "super"},
		{"Escape", "Escape"},
		{"Tab", "Tab"},
	}

	for _, c := range cases {
		got := sanitizeKey(c.input)
		if got != c.expected {
			t.Errorf("sanitizeKey(%q): expected %q, got %q", c.input, c.expected, got)
		}
	}
}

func TestButtonMapping(t *testing.T) {
	ac := &AgentController{}
	if ac.mapButton("left") != 1 {
		t.Errorf("expected left=1, got %d", ac.mapButton("left"))
	}
	if ac.mapButton("middle") != 2 {
		t.Errorf("expected middle=2, got %d", ac.mapButton("middle"))
	}
	if ac.mapButton("right") != 3 {
		t.Errorf("expected right=3, got %d", ac.mapButton("right"))
	}
	if ac.mapButton("scroll_up") != 4 {
		t.Errorf("expected scroll_up=4, got %d", ac.mapButton("scroll_up"))
	}
	if ac.mapButton("5") != 5 {
		t.Errorf("expected 5=5, got %d", ac.mapButton("5"))
	}
	if ac.mapButton("") != 1 {
		t.Errorf("expected default 1, got %d", ac.mapButton(""))
	}
}

func TestScreenshotSyntheticFallback(t *testing.T) {
	ac := &AgentController{display: ":9999"} // invalid display to force fallback
	bytes, format, err := ac.CaptureScreenshot(context.Background(), ScreenshotOptions{Format: "png"})
	if err != nil {
		t.Fatalf("expected synthetic fallback to succeed, got: %v", err)
	}
	if format != "png" {
		t.Errorf("expected png format, got %s", format)
	}
	if len(bytes) == 0 {
		t.Fatal("expected non-empty screenshot bytes")
	}
}

func TestExecuteActionSleep(t *testing.T) {
	ac := &AgentController{display: ":9999"}
	res, err := ac.ExecuteAction(context.Background(), AgentAction{
		Type:       "sleep",
		DurationMs: 10,
	})
	if err != nil {
		t.Fatalf("expected sleep to succeed, got %v", err)
	}
	if !res.Success {
		t.Errorf("expected success true, got false")
	}
	if !strings.Contains(res.Message, "Slept 10 ms") {
		t.Errorf("expected message containing Slept 10 ms, got %s", res.Message)
	}
}

func TestMCPManifestStructure(t *testing.T) {
	manifest := GenerateMCPManifest("https://desktop.svc.plus")
	if manifest["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion 2024-11-05, got %v", manifest["protocolVersion"])
	}
	tools, ok := manifest["tools"].([]map[string]any)
	if !ok || len(tools) < 5 {
		t.Fatalf("expected at least 5 tools, got %d", len(tools))
	}
}
