package desktopapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
)

func newTestService(t *testing.T, configPath string, deps Dependencies) *Service {
	t.Helper()

	if deps.ConfigPath == "" {
		deps.ConfigPath = configPath
	}
	if deps.ExecutablePath == "" {
		deps.ExecutablePath = filepath.Join(t.TempDir(), "snt-gui")
	}
	if deps.LoadOverview == nil {
		deps.LoadOverview = func(context.Context, string, string, config.ClientConfig, bool, error, control.OverviewOptions) (control.Overview, error) {
			return control.Overview{}, nil
		}
	}
	return New(deps)
}

func TestCollectConfigPreservesSavedSecretsWhenInputIsBlank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "network-secret"
	cfg.AdminPassword = "admin-secret"
	cfg.DeviceName = "macbook-air"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := newTestService(t, path, Dependencies{})

	loaded, err := service.loadEditableConfig()
	if err != nil {
		t.Fatalf("loadEditableConfig: %v", err)
	}

	got, err := service.collectConfig(loaded.Config)
	if err != nil {
		t.Fatalf("collectConfig: %v", err)
	}

	if got.Password != cfg.Password {
		t.Fatalf("Password = %q, want %q", got.Password, cfg.Password)
	}
	if got.AdminPassword != cfg.AdminPassword {
		t.Fatalf("AdminPassword = %q, want %q", got.AdminPassword, cfg.AdminPassword)
	}
}

func TestQuickBindDiscoveredPersistsBindAndRestartsRunningRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "network-secret"
	cfg.DeviceName = "macbook-air"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	manager := control.NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		<-ctx.Done()
		return ctx.Err()
	})

	service := newTestService(t, path, Dependencies{
		RuntimeManager: manager,
	})

	if _, err := manager.Start(path); err != nil {
		t.Fatalf("manager.Start: %v", err)
	}
	waitForRunning(t, manager)

	loaded, err := service.loadEditableConfig()
	if err != nil {
		t.Fatalf("loadEditableConfig: %v", err)
	}

	result, err := service.QuickBindDiscovered(loaded.Config, DiscoveredService{
		DeviceName:  "winpc",
		ServiceName: "rdp",
		Protocol:    config.ServiceProtocolTCP,
	})
	if err != nil {
		t.Fatalf("QuickBindDiscovered: %v", err)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}
	bind, ok := saved.Binds["winpc-rdp-tcp"]
	if !ok {
		t.Fatalf("expected quick bind key to be persisted, binds=%v", saved.Binds)
	}
	if bind.Protocol != config.ServiceProtocolTCP || bind.Peer != "winpc" || bind.Service != "rdp" {
		t.Fatalf("unexpected bind: %+v", bind)
	}
	if result.Message != "配置已保存，客户端已自动重启" {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestAutoStartStartsRuntimeWhenConfigEnablesAutoConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "network-secret"
	cfg.DeviceName = "macbook-air"
	cfg.AutoConnect = true
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	manager := control.NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		<-ctx.Done()
		return ctx.Err()
	})

	service := newTestService(t, path, Dependencies{
		RuntimeManager: manager,
	})

	if err := service.AutoStart(); err != nil {
		t.Fatalf("AutoStart: %v", err)
	}
	waitForRunning(t, manager)
}

func TestSaveConfigAllowsClearingListenerFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "network-secret"
	cfg.DeviceName = "macbook-air"
	cfg.UDPListen = "127.0.0.1:19999"
	cfg.AdminListen = "127.0.0.1:19091"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	service := newTestService(t, path, Dependencies{})

	loaded, err := service.loadEditableConfig()
	if err != nil {
		t.Fatalf("loadEditableConfig: %v", err)
	}
	loaded.Config.UDPListen = ""
	loaded.Config.AdminListen = ""

	result, err := service.SaveConfig(loaded.Config)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}
	if saved.UDPListen != ":0" {
		t.Fatalf("UDPListen = %q, want %q", saved.UDPListen, ":0")
	}
	if saved.AdminListen != "" {
		t.Fatalf("AdminListen = %q, want empty", saved.AdminListen)
	}
	if result.State.Config.Config.UDPListen != ":0" {
		t.Fatalf("State UDPListen = %q, want %q", result.State.Config.Config.UDPListen, ":0")
	}
	if result.State.Config.Config.AdminListen != "" {
		t.Fatalf("State AdminListen = %q, want empty", result.State.Config.Config.AdminListen)
	}
}

func waitForRunning(t *testing.T, manager *control.RuntimeManager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if manager.Snapshot().State == "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime manager did not reach running state, got %q", manager.Snapshot().State)
}
