package config

import (
	"path/filepath"
	"testing"
)

func TestNormalizeLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default", input: "", want: LogLevelInfo},
		{name: "debug", input: "debug", want: LogLevelDebug},
		{name: "info uppercase", input: "INFO", want: LogLevelInfo},
		{name: "warn", input: "warn", want: LogLevelWarn},
		{name: "error", input: "error", want: LogLevelError},
		{name: "invalid", input: "trace", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeLogLevel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeLogLevel(%q) expected error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeLogLevel(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeLogLevel(%q)=%q want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSaveAndLoadServerConfigPreservesLogLevel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server.json")
	want := ServerConfig{
		HTTPListen:    ":8080",
		UDPListen:     ":3479",
		PublicUDPAddr: "1.2.3.4:3479",
		Password:      "server-password-1234",
		AdminPassword: "server-admin-1234",
		LogLevel:      LogLevelDebug,
	}
	if err := SaveServerConfig(path, want); err != nil {
		t.Fatalf("SaveServerConfig: %v", err)
	}

	got, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("LoadServerConfig: %v", err)
	}
	if got.LogLevel != want.LogLevel {
		t.Fatalf("unexpected log level: got=%q want=%q", got.LogLevel, want.LogLevel)
	}
	if got.Password != want.Password || got.AdminPassword != want.AdminPassword {
		t.Fatalf("unexpected saved config: %+v", got)
	}
}
