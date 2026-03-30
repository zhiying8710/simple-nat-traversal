package desktopapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

const (
	refreshTimeout = 4 * time.Second
	actionTimeout  = 3 * time.Second
)

type Dependencies struct {
	ExecutablePath     string
	ConfigPath         string
	RuntimeManager     *control.RuntimeManager
	Logs               *control.LogBuffer
	InstallAutostart   func(executablePath, configPath string) (autostart.Status, error)
	UninstallAutostart func() (autostart.Status, error)
	KickNetworkDevice  func(context.Context, config.ClientConfig, proto.KickDeviceRequest) (proto.KickDeviceResponse, error)
	SetRuntimeLogLevel func(context.Context, config.ClientConfig, string) (proto.LogLevelResponse, error)
	LoadOverview       func(context.Context, string, string, config.ClientConfig, bool, error, control.OverviewOptions) (control.Overview, error)
}

type EditableConfig struct {
	ServerURL               string                          `json:"serverURL"`
	AllowInsecureHTTP       bool                            `json:"allowInsecureHTTP"`
	Password                string                          `json:"password,omitempty"`
	ClearPassword           bool                            `json:"clearPassword,omitempty"`
	PasswordConfigured      bool                            `json:"passwordConfigured"`
	AdminPassword           string                          `json:"adminPassword,omitempty"`
	ClearAdminPassword      bool                            `json:"clearAdminPassword,omitempty"`
	AdminPasswordConfigured bool                            `json:"adminPasswordConfigured"`
	DeviceName              string                          `json:"deviceName"`
	AutoConnect             bool                            `json:"autoConnect"`
	UDPListen               string                          `json:"udpListen"`
	AdminListen             string                          `json:"adminListen"`
	LogLevel                string                          `json:"logLevel"`
	Publish                 map[string]config.PublishConfig `json:"publish"`
	Binds                   map[string]config.BindConfig    `json:"binds"`
}

type LoadedConfig struct {
	Exists            bool           `json:"exists"`
	DefaultDeviceName string         `json:"defaultDeviceName"`
	ConfigPath        string         `json:"configPath"`
	Config            EditableConfig `json:"config"`
}

type DiscoveredService struct {
	DeviceID    string `json:"deviceID"`
	DeviceName  string `json:"deviceName"`
	ServiceName string `json:"serviceName"`
	Protocol    string `json:"protocol"`
}

type AppState struct {
	GeneratedAt   time.Time             `json:"generatedAt"`
	Config        LoadedConfig          `json:"config"`
	Overview      control.Overview      `json:"overview"`
	RuntimeStatus control.RuntimeStatus `json:"runtimeStatus"`
	Logs          []string              `json:"logs"`
	Discovered    []DiscoveredService   `json:"discovered"`
	StatusJSON    string                `json:"statusJSON"`
	OverviewJSON  string                `json:"overviewJSON"`
	NetworkJSON   string                `json:"networkJSON"`
}

type ActionResult struct {
	Message string   `json:"message"`
	State   AppState `json:"state"`
}

type Service struct {
	deps              Dependencies
	defaultDeviceName string
}

func New(deps Dependencies) *Service {
	if deps.RuntimeManager == nil {
		deps.RuntimeManager = control.NewRuntimeManager()
	}
	if deps.Logs == nil {
		deps.Logs = control.NewLogBuffer(500)
	}
	if deps.InstallAutostart == nil {
		deps.InstallAutostart = autostart.Install
	}
	if deps.UninstallAutostart == nil {
		deps.UninstallAutostart = autostart.Uninstall
	}
	if deps.KickNetworkDevice == nil {
		deps.KickNetworkDevice = client.KickNetworkDevice
	}
	if deps.SetRuntimeLogLevel == nil {
		deps.SetRuntimeLogLevel = client.SetRuntimeLogLevel
	}
	if deps.LoadOverview == nil {
		deps.LoadOverview = control.LoadOverviewForConfig
	}
	return &Service{
		deps:              deps,
		defaultDeviceName: config.SuggestedDeviceName(),
	}
}

func (s *Service) State() (AppState, error) {
	loaded, err := s.loadEditableConfig()
	if err != nil {
		return AppState{}, err
	}
	return s.stateFromEditable(loaded.Config, loaded)
}

func (s *Service) AutoStart() error {
	cfg, err := config.LoadClientConfig(s.deps.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !cfg.AutoConnect {
		return nil
	}
	_, err = s.deps.RuntimeManager.Start(s.deps.ConfigPath)
	return err
}

func (s *Service) Refresh(input EditableConfig) (AppState, error) {
	loaded, cfg, err := s.draftFromEditable(input)
	return s.buildState(loaded, cfg, err)
}

func (s *Service) SaveConfig(input EditableConfig) (ActionResult, error) {
	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := config.SaveClientConfig(s.deps.ConfigPath, cfg); err != nil {
		return ActionResult{}, err
	}
	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: "配置已保存",
		State:   state,
	}, nil
}

func (s *Service) StartClient(input EditableConfig) (ActionResult, error) {
	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := config.SaveClientConfig(s.deps.ConfigPath, cfg); err != nil {
		return ActionResult{}, err
	}
	status, err := s.deps.RuntimeManager.Start(s.deps.ConfigPath)
	if err != nil {
		return ActionResult{}, err
	}
	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: fmt.Sprintf("客户端状态: %s", status.State),
		State:   state,
	}, nil
}

func (s *Service) StopClient() (ActionResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	status, err := s.deps.RuntimeManager.Stop(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: fmt.Sprintf("客户端状态: %s", status.State),
		State:   state,
	}, nil
}

func (s *Service) ApplyLogLevel(input EditableConfig) (ActionResult, error) {
	level, err := config.NormalizeLogLevel(strings.TrimSpace(input.LogLevel))
	if err != nil {
		return ActionResult{}, err
	}
	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}
	cfg.LogLevel = level
	if err := config.SaveClientConfig(s.deps.ConfigPath, cfg); err != nil {
		return ActionResult{}, err
	}

	message := fmt.Sprintf("日志级别已保存: %s", level)
	if s.deps.RuntimeManager.Snapshot().State == "running" {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		resp, err := s.deps.SetRuntimeLogLevel(ctx, cfg, level)
		if err != nil {
			return ActionResult{}, err
		}
		message = fmt.Sprintf("日志级别已应用: %s", resp.LogLevel)
	}

	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: message,
		State:   state,
	}, nil
}

func (s *Service) InstallAutostart(input EditableConfig) (ActionResult, error) {
	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := config.SaveClientConfig(s.deps.ConfigPath, cfg); err != nil {
		return ActionResult{}, err
	}
	status, err := s.deps.InstallAutostart(s.executablePath(), s.deps.ConfigPath)
	if err != nil {
		return ActionResult{}, err
	}
	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: fmt.Sprintf("开机启动已安装: %t", status.Installed),
		State:   state,
	}, nil
}

func (s *Service) UninstallAutostart() (ActionResult, error) {
	status, err := s.deps.UninstallAutostart()
	if err != nil {
		return ActionResult{}, err
	}
	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: fmt.Sprintf("开机启动已安装: %t", status.Installed),
		State:   state,
	}, nil
}

func (s *Service) UpsertPublish(input EditableConfig, name, protocol, local string) (ActionResult, error) {
	key := sanitizeConfigKey(name)
	local = strings.TrimSpace(local)
	if key == "" || local == "" {
		return ActionResult{}, errors.New("服务名和本地地址不能为空")
	}
	normalizedProtocol, err := config.NormalizeServiceProtocol(protocol)
	if err != nil {
		return ActionResult{}, err
	}

	next := cloneEditableConfig(input)
	next.Publish[key] = config.PublishConfig{
		Protocol: normalizedProtocol,
		Local:    local,
	}
	return s.persistServiceDrafts(next, "服务配置已更新")
}

func (s *Service) DeletePublish(input EditableConfig, name string) (ActionResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ActionResult{}, errors.New("请选择要删除的发布服务")
	}
	next := cloneEditableConfig(input)
	delete(next.Publish, name)
	return s.persistServiceDrafts(next, "服务配置已删除")
}

func (s *Service) UpsertBind(input EditableConfig, name, protocol, peer, serviceName, local string) (ActionResult, error) {
	key := sanitizeConfigKey(name)
	peer = strings.TrimSpace(peer)
	serviceName = strings.TrimSpace(serviceName)
	local = strings.TrimSpace(local)
	if key == "" || peer == "" || serviceName == "" || local == "" {
		return ActionResult{}, errors.New("绑定名、远端设备、远端服务和本地地址不能为空")
	}
	normalizedProtocol, err := config.NormalizeServiceProtocol(protocol)
	if err != nil {
		return ActionResult{}, err
	}

	next := cloneEditableConfig(input)
	next.Binds[key] = config.BindConfig{
		Protocol: normalizedProtocol,
		Peer:     peer,
		Service:  serviceName,
		Local:    local,
	}
	return s.persistServiceDrafts(next, "服务配置已更新")
}

func (s *Service) DeleteBind(input EditableConfig, name string) (ActionResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ActionResult{}, errors.New("请选择要删除的绑定服务")
	}
	next := cloneEditableConfig(input)
	delete(next.Binds, name)
	return s.persistServiceDrafts(next, "服务配置已删除")
}

func (s *Service) QuickBindDiscovered(input EditableConfig, service DiscoveredService) (ActionResult, error) {
	deviceName := strings.TrimSpace(service.DeviceName)
	serviceName := strings.TrimSpace(service.ServiceName)
	if deviceName == "" || serviceName == "" {
		return ActionResult{}, errors.New("请选择可绑定的远端服务")
	}

	next := cloneEditableConfig(input)
	baseName := deviceName + "-" + serviceName
	protocol, err := config.NormalizeServiceProtocol(service.Protocol)
	if err != nil {
		protocol = config.ServiceProtocolUDP
	}
	if protocol != config.ServiceProtocolUDP {
		baseName += "-" + protocol
	}
	key := uniqueConfigKey(sanitizeConfigKey(baseName), next.Binds)
	next.Binds[key] = config.BindConfig{
		Protocol: protocol,
		Peer:     deviceName,
		Service:  serviceName,
		Local:    "127.0.0.1:0",
	}
	return s.persistServiceDrafts(next, "配置已应用")
}

func (s *Service) KickDevice(input EditableConfig, deviceName, deviceID string) (ActionResult, error) {
	deviceName = strings.TrimSpace(deviceName)
	deviceID = strings.TrimSpace(deviceID)
	if deviceName == "" && deviceID == "" {
		return ActionResult{}, errors.New("设备名称和设备 ID 至少填写一个")
	}

	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	resp, err := s.deps.KickNetworkDevice(ctx, cfg, proto.KickDeviceRequest{
		DeviceName: deviceName,
		DeviceID:   deviceID,
	})
	if err != nil {
		return ActionResult{}, err
	}

	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Message: fmt.Sprintf("已踢出设备: %s (%s)", strings.TrimSpace(resp.DeviceName), strings.TrimSpace(resp.DeviceID)),
		State:   state,
	}, nil
}

func (s *Service) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	_, _ = s.deps.RuntimeManager.Stop(ctx)
}

func (s *Service) persistServiceDrafts(input EditableConfig, successMessage string) (ActionResult, error) {
	cfg, err := s.collectConfig(input)
	if err != nil {
		return ActionResult{}, err
	}
	if err := config.SaveClientConfig(s.deps.ConfigPath, cfg); err != nil {
		return ActionResult{}, err
	}

	restarted, err := s.restartClientIfRunning()
	if err != nil {
		return ActionResult{}, err
	}

	state, err := s.State()
	if err != nil {
		return ActionResult{}, err
	}

	if restarted {
		successMessage = "配置已保存，客户端已自动重启"
	}
	return ActionResult{
		Message: successMessage,
		State:   state,
	}, nil
}

func (s *Service) restartClientIfRunning() (bool, error) {
	if s.deps.RuntimeManager.Snapshot().State != "running" {
		return false, nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if _, err := s.deps.RuntimeManager.Stop(stopCtx); err != nil {
		return false, err
	}
	if _, err := s.deps.RuntimeManager.Start(s.deps.ConfigPath); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) collectConfig(input EditableConfig) (config.ClientConfig, error) {
	_, cfg, err := s.draftFromEditable(input)
	if err != nil {
		return config.ClientConfig{}, err
	}
	return cfg, nil
}

func (s *Service) stateFromEditable(input EditableConfig, loaded LoadedConfig) (AppState, error) {
	_, cfg, err := s.draftFromEditable(input)
	return s.buildState(loaded, cfg, err)
}

func (s *Service) buildState(loaded LoadedConfig, cfg config.ClientConfig, configErr error) (AppState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()

	overview, err := s.deps.LoadOverview(
		ctx,
		s.executablePath(),
		s.deps.ConfigPath,
		cfg,
		loaded.Exists,
		configErr,
		control.OverviewOptions{IncludeNetwork: true},
	)
	if err != nil {
		return AppState{}, err
	}

	logs := s.deps.Logs.Snapshot()
	discovered := buildDiscoveredServices(&overview, cfg.DeviceName)
	networkJSON := ""
	if overview.Network != nil {
		networkJSON = mustPrettyJSON(overview.Network)
	}

	return AppState{
		GeneratedAt:   time.Now(),
		Config:        loaded,
		Overview:      overview,
		RuntimeStatus: s.deps.RuntimeManager.Snapshot(),
		Logs:          logs,
		Discovered:    discovered,
		StatusJSON:    mustPrettyJSON(joinStatus(overview, s.deps.RuntimeManager.Snapshot())),
		OverviewJSON:  mustPrettyJSON(overview),
		NetworkJSON:   networkJSON,
	}, nil
}

type runtimeAndStatus struct {
	Runtime control.RuntimeStatus         `json:"runtime"`
	Status  *client.StatusSnapshot        `json:"status,omitempty"`
	Network *proto.NetworkDevicesResponse `json:"network,omitempty"`
}

func joinStatus(overview control.Overview, runtimeStatus control.RuntimeStatus) runtimeAndStatus {
	return runtimeAndStatus{
		Runtime: runtimeStatus,
		Status:  overview.Status,
		Network: overview.Network,
	}
}

func (s *Service) draftFromEditable(input EditableConfig) (LoadedConfig, config.ClientConfig, error) {
	existing, exists, err := loadConfigOrDefault(s.deps.ConfigPath, s.defaultDeviceName)
	if err != nil {
		return LoadedConfig{}, config.ClientConfig{}, err
	}

	editable := normalizeEditableConfig(input, existing)
	loaded := LoadedConfig{
		Exists:            exists,
		DefaultDeviceName: s.defaultDeviceName,
		ConfigPath:        s.deps.ConfigPath,
		Config:            editable,
	}

	cfg := existing
	if editable.ClearPassword && strings.TrimSpace(editable.Password) != "" {
		return loaded, cfg, errors.New("组网密码不能同时填写和清空")
	}
	if editable.ClearAdminPassword && strings.TrimSpace(editable.AdminPassword) != "" {
		return loaded, cfg, errors.New("管理密码不能同时填写和清空")
	}

	cfg.ServerURL = strings.TrimSpace(editable.ServerURL)
	cfg.AllowInsecureHTTP = editable.AllowInsecureHTTP
	cfg.DeviceName = strings.TrimSpace(editable.DeviceName)
	if cfg.DeviceName == "" {
		cfg.DeviceName = s.defaultDeviceName
	}
	cfg.AutoConnect = editable.AutoConnect
	cfg.UDPListen = strings.TrimSpace(editable.UDPListen)
	cfg.AdminListen = strings.TrimSpace(editable.AdminListen)
	cfg.LogLevel = strings.TrimSpace(editable.LogLevel)

	if editable.ClearPassword {
		cfg.Password = ""
	} else if strings.TrimSpace(editable.Password) != "" {
		cfg.Password = editable.Password
	}
	if editable.ClearAdminPassword {
		cfg.AdminPassword = ""
	} else if strings.TrimSpace(editable.AdminPassword) != "" {
		cfg.AdminPassword = editable.AdminPassword
	}

	cfg.Publish = clonePublishMap(editable.Publish)
	cfg.Binds = cloneBindMap(editable.Binds)

	if err := config.ValidateClientConfig(&cfg); err != nil {
		return loaded, cfg, err
	}

	loaded.Config = normalizeEditableConfig(loaded.Config, cfg)
	return loaded, cfg, nil
}

func (s *Service) loadEditableConfig() (LoadedConfig, error) {
	cfg, exists, err := loadConfigOrDefault(s.deps.ConfigPath, s.defaultDeviceName)
	if err != nil {
		return LoadedConfig{}, err
	}
	return LoadedConfig{
		Exists:            exists,
		DefaultDeviceName: s.defaultDeviceName,
		ConfigPath:        s.deps.ConfigPath,
		Config:            editableFromConfig(cfg),
	}, nil
}

func editableFromConfig(cfg config.ClientConfig) EditableConfig {
	return EditableConfig{
		ServerURL:               cfg.ServerURL,
		AllowInsecureHTTP:       cfg.AllowInsecureHTTP,
		PasswordConfigured:      strings.TrimSpace(cfg.Password) != "",
		AdminPasswordConfigured: strings.TrimSpace(cfg.AdminPassword) != "",
		DeviceName:              cfg.DeviceName,
		AutoConnect:             cfg.AutoConnect,
		UDPListen:               cfg.UDPListen,
		AdminListen:             cfg.AdminListen,
		LogLevel:                cfg.LogLevel,
		Publish:                 clonePublishMap(cfg.Publish),
		Binds:                   cloneBindMap(cfg.Binds),
	}
}

func normalizeEditableConfig(input EditableConfig, existing config.ClientConfig) EditableConfig {
	if isZeroEditableConfig(input) {
		return editableFromConfig(existing)
	}

	out := input
	if strings.TrimSpace(out.ServerURL) == "" {
		out.ServerURL = existing.ServerURL
	}
	if strings.TrimSpace(out.DeviceName) == "" {
		out.DeviceName = existing.DeviceName
	}
	if strings.TrimSpace(out.LogLevel) == "" {
		out.LogLevel = existing.LogLevel
	}
	out.PasswordConfigured = strings.TrimSpace(existing.Password) != ""
	out.AdminPasswordConfigured = strings.TrimSpace(existing.AdminPassword) != ""
	out.Publish = clonePublishMap(firstPublishMap(input.Publish, existing.Publish))
	out.Binds = cloneBindMap(firstBindMap(input.Binds, existing.Binds))
	return out
}

func isZeroEditableConfig(input EditableConfig) bool {
	return strings.TrimSpace(input.ServerURL) == "" &&
		!input.AllowInsecureHTTP &&
		strings.TrimSpace(input.Password) == "" &&
		!input.ClearPassword &&
		!input.PasswordConfigured &&
		strings.TrimSpace(input.AdminPassword) == "" &&
		!input.ClearAdminPassword &&
		!input.AdminPasswordConfigured &&
		strings.TrimSpace(input.DeviceName) == "" &&
		!input.AutoConnect &&
		strings.TrimSpace(input.UDPListen) == "" &&
		strings.TrimSpace(input.AdminListen) == "" &&
		strings.TrimSpace(input.LogLevel) == "" &&
		input.Publish == nil &&
		input.Binds == nil
}

func loadConfigOrDefault(path, defaultDeviceName string) (config.ClientConfig, bool, error) {
	cfg, err := config.LoadClientConfig(path)
	if err == nil {
		if strings.TrimSpace(cfg.DeviceName) == "" {
			cfg.DeviceName = defaultDeviceName
		}
		return cfg, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		cfg = config.ClientDefaults()
		cfg.DeviceName = defaultDeviceName
		return cfg, false, nil
	}
	return config.ClientConfig{}, false, err
}

func cloneEditableConfig(in EditableConfig) EditableConfig {
	out := in
	out.Publish = clonePublishMap(in.Publish)
	out.Binds = cloneBindMap(in.Binds)
	return out
}

func clonePublishMap(in map[string]config.PublishConfig) map[string]config.PublishConfig {
	if len(in) == 0 {
		return map[string]config.PublishConfig{}
	}
	out := make(map[string]config.PublishConfig, len(in))
	for name, publish := range in {
		out[name] = publish
	}
	return out
}

func cloneBindMap(in map[string]config.BindConfig) map[string]config.BindConfig {
	if len(in) == 0 {
		return map[string]config.BindConfig{}
	}
	out := make(map[string]config.BindConfig, len(in))
	for name, bind := range in {
		out[name] = bind
	}
	return out
}

func firstPublishMap(input, existing map[string]config.PublishConfig) map[string]config.PublishConfig {
	if input != nil {
		return input
	}
	return existing
}

func firstBindMap(input, existing map[string]config.BindConfig) map[string]config.BindConfig {
	if input != nil {
		return input
	}
	return existing
}

func sanitizeConfigKey(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	replacer := strings.NewReplacer(" ", "-", "_", "-", ".", "-", "/", "-", "\\", "-", ":", "-")
	normalized = replacer.Replace(normalized)
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}
	return strings.Trim(normalized, "-")
}

func uniqueConfigKey(base string, existing map[string]config.BindConfig) string {
	if base == "" {
		base = "bind"
	}
	if _, ok := existing[base]; !ok {
		return base
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", base, index)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func buildDiscoveredServices(overview *control.Overview, selfName string) []DiscoveredService {
	if overview == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]DiscoveredService, 0)

	addDetails := func(deviceID, deviceName string, services []proto.ServiceInfo) {
		if strings.TrimSpace(deviceName) == "" || deviceName == selfName {
			return
		}
		for _, service := range services {
			name := strings.TrimSpace(service.Name)
			if name == "" {
				continue
			}
			protocol := strings.TrimSpace(service.Protocol)
			if protocol == "" {
				protocol = config.ServiceProtocolUDP
			}
			key := deviceName + "\x00" + name + "\x00" + protocol
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, DiscoveredService{
				DeviceID:    deviceID,
				DeviceName:  deviceName,
				ServiceName: name,
				Protocol:    protocol,
			})
		}
	}
	addNames := func(deviceID, deviceName string, services []string) {
		details := make([]proto.ServiceInfo, 0, len(services))
		for _, service := range services {
			name := strings.TrimSpace(service)
			if name == "" {
				continue
			}
			details = append(details, proto.ServiceInfo{
				Name:     name,
				Protocol: config.ServiceProtocolUDP,
			})
		}
		addDetails(deviceID, deviceName, details)
	}

	if overview.Status != nil {
		for _, peer := range overview.Status.Peers {
			if len(peer.ServiceDetails) > 0 {
				addDetails(peer.DeviceID, peer.DeviceName, peer.ServiceDetails)
				continue
			}
			addNames(peer.DeviceID, peer.DeviceName, peer.Services)
		}
	}
	if overview.Network != nil {
		for _, device := range overview.Network.Devices {
			if len(device.ServiceDetails) > 0 {
				addDetails(device.DeviceID, device.DeviceName, device.ServiceDetails)
				continue
			}
			addNames(device.DeviceID, device.DeviceName, device.Services)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceName == out[j].DeviceName {
			if out[i].ServiceName == out[j].ServiceName {
				return out[i].Protocol < out[j].Protocol
			}
			return out[i].ServiceName < out[j].ServiceName
		}
		return out[i].DeviceName < out[j].DeviceName
	})
	return out
}

func mustPrettyJSON(value any) string {
	raw, err := jsonMarshalIndent(value)
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return raw
}

func jsonMarshalIndent(value any) (string, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Service) executablePath() string {
	if strings.TrimSpace(s.deps.ExecutablePath) == "" {
		return "snt"
	}
	return s.deps.ExecutablePath
}
