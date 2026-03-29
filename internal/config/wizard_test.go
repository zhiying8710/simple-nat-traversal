package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoadClientConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	want := ClientConfig{
		ServerURL:         "http://127.0.0.1:8080",
		AllowInsecureHTTP: false,
		Password:          "secret-password",
		AdminPassword:     "admin-secret",
		DeviceName:        "mac-a",
		AutoConnect:       true,
		UDPListen:         ":0",
		AdminListen:       "127.0.0.1:19090",
		Publish: map[string]PublishConfig{
			"echo": {Local: "127.0.0.1:19132"},
		},
		Binds: map[string]BindConfig{
			"echo-b": {
				Peer:    "win-b",
				Service: "echo",
				Local:   "127.0.0.1:29132",
			},
		},
	}

	if err := SaveClientConfig(path, want); err != nil {
		t.Fatalf("save client config: %v", err)
	}
	got, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load client config: %v", err)
	}

	if got.ServerURL != want.ServerURL || got.AllowInsecureHTTP != want.AllowInsecureHTTP || got.Password != want.Password || got.AdminPassword != want.AdminPassword || got.DeviceName != want.DeviceName || got.AutoConnect != want.AutoConnect || got.AdminListen != want.AdminListen {
		t.Fatalf("unexpected config roundtrip: got=%+v want=%+v", got, want)
	}
	if got.IdentityPrivate == "" {
		t.Fatalf("expected identity_private to be generated: %+v", got)
	}
	if got.Publish["echo"].Local != "127.0.0.1:19132" {
		t.Fatalf("unexpected publish entry: %+v", got.Publish)
	}
	if got.Binds["echo-b"].Peer != "win-b" || got.Binds["echo-b"].Service != "echo" || got.Binds["echo-b"].Local != "127.0.0.1:29132" {
		t.Fatalf("unexpected bind entry: %+v", got.Binds)
	}
}

func TestInitClientConfigInteractive(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	input := strings.NewReader(strings.Join([]string{
		"http://127.0.0.1:8080",
		"",
		"another-secret",
		"admin-secret",
		"win-b",
		"y",
		":0",
		"127.0.0.1:19091",
		"y",
		"echo",
		"127.0.0.1:19132",
		"",
		"y",
		"echo-a",
		"mac-a",
		"echo",
		"127.0.0.1:29132",
		"",
	}, "\n") + "\n")
	var output bytes.Buffer

	if _, err := InitClientConfigInteractive(path, input, &output); err != nil {
		t.Fatalf("init client config interactive: %v", err)
	}

	cfg, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if cfg.DeviceName != "win-b" || cfg.Password != "another-secret" || cfg.AdminPassword != "admin-secret" || cfg.AllowInsecureHTTP {
		t.Fatalf("unexpected generated basic config: %+v", cfg)
	}
	if !cfg.AutoConnect {
		t.Fatalf("expected auto_connect to be true: %+v", cfg)
	}
	if cfg.IdentityPrivate == "" {
		t.Fatalf("expected identity_private to be generated: %+v", cfg)
	}
	if cfg.Publish["echo"].Local != "127.0.0.1:19132" {
		t.Fatalf("unexpected generated publish config: %+v", cfg.Publish)
	}
	if cfg.Binds["echo-a"].Peer != "mac-a" {
		t.Fatalf("unexpected generated bind config: %+v", cfg.Binds)
	}
	if !strings.Contains(output.String(), "saved") {
		t.Fatalf("wizard output missing save confirmation: %q", output.String())
	}
}

func TestShowClientConfigRedactsSecretsByDefault(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.Password = "secret-password"
	cfg.AdminPassword = "admin-secret"
	cfg.DeviceName = "mac-a"
	if err := SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}
	saved, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}

	var output bytes.Buffer
	if err := ShowClientConfig(path, &output, false); err != nil {
		t.Fatalf("ShowClientConfig: %v", err)
	}
	text := output.String()
	for _, secret := range []string{saved.Password, saved.AdminPassword, saved.IdentityPrivate} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted show-config leaked secret %q in %s", secret, text)
		}
	}
	if !strings.Contains(text, "redacted") {
		t.Fatalf("expected redacted output, got %s", text)
	}
}

func TestSaveClientConfigRejectsPublicHTTPWithoutOptIn(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := ClientDefaults()
	cfg.ServerURL = "http://203.0.113.10:8080"
	cfg.Password = "secret-password"
	cfg.DeviceName = "mac-a"
	if err := SaveClientConfig(path, cfg); err == nil {
		t.Fatal("expected public http server_url without opt-in to fail")
	}
}

func TestSaveClientConfigAllowsPublicHTTPWithOptIn(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := ClientDefaults()
	cfg.ServerURL = "http://203.0.113.10:8080"
	cfg.AllowInsecureHTTP = true
	cfg.Password = "secret-password"
	cfg.DeviceName = "mac-a"
	if err := SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}
}

func TestShowClientConfigUnsafeIncludesSecrets(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.Password = "secret-password"
	cfg.AdminPassword = "admin-secret"
	cfg.DeviceName = "mac-a"
	if err := SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}
	saved, err := LoadClientConfig(path)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}

	var output bytes.Buffer
	if err := ShowClientConfig(path, &output, true); err != nil {
		t.Fatalf("ShowClientConfig: %v", err)
	}
	text := output.String()
	for _, secret := range []string{saved.Password, saved.AdminPassword, saved.IdentityPrivate} {
		if !strings.Contains(text, secret) {
			t.Fatalf("unsafe show-config missing secret %q in %s", secret, text)
		}
	}
}

func TestEditClientConfigInteractiveDoesNotEchoExistingSecrets(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.Password = "secret-password"
	cfg.AdminPassword = "admin-secret"
	cfg.DeviceName = "mac-a"
	if err := SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}

	input := strings.NewReader(strings.Join([]string{
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"",
		"n",
		"n",
	}, "\n") + "\n")
	var output bytes.Buffer

	if _, err := EditClientConfigInteractive(path, input, &output); err != nil {
		t.Fatalf("EditClientConfigInteractive: %v", err)
	}
	text := output.String()
	if strings.Contains(text, "secret-password") || strings.Contains(text, "admin-secret") {
		t.Fatalf("edit-config output leaked existing secret: %s", text)
	}
	if !strings.Contains(text, "password [configured]:") || !strings.Contains(text, "admin_password [configured]:") {
		t.Fatalf("expected masked password prompts, got: %s", text)
	}
}
