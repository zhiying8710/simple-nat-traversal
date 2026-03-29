package autostart

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSpecDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := buildSpec("darwin", "/tmp/snt", "/tmp/client.json")
	if err != nil {
		t.Fatalf("buildSpec(darwin): %v", err)
	}
	if got.filePath != filepath.Join(home, "Library", "LaunchAgents", macLaunchAgentFile) {
		t.Fatalf("unexpected filePath: %s", got.filePath)
	}
	for _, want := range []string{
		macLaunchAgentLabel,
		"<key>RunAtLoad</key>",
		"<string>/tmp/snt</string>",
		"<string>/tmp/client.json</string>",
		`"/tmp/snt" -config "/tmp/client.json"`,
	} {
		if !strings.Contains(got.content+got.launchCommand, want) {
			t.Fatalf("darwin spec missing %q:\ncontent=%s\ncommand=%s", want, got.content, got.launchCommand)
		}
	}
}

func TestBuildSpecWindows(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	got, err := buildSpec("windows", "/tmp/snt.exe", "/tmp/client.json")
	if err != nil {
		t.Fatalf("buildSpec(windows): %v", err)
	}
	if got.filePath != filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", winStartupFile) {
		t.Fatalf("unexpected filePath: %s", got.filePath)
	}
	for _, want := range []string{
		`CreateObject("WScript.Shell")`,
		`/tmp/snt.exe`,
		`/tmp/client.json`,
		`"/tmp/snt.exe" -config "/tmp/client.json"`,
	} {
		if !strings.Contains(got.content+got.launchCommand, want) {
			t.Fatalf("windows spec missing %q:\ncontent=%s\ncommand=%s", want, got.content, got.launchCommand)
		}
	}
}

func TestInstallRejectsEphemeralExecutable(t *testing.T) {
	t.Parallel()

	_, err := Install(filepath.Join(t.TempDir(), "go-build1234", "b001", "exe", "snt"), "/tmp/client.json")
	if err == nil {
		t.Fatal("expected Install to reject temporary go-build executable")
	}
}

func TestRenderStatus(t *testing.T) {
	t.Parallel()

	rendered := RenderStatus(Status{
		Platform:       "darwin",
		Installed:      true,
		FilePath:       "/Users/test/Library/LaunchAgents/com.simple-nat-traversal.snt.plist",
		ExecutablePath: "/Applications/snt",
		ConfigPath:     "/Users/test/.config/snt/client.json",
		LaunchCommand:  `"/Applications/snt" -config "/Users/test/.config/snt/client.json"`,
	})
	for _, want := range []string{
		"platform\tdarwin",
		"installed\tyes",
		"/Applications/snt",
		"/Users/test/.config/snt/client.json",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderStatus missing %q:\n%s", want, rendered)
		}
	}
}
