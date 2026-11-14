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

func TestCoordIsNormalizedAutoAndOverrides(t *testing.T) {
	trueVal := true
	falseVal := false
	cases := []struct {
		name       string
		x, y       float64
		mode       string
		flag       *bool
		normalized bool
	}{
		{"center fraction", 0.5, 0.5, "", nil, true},
		{"zero integers are pixels", 0, 0, "", nil, false},
		{"one integers are pixels", 1, 1, "", nil, false},
		{"1.0 1.0 are pixels", 1.0, 1.0, "", nil, false},
		{"mixed fraction is normalized", 0.0, 0.5, "", nil, true},
		{"pixels beyond 1", 100, 80, "", nil, false},
		{"explicit normalized 1,1", 1, 1, "normalized", nil, true},
		{"explicit pixel 0.5", 0.5, 0.5, "pixel", nil, false},
		{"normalized flag true", 1, 1, "", &trueVal, true},
		{"normalized flag false", 0.5, 0.5, "", &falseVal, false},
	}
	for _, tc := range cases {
		got := CoordIsNormalized(tc.x, tc.y, tc.mode, tc.flag)
		if got != tc.normalized {
			t.Errorf("%s: CoordIsNormalized(%v,%v) = %v, want %v", tc.name, tc.x, tc.y, got, tc.normalized)
		}
	}
}

func TestResolveCoordsIntegerPixelsAndNormalized(t *testing.T) {
	ac := &AgentController{display: ":0.0"} // geometry falls back to 1280x720

	assertXY := func(name string, x, y float64, mode string, flag *bool, wantX, wantY int) {
		t.Helper()
		gotX, gotY, err := ac.ResolveCoordsWithMode(&x, &y, mode, flag)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if gotX != wantX || gotY != wantY {
			t.Errorf("%s: got (%d,%d), want (%d,%d)", name, gotX, gotY, wantX, wantY)
		}
	}

	assertXY("0,0 pixels", 0, 0, "", nil, 0, 0)
	assertXY("1,1 pixels", 1, 1, "", nil, 1, 1)
	assertXY("1.0,1.0 pixels", 1.0, 1.0, "", nil, 1, 1)
	assertXY("0.5,0.5 normalized", 0.5, 0.5, "", nil, 640, 360)
	norm := true
	assertXY("explicit normalized 1.0,1.0", 1.0, 1.0, "", &norm, 1279, 719)
	assertXY("coord_mode normalized 1,1", 1, 1, "normalized", nil, 1279, 719)
	assertXY("absolute 500,300", 500, 300, "", nil, 500, 300)
}

func TestApplyActionAliases(t *testing.T) {
	click := AgentAction{Action: "click", Clicks: 3}
	click.ApplyActionAliases()
	if click.Count != 3 {
		t.Errorf("clicks alias: got count %d", click.Count)
	}

	key := AgentAction{Action: "key", Keys: []string{"ctrl", "c"}}
	key.ApplyActionAliases()
	if key.Key != "ctrl+c" {
		t.Errorf("keys alias: got %q", key.Key)
	}

	x, y := 0.2, 0.3
	toX, toY := 0.8, 0.9
	drag := AgentAction{Action: "drag", X: &x, Y: &y, ToX: &toX, ToY: &toY}
	drag.ApplyActionAliases()
	if drag.FromX == nil || *drag.FromX != 0.2 || drag.FromY == nil || *drag.FromY != 0.3 {
		t.Errorf("drag x,y alias: from=(%v,%v)", drag.FromX, drag.FromY)
	}

	scroll := AgentAction{Action: "scroll", Direction: "up", Amount: 3}
	scroll.ApplyActionAliases()
	if scroll.Dy != -3 || scroll.Count != 3 {
		t.Errorf("scroll direction/amount: dy=%d count=%d", scroll.Dy, scroll.Count)
	}
}

func TestShouldPasteText(t *testing.T) {
	if ShouldPasteText("hello", "") {
		t.Error("short ASCII should type, not paste")
	}
	if !ShouldPasteText("hello\nworld", "") {
		t.Error("multiline should paste")
	}
	if !ShouldPasteText("你好世界", "") {
		t.Error("non-ASCII should paste")
	}
	long := strings.Repeat("a", TypePasteRuneThreshold)
	if !ShouldPasteText(long, "") {
		t.Error("long ASCII should auto-paste")
	}
	if ShouldPasteText(long, "type") {
		t.Error("mode=type should force xdotool even for long text")
	}
	if !ShouldPasteText("hi", "paste") {
		t.Error("mode=paste should always paste")
	}
}
