package config

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"simple-nat-traversal/internal/secure"
)

type ServerConfig struct {
	HTTPListen    string `json:"http_listen"`
	UDPListen     string `json:"udp_listen"`
	PublicUDPAddr string `json:"public_udp_addr"`
	Password      string `json:"password"`
	AdminPassword string `json:"admin_password,omitempty"`
	LogLevel      string `json:"log_level,omitempty"`
	StatePath     string `json:"state_path,omitempty"`
	TLSCertFile   string `json:"tls_cert_file"`
	TLSKeyFile    string `json:"tls_key_file"`
}

type ClientConfig struct {
	ServerURL         string                   `json:"server_url"`
	AllowInsecureHTTP bool                     `json:"allow_insecure_http,omitempty"`
	Password          string                   `json:"password"`
	AdminPassword     string                   `json:"admin_password,omitempty"`
	DeviceName        string                   `json:"device_name"`
	IdentityPrivate   string                   `json:"identity_private,omitempty"`
	AutoConnect       bool                     `json:"auto_connect"`
	UDPListen         string                   `json:"udp_listen"`
	AdminListen       string                   `json:"admin_listen"`
	LogLevel          string                   `json:"log_level,omitempty"`
	Publish           map[string]PublishConfig `json:"publish"`
	Binds             map[string]BindConfig    `json:"binds"`
}

const (
	ServiceProtocolUDP = "udp"
	ServiceProtocolTCP = "tcp"
	LogLevelDebug      = "debug"
	LogLevelInfo       = "info"
	LogLevelWarn       = "warn"
	LogLevelError      = "error"
)

type PublishConfig struct {
	Protocol string `json:"protocol,omitempty"`
	Local    string `json:"local"`
}

type BindConfig struct {
	Protocol string `json:"protocol,omitempty"`
	Peer     string `json:"peer"`
	Service  string `json:"service"`
	Local    string `json:"local"`
}

func LoadServerConfig(path string) (ServerConfig, error) {
	var cfg ServerConfig
	if err := loadJSON(path, &cfg); err != nil {
		return ServerConfig{}, err
	}
	if err := normalizeServerConfig(&cfg); err != nil {
		return ServerConfig{}, err
	}
	if strings.TrimSpace(cfg.StatePath) == "" {
		cfg.StatePath = defaultServerStatePath(path)
	}
	return cfg, nil
}

func SaveServerConfig(path string, cfg ServerConfig) error {
	if err := normalizeServerConfig(&cfg); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func LoadClientConfig(path string) (ClientConfig, error) {
	var cfg ClientConfig
	if err := loadJSON(path, &cfg); err != nil {
		return ClientConfig{}, err
	}
	return cfg, normalizeClientConfig(&cfg)
}

func SaveClientConfig(path string, cfg ClientConfig) error {
	if _, changed, err := EnsureClientIdentity(&cfg); err != nil {
		return err
	} else if changed {
		// identity_private is persisted with the config so device identity survives restarts.
	}
	if err := normalizeClientConfig(&cfg); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func ClientDefaults() ClientConfig {
	return ClientConfig{
		ServerURL:         "https://YOUR_VPS_PUBLIC_IP",
		AllowInsecureHTTP: false,
		Password:          "",
		AdminPassword:     "",
		DeviceName:        "device-name",
		IdentityPrivate:   "",
		AutoConnect:       false,
		UDPListen:         ":0",
		AdminListen:       "127.0.0.1:19090",
		LogLevel:          LogLevelInfo,
		Publish:           map[string]PublishConfig{},
		Binds:             map[string]BindConfig{},
	}
}

func DefaultGUIClientConfigPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		return "client.json"
	}
	return filepath.Join(configDir, "simple-nat-traversal", "client.json")
}

func normalizeServerConfig(cfg *ServerConfig) error {
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = ":8080"
	}
	if cfg.UDPListen == "" {
		cfg.UDPListen = ":3479"
	}
	if cfg.PublicUDPAddr == "" {
		cfg.PublicUDPAddr = cfg.UDPListen
	}
	if cfg.Password == "" {
		return errors.New("password is required")
	}
	logLevel, err := NormalizeLogLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("log_level: %w", err)
	}
	cfg.LogLevel = logLevel
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return errors.New("tls_cert_file and tls_key_file must be provided together")
	}
	return nil
}

func defaultServerStatePath(configPath string) string {
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		name = base
	}
	if name == "" {
		name = "server"
	}
	return filepath.Join(filepath.Dir(configPath), name+".state.json")
}

func normalizeClientConfig(cfg *ClientConfig) error {
	if cfg.ServerURL == "" {
		return errors.New("server_url is required")
	}
	normalizedServerURL, err := ValidateServerURL(cfg.ServerURL, cfg.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	if cfg.Password == "" {
		return errors.New("password is required")
	}
	if cfg.DeviceName == "" {
		return errors.New("device_name is required")
	}
	if strings.TrimSpace(cfg.IdentityPrivate) != "" {
		if _, _, err := secure.ParseIdentityPrivate(cfg.IdentityPrivate); err != nil {
			return fmt.Errorf("identity_private is invalid: %w", err)
		}
	}
	if cfg.UDPListen == "" {
		cfg.UDPListen = ":0"
	}
	cfg.ServerURL = normalizedServerURL
	if err := ValidateAdminListen(cfg.AdminListen); err != nil {
		return err
	}
	logLevel, err := NormalizeLogLevel(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("log_level: %w", err)
	}
	cfg.LogLevel = logLevel
	if cfg.Publish == nil {
		cfg.Publish = map[string]PublishConfig{}
	}
	if cfg.Binds == nil {
		cfg.Binds = map[string]BindConfig{}
	}
	for name, publish := range cfg.Publish {
		protocol, err := NormalizeServiceProtocol(publish.Protocol)
		if err != nil {
			return fmt.Errorf("publish.%s.protocol: %w", name, err)
		}
		if publish.Local == "" {
			return fmt.Errorf("publish.%s.local is required", name)
		}
		publish.Protocol = protocol
		cfg.Publish[name] = publish
	}
	for name, bind := range cfg.Binds {
		protocol, err := NormalizeServiceProtocol(bind.Protocol)
		if err != nil {
			return fmt.Errorf("binds.%s.protocol: %w", name, err)
		}
		if bind.Peer == "" {
			return fmt.Errorf("binds.%s.peer is required", name)
		}
		if bind.Service == "" {
			return fmt.Errorf("binds.%s.service is required", name)
		}
		if bind.Local == "" {
			return fmt.Errorf("binds.%s.local is required", name)
		}
		bind.Protocol = protocol
		cfg.Binds[name] = bind
	}
	return nil
}

func ValidateClientConfig(cfg *ClientConfig) error {
	return normalizeClientConfig(cfg)
}

func NormalizeServiceProtocol(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ServiceProtocolUDP:
		return ServiceProtocolUDP, nil
	case ServiceProtocolTCP:
		return ServiceProtocolTCP, nil
	default:
		return "", fmt.Errorf("must be %q or %q", ServiceProtocolUDP, ServiceProtocolTCP)
	}
}

func NormalizeLogLevel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", LogLevelInfo:
		return LogLevelInfo, nil
	case LogLevelDebug:
		return LogLevelDebug, nil
	case LogLevelWarn:
		return LogLevelWarn, nil
	case LogLevelError:
		return LogLevelError, nil
	default:
		return "", fmt.Errorf("must be %q, %q, %q or %q", LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError)
	}
}

func ValidateServerURL(raw string, allowInsecureHTTP bool) (string, error) {
	normalized := strings.TrimRight(strings.TrimSpace(raw), "/")
	if normalized == "" {
		return "", errors.New("server_url is required")
	}

	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", fmt.Errorf("invalid server_url: %w", err)
	}
	if strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
		return "", errors.New("server_url must be an absolute http/https URL")
	}

	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return normalized, nil
	case "http":
		if allowInsecureHTTP || isLoopbackHost(parsed.Hostname()) {
			return normalized, nil
		}
		return "", errors.New("server_url must use https unless allow_insecure_http is true or the host is loopback")
	default:
		return "", errors.New("server_url must use http or https")
	}
}

func EnsureClientIdentity(cfg *ClientConfig) (ed25519.PublicKey, bool, error) {
	if cfg == nil {
		return nil, false, errors.New("client config is nil")
	}
	if strings.TrimSpace(cfg.IdentityPrivate) != "" {
		publicKey, _, err := secure.ParseIdentityPrivate(cfg.IdentityPrivate)
		if err != nil {
			return nil, false, fmt.Errorf("parse identity_private: %w", err)
		}
		return publicKey, false, nil
	}

	publicKey, privateKey, err := secure.NewIdentityKey()
	if err != nil {
		return nil, false, fmt.Errorf("create identity key: %w", err)
	}
	encoded, err := secure.EncodeIdentityPrivate(privateKey)
	if err != nil {
		return nil, false, fmt.Errorf("encode identity key: %w", err)
	}
	cfg.IdentityPrivate = encoded
	return publicKey, true, nil
}

func ValidateAdminListen(addr string) error {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid admin_listen: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("admin_listen must bind to localhost/loopback only")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loadJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
