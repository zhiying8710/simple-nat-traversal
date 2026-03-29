package client

import (
	"context"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
)

func TestSetRuntimeLogLevel(t *testing.T) {
	previous := logx.CurrentLevel()
	defer func() {
		_, _ = logx.SetLevel(previous)
	}()
	_, _ = logx.SetLevel(config.LogLevelInfo)

	c := &Client{
		cfg: config.ClientConfig{
			AdminListen: "127.0.0.1:0",
			LogLevel:    config.LogLevelInfo,
		},
	}
	if err := c.startAdminServer(); err != nil {
		t.Fatalf("startAdminServer: %v", err)
	}
	defer func() {
		if c.adminServer != nil {
			_ = c.adminServer.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := SetRuntimeLogLevel(ctx, config.ClientConfig{AdminListen: c.adminAddr}, config.LogLevelDebug)
	if err != nil {
		t.Fatalf("SetRuntimeLogLevel: %v", err)
	}
	if resp.LogLevel != config.LogLevelDebug {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := c.snapshotStatus().LogLevel; got != config.LogLevelDebug {
		t.Fatalf("unexpected runtime log level: %s", got)
	}
}
