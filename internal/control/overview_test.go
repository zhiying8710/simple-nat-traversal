package control

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

func TestLoadOverviewMissingConfig(t *testing.T) {
	t.Parallel()

	executable := filepath.Join(t.TempDir(), "snt")
	configPath := filepath.Join(t.TempDir(), "missing-client.json")

	got, err := LoadOverview(context.Background(), executable, configPath, OverviewOptions{})
	if err != nil {
		t.Fatalf("LoadOverview: %v", err)
	}
	if got.ConfigExists {
		t.Fatal("expected ConfigExists to be false")
	}
	if got.ConfigValid {
		t.Fatal("expected ConfigValid to be false")
	}
	if got.ConfigError == "" {
		t.Fatal("expected ConfigError to be set")
	}
}

func TestRenderOverview(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 28, 15, 0, 0, 0, time.UTC)
	cfg := OverviewConfig{
		ServerURL:   "http://127.0.0.1:8080",
		DeviceName:  "mac-a",
		UDPListen:   ":0",
		AdminListen: "127.0.0.1:19090",
		LogLevel:    config.LogLevelInfo,
		Publish: map[string]config.PublishConfig{
			"echo": {Local: "127.0.0.1:19132"},
		},
		Binds: map[string]config.BindConfig{
			"echo-b": {Peer: "win-b", Service: "echo", Local: "127.0.0.1:29132"},
		},
	}
	status := client.StatusSnapshot{
		ObservedAddr: "1.2.3.4:50000",
		NetworkState: "joined",
		RejoinCount:  1,
		Peers:        []client.PeerStatus{{DeviceName: "win-b"}},
	}
	network := proto.NetworkDevicesResponse{
		Devices: []proto.NetworkDeviceStatus{{DeviceName: "mac-a"}, {DeviceName: "win-b"}},
	}

	rendered := RenderOverview(Overview{
		GeneratedAt:    now,
		ExecutablePath: "/Applications/snt",
		ConfigPath:     "/Users/test/client.json",
		ConfigExists:   true,
		ConfigValid:    true,
		ClientRunning:  true,
		Autostart: autostart.Status{
			Installed: true,
			FilePath:  "/Users/test/Library/LaunchAgents/com.simple-nat-traversal.snt.plist",
		},
		Config:  &cfg,
		Status:  &status,
		Network: &network,
	})

	for _, want := range []string{
		"config_exists\tyes",
		"config_valid\tyes",
		"client_running\tyes",
		"autostart_installed\tyes",
		"device_name\tmac-a",
		"log_level\tinfo",
		"publish_count\t1",
		"bind_count\t1",
		"network_state\tjoined",
		"peer_count\t1",
		"network_devices\t2",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("RenderOverview missing %q:\n%s", want, rendered)
		}
	}
}

func TestLoadOverviewRedactsSecrets(t *testing.T) {
	t.Parallel()

	executable := filepath.Join(t.TempDir(), "snt")
	configPath := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.Password = "network-secret-1234"
	cfg.AdminPassword = "admin-secret-5678"
	cfg.DeviceName = "mac-a"
	cfg.AutoConnect = true
	if err := config.SaveClientConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}
	saved, err := config.LoadClientConfig(configPath)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}

	got, err := LoadOverview(context.Background(), executable, configPath, OverviewOptions{})
	if err != nil {
		t.Fatalf("LoadOverview: %v", err)
	}
	if got.Config == nil {
		t.Fatal("expected redacted config in overview")
	}
	if !got.Config.PasswordConfigured || !got.Config.AdminPasswordConfigured || !got.Config.IdentityConfigured {
		t.Fatalf("expected configured-secret flags to be preserved: %+v", got.Config)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(raw)
	for _, secret := range []string{
		saved.Password,
		saved.AdminPassword,
		saved.IdentityPrivate,
		`"identity_private"`,
		`"password":"`,
		`"admin_password":"`,
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("overview leaked secret %q in %s", secret, text)
		}
	}
	if !strings.Contains(text, `"device_name":"mac-a"`) {
		t.Fatalf("expected redacted overview config to keep non-secret fields: %s", text)
	}
}
