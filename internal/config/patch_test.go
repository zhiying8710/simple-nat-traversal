package config

import (
	"path/filepath"
	"testing"
)

func TestApplyClientConfigPatch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	if err := SaveClientConfig(path, ClientConfig{
		ServerURL:     "http://127.0.0.1:8080",
		Password:      "old-secret",
		AdminPassword: "old-admin-secret",
		DeviceName:    "mac-a",
		AutoConnect:   false,
		UDPListen:     ":0",
		AdminListen:   "127.0.0.1:19090",
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
	}); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	newPassword := "new-secret"
	newAdminPassword := "new-admin-secret"
	newAdmin := "127.0.0.1:19091"
	autoConnect := true
	allowInsecureHTTP := true
	got, err := ApplyClientConfigPatch(path, ClientConfigPatch{
		AllowInsecureHTTP: &allowInsecureHTTP,
		Password:          &newPassword,
		AdminPassword:     &newAdminPassword,
		AutoConnect:       &autoConnect,
		AdminListen:       &newAdmin,
		UpsertPublish:     []string{"game=127.0.0.1:19133"},
		DeletePublish:     []string{"echo"},
		UpsertBind:        []string{"game-b=win-b,game,127.0.0.1:29133"},
		DeleteBind:        []string{"echo-b"},
	})
	if err != nil {
		t.Fatalf("apply config patch: %v", err)
	}

	if got.Password != newPassword || got.AdminPassword != newAdminPassword || got.AdminListen != newAdmin || got.AutoConnect != autoConnect || !got.AllowInsecureHTTP {
		t.Fatalf("unexpected patched scalar values: %+v", got)
	}
	if got.IdentityPrivate == "" {
		t.Fatalf("expected identity_private to be generated: %+v", got)
	}
	if _, ok := got.Publish["echo"]; ok {
		t.Fatalf("expected publish echo to be deleted: %+v", got.Publish)
	}
	if got.Publish["game"].Local != "127.0.0.1:19133" {
		t.Fatalf("unexpected patched publish entry: %+v", got.Publish)
	}
	if _, ok := got.Binds["echo-b"]; ok {
		t.Fatalf("expected bind echo-b to be deleted: %+v", got.Binds)
	}
	if got.Binds["game-b"].Peer != "win-b" || got.Binds["game-b"].Service != "game" || got.Binds["game-b"].Local != "127.0.0.1:29133" {
		t.Fatalf("unexpected patched bind entry: %+v", got.Binds)
	}
}

func TestApplyClientConfigPatchRejectsInvalidAdminListen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	if err := SaveClientConfig(path, ClientConfig{
		ServerURL:     "http://127.0.0.1:8080",
		Password:      "secret",
		AdminPassword: "admin-secret",
		DeviceName:    "mac-a",
		AutoConnect:   false,
		UDPListen:     ":0",
		AdminListen:   "127.0.0.1:19090",
	}); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	badAdmin := "0.0.0.0:19090"
	_, err := ApplyClientConfigPatch(path, ClientConfigPatch{AdminListen: &badAdmin})
	if err == nil {
		t.Fatal("expected invalid admin listen to fail")
	}
}
