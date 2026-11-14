package desktop

import (
	"strings"
	"testing"
)

func TestBuildGStreamerUsesBrowserFriendlyH264(t *testing.T) {
	pm := NewPipelineManager()

	tool, args, err := pm.buildGStreamer(PipelineConfig{
		Display: ":11.0",
		Port:    5004,
		Bitrate: 2000,
	})
	if err != nil {
		t.Fatalf("buildGStreamer returned error: %v", err)
	}

	if tool != "gst-launch-1.0" {
		t.Fatalf("expected gst-launch-1.0, got %q", tool)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"video/x-raw,format=I420",
		"width=1280",
		"height=720",
		"x264enc",
		"tune=zerolatency",
		"key-int-max=30",
		"byte-stream=true",
		"video/x-h264,profile=baseline",
		"rtph264pay",
		"config-interval=1",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected GStreamer args to contain %q, got %s", expected, joined)
		}
	}
}

func TestNormalizeVideoDimensionUsesEvenValues(t *testing.T) {
	if got := normalizeVideoDimension(1353, 1280); got != 1352 {
		t.Fatalf("expected odd dimension to be rounded down to 1352, got %d", got)
	}
	if got := normalizeVideoDimension(0, 720); got != 720 {
		t.Fatalf("expected fallback for zero dimension, got %d", got)
	}
	if got := normalizeVideoDimension(1, 720); got != 720 {
		t.Fatalf("expected fallback for too-small dimension, got %d", got)
	}
}

func TestBuildFFmpegUsesBrowserFriendlyH264(t *testing.T) {
	pm := NewPipelineManager()

	tool, args, err := pm.buildFFmpeg(PipelineConfig{
		Display: ":11.0",
		Port:    5004,
		Bitrate: 2000,
		FPS:     30,
		Width:   1280,
		Height:  720,
	})
	if err != nil {
		t.Fatalf("buildFFmpeg returned error: %v", err)
	}

	if tool != "ffmpeg" {
		t.Fatalf("expected ffmpeg, got %q", tool)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"-c:v libx264",
		"-tune zerolatency",
		"-profile:v baseline",
		"-pix_fmt yuv420p",
		"-x264-params repeat-headers=1",
		"-g 30",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("expected FFmpeg args to contain %q, got %s", expected, joined)
		}
	}
}

func TestBuildGStreamerUseGPUSelectsNVENC(t *testing.T) {
	pm := NewPipelineManager()
	_, args, err := pm.buildGStreamer(PipelineConfig{
		Display: ":11.0",
		Port:    5004,
		Bitrate: 2000,
		UseGPU:  true,
	})
	if err != nil {
		t.Fatalf("buildGStreamer: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "nvh264enc") {
		t.Fatalf("expected nvh264enc when UseGPU=true, got %s", joined)
	}
	if strings.Contains(joined, "x264enc") {
		t.Fatalf("did not expect software x264enc when UseGPU=true, got %s", joined)
	}
}

func TestBuildFFmpegUseGPUSelectsNVENC(t *testing.T) {
	pm := NewPipelineManager()
	_, args, err := pm.buildFFmpeg(PipelineConfig{
		Display: ":11.0",
		Port:    5004,
		Width:   1280,
		Height:  720,
		FPS:     30,
		Bitrate: 2000,
		UseGPU:  true,
	})
	if err != nil {
		t.Fatalf("buildFFmpeg: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "h264_nvenc") {
		t.Fatalf("expected h264_nvenc when UseGPU=true, got %s", joined)
	}
}
