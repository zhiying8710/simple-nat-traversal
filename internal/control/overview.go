package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

type OverviewOptions struct {
	IncludeNetwork bool
}

type OverviewConfig struct {
	ServerURL               string                          `json:"server_url"`
	AllowInsecureHTTP       bool                            `json:"allow_insecure_http"`
	DeviceName              string                          `json:"device_name"`
	AutoConnect             bool                            `json:"auto_connect"`
	UDPListen               string                          `json:"udp_listen"`
	AdminListen             string                          `json:"admin_listen"`
	LogLevel                string                          `json:"log_level"`
	PasswordConfigured      bool                            `json:"password_configured"`
	AdminPasswordConfigured bool                            `json:"admin_password_configured"`
	IdentityConfigured      bool                            `json:"identity_configured"`
	Publish                 map[string]config.PublishConfig `json:"publish,omitempty"`
	Binds                   map[string]config.BindConfig    `json:"binds,omitempty"`
}

type Overview struct {
	GeneratedAt    time.Time                     `json:"generated_at"`
	ExecutablePath string                        `json:"executable_path"`
	ConfigPath     string                        `json:"config_path"`
	ConfigExists   bool                          `json:"config_exists"`
	ConfigValid    bool                          `json:"config_valid"`
	ConfigError    string                        `json:"config_error,omitempty"`
	ClientRunning  bool                          `json:"client_running"`
	StatusError    string                        `json:"status_error,omitempty"`
	NetworkError   string                        `json:"network_error,omitempty"`
	Autostart      autostart.Status              `json:"autostart"`
	AutostartError string                        `json:"autostart_error,omitempty"`
	Config         *OverviewConfig               `json:"config,omitempty"`
	Status         *client.StatusSnapshot        `json:"status,omitempty"`
	Network        *proto.NetworkDevicesResponse `json:"network,omitempty"`
}

func LoadOverview(ctx context.Context, executablePath, configPath string, opts OverviewOptions) (Overview, error) {
	execAbs, err := filepath.Abs(executablePath)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve executable path: %w", err)
	}
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve config path: %w", err)
	}

	out := Overview{
		GeneratedAt:    time.Now(),
		ExecutablePath: execAbs,
		ConfigPath:     configAbs,
	}

	if _, err := os.Stat(configAbs); err == nil {
		out.ConfigExists = true
	} else if !os.IsNotExist(err) {
		out.ConfigError = err.Error()
		return out, nil
	}

	if status, err := autostart.StatusFor(execAbs, configAbs); err == nil {
		out.Autostart = status
	} else {
		out.AutostartError = err.Error()
	}

	if !out.ConfigExists {
		out.ConfigError = "config file does not exist"
		return out, nil
	}

	cfg, err := config.LoadClientConfig(configAbs)
	if err != nil {
		out.ConfigError = err.Error()
		return out, nil
	}
	return populateOverview(ctx, out, cfg, nil, opts), nil
}

func LoadOverviewForConfig(ctx context.Context, executablePath, configPath string, cfg config.ClientConfig, configExists bool, configErr error, opts OverviewOptions) (Overview, error) {
	execAbs, err := filepath.Abs(executablePath)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve executable path: %w", err)
	}
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve config path: %w", err)
	}

	out := Overview{
		GeneratedAt:    time.Now(),
		ExecutablePath: execAbs,
		ConfigPath:     configAbs,
		ConfigExists:   configExists,
	}

	if status, err := autostart.StatusFor(execAbs, configAbs); err == nil {
		out.Autostart = status
	} else {
		out.AutostartError = err.Error()
	}

	return populateOverview(ctx, out, cfg, configErr, opts), nil
}

func populateOverview(ctx context.Context, out Overview, cfg config.ClientConfig, configErr error, opts OverviewOptions) Overview {
	redactedCfg := redactOverviewConfig(cfg)
	out.Config = &redactedCfg

	if configErr != nil {
		out.ConfigError = configErr.Error()
		return out
	}

	cfgCopy := cfg
	if err := config.ValidateClientConfig(&cfgCopy); err != nil {
		out.ConfigError = err.Error()
		return out
	}

	out.ConfigValid = true
	redactedCfg = redactOverviewConfig(cfgCopy)
	out.Config = &redactedCfg

	statusCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	status, err := client.FetchStatus(statusCtx, cfgCopy)
	if err == nil {
		out.ClientRunning = true
		out.Status = &status
	} else {
		out.StatusError = err.Error()
	}

	if opts.IncludeNetwork {
		networkCtx, networkCancel := context.WithTimeout(ctx, 2*time.Second)
		defer networkCancel()
		network, err := client.FetchNetworkDevices(networkCtx, cfgCopy)
		if err == nil {
			out.Network = &network
		} else {
			out.NetworkError = err.Error()
		}
	}

	return out
}

func redactOverviewConfig(cfg config.ClientConfig) OverviewConfig {
	return OverviewConfig{
		ServerURL:               cfg.ServerURL,
		AllowInsecureHTTP:       cfg.AllowInsecureHTTP,
		DeviceName:              cfg.DeviceName,
		AutoConnect:             cfg.AutoConnect,
		UDPListen:               cfg.UDPListen,
		AdminListen:             cfg.AdminListen,
		LogLevel:                cfg.LogLevel,
		PasswordConfigured:      strings.TrimSpace(cfg.Password) != "",
		AdminPasswordConfigured: strings.TrimSpace(cfg.AdminPassword) != "",
		IdentityConfigured:      strings.TrimSpace(cfg.IdentityPrivate) != "",
		Publish:                 clonePublishConfigs(cfg.Publish),
		Binds:                   cloneBindConfigs(cfg.Binds),
	}
}

func clonePublishConfigs(in map[string]config.PublishConfig) map[string]config.PublishConfig {
	if len(in) == 0 {
		return map[string]config.PublishConfig{}
	}
	out := make(map[string]config.PublishConfig, len(in))
	for name, publish := range in {
		out[name] = publish
	}
	return out
}

func cloneBindConfigs(in map[string]config.BindConfig) map[string]config.BindConfig {
	if len(in) == 0 {
		return map[string]config.BindConfig{}
	}
	out := make(map[string]config.BindConfig, len(in))
	for name, bind := range in {
		out[name] = bind
	}
	return out
}

func RenderOverview(overview Overview) string {
	var out strings.Builder

	fmt.Fprintf(&out, "generated_at\t%s\n", overview.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&out, "executable\t%s\n", dash(overview.ExecutablePath))
	fmt.Fprintf(&out, "config\t%s\n", dash(overview.ConfigPath))
	fmt.Fprintf(&out, "config_exists\t%s\n", yesNo(overview.ConfigExists))
	fmt.Fprintf(&out, "config_valid\t%s\n", yesNo(overview.ConfigValid))
	fmt.Fprintf(&out, "config_error\t%s\n", dash(overview.ConfigError))
	fmt.Fprintf(&out, "client_running\t%s\n", yesNo(overview.ClientRunning))
	fmt.Fprintf(&out, "status_error\t%s\n", dash(overview.StatusError))
	fmt.Fprintf(&out, "network_error\t%s\n", dash(overview.NetworkError))
	fmt.Fprintf(&out, "autostart_installed\t%s\n", yesNo(overview.Autostart.Installed))
	fmt.Fprintf(&out, "autostart_file\t%s\n", dash(overview.Autostart.FilePath))
	fmt.Fprintf(&out, "autostart_error\t%s\n", dash(overview.AutostartError))

	if overview.Config != nil {
		fmt.Fprintf(&out, "device_name\t%s\n", dash(overview.Config.DeviceName))
		fmt.Fprintf(&out, "server_url\t%s\n", dash(overview.Config.ServerURL))
		fmt.Fprintf(&out, "allow_insecure_http\t%s\n", yesNo(overview.Config.AllowInsecureHTTP))
		fmt.Fprintf(&out, "udp_listen\t%s\n", dash(overview.Config.UDPListen))
		fmt.Fprintf(&out, "admin_listen\t%s\n", dash(overview.Config.AdminListen))
		fmt.Fprintf(&out, "log_level\t%s\n", dash(overview.Config.LogLevel))
		fmt.Fprintf(&out, "publish_count\t%d\n", len(overview.Config.Publish))
		fmt.Fprintf(&out, "bind_count\t%d\n", len(overview.Config.Binds))
	}
	if overview.Status != nil {
		fmt.Fprintf(&out, "public_udp\t%s\n", dash(overview.Status.ObservedAddr))
		fmt.Fprintf(&out, "network_state\t%s\n", dash(overview.Status.NetworkState))
		fmt.Fprintf(&out, "peer_count\t%d\n", len(overview.Status.Peers))
		fmt.Fprintf(&out, "rejoin_count\t%d\n", overview.Status.RejoinCount)
	}
	if overview.Network != nil {
		fmt.Fprintf(&out, "network_devices\t%d\n", len(overview.Network.Devices))
	}
	return out.String()
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
