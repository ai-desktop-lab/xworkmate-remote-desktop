package desktop

import (
	"errors"
	"testing"
)

func TestValidatePlatform(t *testing.T) {
	tests := []struct {
		name        string
		osName      string
		expectError bool
		targetError error
	}{
		{name: "Linux valid", osName: "linux", expectError: false},
		{name: "macOS darwin valid", osName: "darwin", expectError: false},
		{name: "macOS alias valid", osName: "macos", expectError: false},
		{name: "macOS osx valid", osName: "osx", expectError: false},
		{name: "Windows valid", osName: "windows", expectError: false},
		{name: "Windows win32 valid", osName: "win32", expectError: false},
		{name: "HarmonyOS rejected", osName: "harmonyos", expectError: true, targetError: ErrHarmonyOSRejected},
		{name: "OpenHarmony rejected", osName: "openharmony", expectError: true, targetError: ErrHarmonyOSRejected},
		{name: "OHOS rejected", osName: "ohos", expectError: true, targetError: ErrHarmonyOSRejected},
		{name: "鸿蒙 Chinese name rejected", osName: "鸿蒙", expectError: true, targetError: ErrHarmonyOSRejected},
		{name: "iOS deferred", osName: "ios", expectError: true, targetError: ErrMobileDeferred},
		{name: "Android deferred", osName: "android", expectError: true, targetError: ErrMobileDeferred},
		{name: "Unknown unsupported", osName: "solaris", expectError: true, targetError: ErrPlatformUnsupported},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlatform(tc.osName)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for %s, got nil", tc.osName)
				}
				if tc.targetError != nil && !errors.Is(err, tc.targetError) {
					t.Fatalf("expected error %v, got %v", tc.targetError, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected success for %s, got %v", tc.osName, err)
				}
			}
		})
	}
}

func TestGetPlatformCapabilities(t *testing.T) {
	caps, err := GetPlatformCapabilities("linux")
	if err != nil || caps == nil || !caps.Supported {
		t.Fatalf("linux capabilities expected to be supported: %v", err)
	}
	if caps.OSFamily != "linux" {
		t.Errorf("expected OSFamily linux, got %s", caps.OSFamily)
	}

	macCaps, err := GetPlatformCapabilities("darwin")
	if err != nil || macCaps == nil || !macCaps.Supported {
		t.Fatalf("darwin capabilities expected to be supported: %v", err)
	}
	if macCaps.OSFamily != "darwin" {
		t.Errorf("expected OSFamily darwin, got %s", macCaps.OSFamily)
	}

	winCaps, err := GetPlatformCapabilities("windows")
	if err != nil || winCaps == nil || !winCaps.Supported {
		t.Fatalf("windows capabilities expected to be supported: %v", err)
	}
	if winCaps.OSFamily != "windows" {
		t.Errorf("expected OSFamily windows, got %s", winCaps.OSFamily)
	}

	// HarmonyOS must fail
	_, err = GetPlatformCapabilities("harmonyos")
	if err == nil || !errors.Is(err, ErrHarmonyOSRejected) {
		t.Fatalf("expected ErrHarmonyOSRejected for harmonyos, got %v", err)
	}
}
