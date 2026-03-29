package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

func SetRuntimeLogLevel(ctx context.Context, cfg config.ServerConfig, level string) (proto.LogLevelResponse, error) {
	normalized, err := config.NormalizeLogLevel(level)
	if err != nil {
		return proto.LogLevelResponse{}, err
	}

	u, err := serverAdminURL(cfg, "/v1/admin/log-level")
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
	if strings.TrimSpace(cfg.AdminPassword) != "" {
		req.Header.Set("X-SNT-Admin-Password", cfg.AdminPassword)
	}

	client := http.DefaultClient
	if strings.TrimSpace(cfg.TLSCertFile) != "" {
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	resp, err := client.Do(req)
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

func serverAdminURL(cfg config.ServerConfig, path string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(cfg.HTTPListen))
	if err != nil {
		return "", fmt.Errorf("invalid http_listen: %w", err)
	}
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	scheme := "http"
	if strings.TrimSpace(cfg.TLSCertFile) != "" {
		scheme = "https"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, port),
		Path:   path,
	}
	return u.String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
