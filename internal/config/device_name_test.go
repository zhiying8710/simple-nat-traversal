package config

import "testing"

func TestBuildSuggestedDeviceName(t *testing.T) {
	got := buildSuggestedDeviceName("macOS 15.4", "Alice Zhang", "abc123")
	want := "15-4-alice-zhang-abc123"
	if got != want {
		t.Fatalf("unexpected suggested device name: got %q want %q", got, want)
	}
}

func TestBuildSuggestedDeviceNameFallsBack(t *testing.T) {
	got := buildSuggestedDeviceName("", "", "")
	if got != "device" {
		t.Fatalf("unexpected fallback name: %q", got)
	}
}
