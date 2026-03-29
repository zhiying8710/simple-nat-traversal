package fyneapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fyneTest "fyne.io/fyne/v2/test"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

func newTestApp(t *testing.T, cfg Config) *App {
	t.Helper()

	if cfg.RuntimeManager == nil {
		cfg.RuntimeManager = control.NewRuntimeManager()
	}
	if cfg.Logs == nil {
		cfg.Logs = control.NewLogBuffer(50)
	}
	if cfg.InstallAutostart == nil {
		cfg.InstallAutostart = autostart.Install
	}
	if cfg.UninstallAutostart == nil {
		cfg.UninstallAutostart = autostart.Uninstall
	}
	if cfg.SetRuntimeLogLevel == nil {
		cfg.SetRuntimeLogLevel = client.SetRuntimeLogLevel
	}
	if cfg.LoadOverview == nil {
		cfg.LoadOverview = control.LoadOverviewForConfig
	}

	fyneApp := fyneTest.NewApp()
	t.Cleanup(func() {
		fyneApp.Quit()
	})

	a := &App{
		cfg:               cfg,
		app:               fyneApp,
		window:            fyneApp.NewWindow("test"),
		locale:            localeEnglish,
		defaultDeviceName: "test-device-abc123",
		refreshHook:       func() {},
	}
	a.buildUI()
	return a
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestCollectConfigFromFormPreservesSavedSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	cfg.DeviceName = "saved-device"
	cfg.AutoConnect = false
	cfg.Publish = map[string]config.PublishConfig{
		"game": {Local: "127.0.0.1:19132"},
	}
	cfg.Binds = map[string]config.BindConfig{
		"remote-game": {Peer: "winpc", Service: "game", Local: "127.0.0.1:29132"},
	}
	if _, changed, err := config.EnsureClientIdentity(&cfg); err != nil {
		t.Fatalf("ensure identity: %v", err)
	} else if !changed {
		t.Fatal("expected identity to be created")
	}
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()

	app.serverURLEntry.SetText("http://127.0.0.1:8080")
	app.allowInsecureCheck.SetChecked(true)
	app.deviceNameEntry.SetText("updated-device")
	app.autoConnectCheck.SetChecked(true)
	app.udpListenEntry.SetText(":9999")
	app.adminListenEntry.SetText("127.0.0.1:19999")
	app.mu.Lock()
	app.draftPublish = map[string]config.PublishConfig{
		"dns": {Local: "127.0.0.1:5300"},
	}
	app.draftBinds = map[string]config.BindConfig{
		"peer-dns": {Peer: "linux-box", Service: "dns", Local: "127.0.0.1:5301"},
	}
	app.mu.Unlock()

	got, err := app.collectConfigFromForm()
	if err != nil {
		t.Fatalf("collect config: %v", err)
	}

	if got.Password != cfg.Password {
		t.Fatalf("password changed unexpectedly: got %q want %q", got.Password, cfg.Password)
	}
	if got.AdminPassword != cfg.AdminPassword {
		t.Fatalf("admin_password changed unexpectedly: got %q want %q", got.AdminPassword, cfg.AdminPassword)
	}
	if got.IdentityPrivate != cfg.IdentityPrivate {
		t.Fatal("identity_private should be preserved")
	}
	if got.DeviceName != "updated-device" {
		t.Fatalf("unexpected device_name: %q", got.DeviceName)
	}
	if !got.AllowInsecureHTTP {
		t.Fatal("allow_insecure_http should be true")
	}
	if !got.AutoConnect {
		t.Fatal("auto_connect should be true")
	}
	if got.LogLevel != config.LogLevelInfo {
		t.Fatalf("unexpected log level: %q", got.LogLevel)
	}
	if got.Publish["dns"].Local != "127.0.0.1:5300" {
		t.Fatalf("unexpected publish map: %+v", got.Publish)
	}
	if got.Binds["peer-dns"].Peer != "linux-box" {
		t.Fatalf("unexpected binds map: %+v", got.Binds)
	}
}

func TestCollectConfigFromFormRejectsClearAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()
	app.passwordEntry.SetText("new-password")
	app.clearPasswordCheck.SetChecked(true)

	if _, err := app.collectConfigFromForm(); err == nil {
		t.Fatal("expected clear-and-replace password conflict")
	}
}

func TestTryAutoConnectStartsRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	cfg.AutoConnect = true
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	started := make(chan struct{})
	manager := control.NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	app := newTestApp(t, Config{
		ConfigPath:     path,
		RuntimeManager: manager,
	})

	if err := app.tryAutoConnect(); err != nil {
		t.Fatalf("tryAutoConnect: %v", err)
	}
	waitForSignal(t, started, "runtime start")

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := manager.Stop(stopCtx); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
}

func TestStartStopAndAutostartActions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	started := make(chan struct{})
	manager := control.NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	var installCalled bool
	var uninstallCalled bool
	app := newTestApp(t, Config{
		ExecutablePath: "/tmp/snt-gui",
		ConfigPath:     path,
		RuntimeManager: manager,
		InstallAutostart: func(executablePath, configPath string) (autostart.Status, error) {
			installCalled = true
			if executablePath != "/tmp/snt-gui" || configPath != path {
				t.Fatalf("unexpected install args: %q %q", executablePath, configPath)
			}
			return autostart.Status{Installed: true}, nil
		},
		UninstallAutostart: func() (autostart.Status, error) {
			uninstallCalled = true
			return autostart.Status{Installed: false}, nil
		},
	})
	app.loadConfigIntoForm()

	if err := app.startClient(); err != nil {
		t.Fatalf("startClient: %v", err)
	}
	waitForSignal(t, started, "runtime start")
	if got := manager.Snapshot().State; got != "running" {
		t.Fatalf("unexpected runtime state: %s", got)
	}

	if err := app.stopClient(); err != nil {
		t.Fatalf("stopClient: %v", err)
	}
	if got := manager.Snapshot().State; got != "stopped" {
		t.Fatalf("unexpected runtime state after stop: %s", got)
	}

	if err := app.installAutostart(); err != nil {
		t.Fatalf("installAutostart: %v", err)
	}
	if !installCalled {
		t.Fatal("expected install autostart hook to be called")
	}

	if err := app.uninstallAutostart(); err != nil {
		t.Fatalf("uninstallAutostart: %v", err)
	}
	if !uninstallCalled {
		t.Fatal("expected uninstall autostart hook to be called")
	}
}

func TestApplyLogLevelSavesConfigAndUpdatesRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	started := make(chan struct{}, 1)
	manager := control.NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := manager.Start(path); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	waitForSignal(t, started, "runtime start")
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = manager.Stop(stopCtx)
	}()

	var appliedLevel string
	app := newTestApp(t, Config{
		ConfigPath:     path,
		RuntimeManager: manager,
		SetRuntimeLogLevel: func(ctx context.Context, cfg config.ClientConfig, level string) (proto.LogLevelResponse, error) {
			appliedLevel = level
			return proto.LogLevelResponse{LogLevel: level}, nil
		},
	})
	app.loadConfigIntoForm()
	app.logLevelSelect.SetSelected(config.LogLevelDebug)

	if err := app.applyLogLevel(); err != nil {
		t.Fatalf("applyLogLevel: %v", err)
	}
	if appliedLevel != config.LogLevelDebug {
		t.Fatalf("unexpected applied log level: %q", appliedLevel)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if saved.LogLevel != config.LogLevelDebug {
		t.Fatalf("unexpected saved log level: %q", saved.LogLevel)
	}
}

func TestKickDeviceUsesInjectedClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var requests []proto.KickDeviceRequest
	app := newTestApp(t, Config{
		ConfigPath: path,
		KickNetworkDevice: func(ctx context.Context, cfg config.ClientConfig, req proto.KickDeviceRequest) (proto.KickDeviceResponse, error) {
			if cfg.AdminPassword != "draft-admin" {
				t.Fatalf("kick used saved admin_password instead of draft value: %q", cfg.AdminPassword)
			}
			if cfg.ServerURL != "http://127.0.0.1:8080" {
				t.Fatalf("kick used saved server_url instead of draft value: %q", cfg.ServerURL)
			}
			requests = append(requests, req)
			return proto.KickDeviceResponse{
				Removed:    true,
				DeviceID:   "dev-123",
				DeviceName: "winpc",
			}, nil
		},
	})
	app.serverURLEntry.SetText("http://127.0.0.1:8080")
	app.allowInsecureCheck.SetChecked(true)
	app.adminPasswordEntry.SetText("draft-admin")
	app.kickDeviceNameEntry.SetText("winpc")
	app.kickDeviceIDEntry.SetText("dev-123")

	if err := app.kickDevice(proto.KickDeviceRequest{DeviceName: "winpc"}); err != nil {
		t.Fatalf("kick by name: %v", err)
	}
	if err := app.kickDevice(proto.KickDeviceRequest{DeviceID: "dev-123"}); err != nil {
		t.Fatalf("kick by id: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("unexpected request count: %d", len(requests))
	}
	if requests[0].DeviceName != "winpc" || requests[1].DeviceID != "dev-123" {
		t.Fatalf("unexpected kick requests: %+v", requests)
	}
	if app.kickDeviceNameEntry.Text != "" || app.kickDeviceIDEntry.Text != "" {
		t.Fatal("kick fields should be cleared after success")
	}
}

func TestRefreshUsesDraftConfigInsteadOfSavedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://saved.example.com"
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	cfg.DeviceName = "saved-device"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	refreshed := make(chan struct{}, 1)
	refreshDone := make(chan struct{}, 1)
	app := newTestApp(t, Config{
		ConfigPath: path,
		LoadOverview: func(ctx context.Context, executablePath, configPath string, cfg config.ClientConfig, configExists bool, configErr error, opts control.OverviewOptions) (control.Overview, error) {
			if !configExists {
				t.Fatal("expected configExists=true")
			}
			if configErr != nil {
				t.Fatalf("unexpected draft config error: %v", configErr)
			}
			if cfg.ServerURL != "http://127.0.0.1:8080" {
				t.Fatalf("refresh used saved server_url instead of draft value: %q", cfg.ServerURL)
			}
			if cfg.AdminPassword != "draft-admin" {
				t.Fatalf("refresh used saved admin_password instead of draft value: %q", cfg.AdminPassword)
			}
			refreshed <- struct{}{}
			return control.Overview{
				GeneratedAt:    time.Now(),
				ExecutablePath: executablePath,
				ConfigPath:     configPath,
				ConfigExists:   configExists,
				ConfigValid:    true,
				Config: &control.OverviewConfig{
					ServerURL:               cfg.ServerURL,
					DeviceName:              cfg.DeviceName,
					AdminPasswordConfigured: true,
					PasswordConfigured:      true,
					Publish:                 cfg.Publish,
					Binds:                   cfg.Binds,
				},
			}, nil
		},
	})
	app.refreshDoneHook = func() {
		refreshDone <- struct{}{}
	}

	app.serverURLEntry.SetText("http://127.0.0.1:8080")
	app.allowInsecureCheck.SetChecked(true)
	app.adminPasswordEntry.SetText("draft-admin")
	app.deviceNameEntry.SetText("draft-device")

	app.refreshAll()
	waitForSignal(t, refreshed, "overview refresh")
	waitForSignal(t, refreshDone, "refresh completion")
}

func TestLoadConfigIntoFormWithoutConfigGeneratesDeviceName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	app := newTestApp(t, Config{
		ConfigPath: path,
	})

	app.loadConfigIntoForm()

	if got := app.deviceNameEntry.Text; strings.TrimSpace(got) == "" {
		t.Fatal("expected generated default device name")
	}
	if got := app.serverURLEntry.Text; got == "" {
		t.Fatal("expected default server_url to be populated")
	}
}

func TestQuickBindDiscoveredServiceSavesBind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	cfg.DeviceName = "macbook-air"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()
	app.mu.Lock()
	app.discovered = []discoveredService{
		{DeviceID: "dev-win", DeviceName: "winpc", ServiceName: "game"},
	}
	app.mu.Unlock()
	app.updateServiceViews()
	app.discoveredSelect.SetSelected("winpc / game")

	if err := app.quickBindDiscoveredService(); err != nil {
		t.Fatalf("quickBindDiscoveredService: %v", err)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	bind, ok := saved.Binds["winpc-game"]
	if !ok {
		t.Fatalf("expected quick bind to be saved, got %+v", saved.Binds)
	}
	if bind.Protocol != config.ServiceProtocolUDP || bind.Peer != "winpc" || bind.Service != "game" || bind.Local != "127.0.0.1:0" {
		t.Fatalf("unexpected quick bind: %+v", bind)
	}
}

func TestQuickBindDiscoveredServicePreservesTCPProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "saved-password"
	cfg.AdminPassword = "saved-admin"
	cfg.DeviceName = "macbook-air"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()
	app.mu.Lock()
	app.discovered = []discoveredService{
		{DeviceID: "dev-win", DeviceName: "winpc", ServiceName: "rdp", Protocol: config.ServiceProtocolTCP},
	}
	app.mu.Unlock()
	app.updateServiceViews()
	app.discoveredSelect.SetSelected("winpc / rdp/tcp")

	if err := app.quickBindDiscoveredService(); err != nil {
		t.Fatalf("quickBindDiscoveredService: %v", err)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	bind, ok := saved.Binds["winpc-rdp-tcp"]
	if !ok {
		t.Fatalf("expected quick tcp bind to be saved, got %+v", saved.Binds)
	}
	if bind.Protocol != config.ServiceProtocolTCP || bind.Peer != "winpc" || bind.Service != "rdp" || bind.Local != "127.0.0.1:0" {
		t.Fatalf("unexpected quick tcp bind: %+v", bind)
	}
}

func TestUpsertPublishDoesNotLeavePhantomStateOnSaveError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "https://example.com"
	cfg.Password = "saved-password"
	cfg.DeviceName = "saved-device"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()
	app.serverURLEntry.SetText("")
	app.publishNameEntry.SetText("game")
	app.publishLocalEntry.SetText("127.0.0.1:19132")

	if err := app.upsertPublish(); err == nil {
		t.Fatal("expected upsertPublish to fail because server_url is invalid")
	}

	if got := app.publishGrid.Text(); strings.Contains(got, "game") {
		t.Fatalf("publish grid should not show unsaved service, got %q", got)
	}
	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if len(saved.Publish) != 0 {
		t.Fatalf("publish map changed unexpectedly: %+v", saved.Publish)
	}
}

func TestUpsertPublishAndBindPreserveExistingProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "saved-password"
	cfg.DeviceName = "saved-device"
	cfg.Publish = map[string]config.PublishConfig{
		"rdp": {Protocol: config.ServiceProtocolTCP, Local: "127.0.0.1:3389"},
	}
	cfg.Binds = map[string]config.BindConfig{
		"win-rdp": {Protocol: config.ServiceProtocolTCP, Peer: "winpc", Service: "rdp", Local: "127.0.0.1:13389"},
	}
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()

	app.publishSelect.SetSelected("rdp")
	if app.publishProtocol.Selected != config.ServiceProtocolTCP {
		t.Fatalf("expected publish protocol selector to load tcp, got %q", app.publishProtocol.Selected)
	}
	app.publishNameEntry.SetText("rdp")
	app.publishLocalEntry.SetText("127.0.0.1:3390")
	if err := app.upsertPublish(); err != nil {
		t.Fatalf("upsertPublish: %v", err)
	}

	app.bindSelect.SetSelected("win-rdp")
	if app.bindProtocol.Selected != config.ServiceProtocolTCP {
		t.Fatalf("expected bind protocol selector to load tcp, got %q", app.bindProtocol.Selected)
	}
	app.bindNameEntry.SetText("win-rdp")
	app.bindPeerEntry.SetText("winpc")
	app.bindServiceEntry.SetText("rdp")
	app.bindLocalEntry.SetText("127.0.0.1:13390")
	if err := app.upsertBind(); err != nil {
		t.Fatalf("upsertBind: %v", err)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if saved.Publish["rdp"].Protocol != config.ServiceProtocolTCP || saved.Publish["rdp"].Local != "127.0.0.1:3390" {
		t.Fatalf("publish protocol/local not preserved: %+v", saved.Publish)
	}
	if saved.Binds["win-rdp"].Protocol != config.ServiceProtocolTCP || saved.Binds["win-rdp"].Local != "127.0.0.1:13390" {
		t.Fatalf("bind protocol/local not preserved: %+v", saved.Binds)
	}
}

func TestUpsertPublishAndBindUseSelectedProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "saved-password"
	cfg.DeviceName = "saved-device"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := newTestApp(t, Config{
		ConfigPath: path,
	})
	app.loadConfigIntoForm()

	app.publishProtocol.SetSelected(config.ServiceProtocolTCP)
	app.publishNameEntry.SetText("ssh")
	app.publishLocalEntry.SetText("127.0.0.1:22")
	if err := app.upsertPublish(); err != nil {
		t.Fatalf("upsertPublish: %v", err)
	}
	if app.publishProtocol.Selected != config.ServiceProtocolUDP {
		t.Fatalf("expected publish protocol selector to reset to udp, got %q", app.publishProtocol.Selected)
	}

	app.bindProtocol.SetSelected(config.ServiceProtocolTCP)
	app.bindNameEntry.SetText("linux-ssh")
	app.bindPeerEntry.SetText("linux-box")
	app.bindServiceEntry.SetText("ssh")
	app.bindLocalEntry.SetText("127.0.0.1:10022")
	if err := app.upsertBind(); err != nil {
		t.Fatalf("upsertBind: %v", err)
	}
	if app.bindProtocol.Selected != config.ServiceProtocolUDP {
		t.Fatalf("expected bind protocol selector to reset to udp, got %q", app.bindProtocol.Selected)
	}

	saved, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if saved.Publish["ssh"].Protocol != config.ServiceProtocolTCP || saved.Publish["ssh"].Local != "127.0.0.1:22" {
		t.Fatalf("publish protocol/local not saved from selector: %+v", saved.Publish)
	}
	if saved.Binds["linux-ssh"].Protocol != config.ServiceProtocolTCP || saved.Binds["linux-ssh"].Local != "127.0.0.1:10022" {
		t.Fatalf("bind protocol/local not saved from selector: %+v", saved.Binds)
	}
}

func TestBuildDiscoveredServicesUsesStatusPeersWithoutAdminPassword(t *testing.T) {
	overview := &control.Overview{
		Status: &client.StatusSnapshot{
			DeviceName: "macbook-air",
			Peers: []client.PeerStatus{
				{
					DeviceID:   "dev-win",
					DeviceName: "winpc",
					Services:   []string{"game", "ssh/tcp"},
					ServiceDetails: []proto.ServiceInfo{
						{Name: "game", Protocol: config.ServiceProtocolUDP},
						{Name: "ssh", Protocol: config.ServiceProtocolTCP},
					},
				},
			},
		},
	}

	got := buildDiscoveredServices(overview, "macbook-air")
	if len(got) != 2 {
		t.Fatalf("unexpected discovered service count: %+v", got)
	}
	if got[0].DeviceName != "winpc" && got[1].DeviceName != "winpc" {
		t.Fatalf("expected discovered services from peer snapshot: %+v", got)
	}
	if got[0].ServiceName == "ssh/tcp" || got[1].ServiceName == "ssh/tcp" {
		t.Fatalf("expected discovered services to keep raw service names, got: %+v", got)
	}
	if (got[0].Protocol != config.ServiceProtocolTCP && got[1].Protocol != config.ServiceProtocolTCP) ||
		(got[0].Protocol != config.ServiceProtocolUDP && got[1].Protocol != config.ServiceProtocolUDP) {
		t.Fatalf("expected discovered services to keep protocol detail, got: %+v", got)
	}
}
