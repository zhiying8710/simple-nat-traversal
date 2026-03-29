package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
)

func TestRuntimeManagerStartStop(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.Password = "secret-password"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	started := make(chan struct{})
	manager := NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	if _, err := manager.Start(path); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not start")
	}

	status := manager.Snapshot()
	if status.State != "running" {
		t.Fatalf("unexpected state after start: %+v", status)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := manager.Stop(stopCtx)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if status.State != "stopped" {
		t.Fatalf("unexpected state after stop: %+v", status)
	}
}

func TestRuntimeManagerCapturesFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "client.json")
	cfg := config.ClientDefaults()
	cfg.Password = "secret-password"
	if err := config.SaveClientConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	manager := NewRuntimeManagerForTest(func(ctx context.Context, cfg config.ClientConfig) error {
		return errors.New("boom")
	})

	if _, err := manager.Start(path); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := manager.Snapshot()
		if status.State == "stopped" {
			if status.LastError != "boom" {
				t.Fatalf("unexpected last error: %+v", status)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("runtime did not stop after failure")
}

func TestLogBufferSnapshot(t *testing.T) {
	t.Parallel()

	buffer := NewLogBuffer(2)
	if _, err := buffer.Write([]byte("first\nsecond\nthird\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := buffer.Snapshot()
	if len(got) != 2 || got[0] != "second" || got[1] != "third" {
		t.Fatalf("unexpected log snapshot: %+v", got)
	}
}
