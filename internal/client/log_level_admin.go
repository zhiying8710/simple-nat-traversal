package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

func SetRuntimeLogLevel(ctx context.Context, cfg config.ClientConfig, level string) (proto.LogLevelResponse, error) {
	if cfg.AdminListen == "" {
		return proto.LogLevelResponse{}, fmt.Errorf("client config missing admin_listen")
	}
	normalized, err := config.NormalizeLogLevel(level)
	if err != nil {
		return proto.LogLevelResponse{}, err
	}

	u, err := adminURLFromListen(cfg.AdminListen, "/log-level")
	if err != nil {
		return proto.LogLevelResponse{}, err
	}
	raw, err := json.Marshal(proto.LogLevelUpdateRequest{LogLevel: normalized})
	if err != nil {
		return proto.LogLevelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return proto.LogLevelResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return proto.LogLevelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return proto.LogLevelResponse{}, fmt.Errorf("set runtime log level failed: %s", strings.TrimSpace(firstNonEmpty(string(body), resp.Status)))
	}

	var out proto.LogLevelResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return proto.LogLevelResponse{}, err
	}
	return out, nil
}
