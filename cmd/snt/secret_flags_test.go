package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOptionalSecretFromEnv(t *testing.T) {
	const envName = "SNT_TEST_PASSWORD"
	t.Setenv(envName, "env-secret")

	got, err := resolveOptionalSecret(envName, "", "password")
	if err != nil {
		t.Fatalf("resolveOptionalSecret: %v", err)
	}
	if got == nil || *got != "env-secret" {
		t.Fatalf("unexpected env secret: %+v", got)
	}
}

func TestResolveOptionalSecretFromFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "password.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := resolveOptionalSecret("", path, "password")
	if err != nil {
		t.Fatalf("resolveOptionalSecret: %v", err)
	}
	if got == nil || *got != "file-secret" {
		t.Fatalf("unexpected file secret: %+v", got)
	}
}

func TestResolveOptionalSecretRejectsConflictingSources(t *testing.T) {
	const envName = "SNT_TEST_ADMIN_PASSWORD"
	t.Setenv(envName, "env-secret")
	path := filepath.Join(t.TempDir(), "password.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := resolveOptionalSecret(envName, path, "admin-password"); err == nil {
		t.Fatal("expected conflicting secret sources to fail")
	}
}

func TestResolveOptionalSecretRejectsMissingEnv(t *testing.T) {
	if _, err := resolveOptionalSecret("SNT_TEST_MISSING_ENV", "", "password"); err == nil {
		t.Fatal("expected missing env secret to fail")
	}
}
