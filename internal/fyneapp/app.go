package fyneapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	fyne "fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"simple-nat-traversal/internal/autostart"
	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

const refreshInterval = 5 * time.Second

type Config struct {
	ExecutablePath     string
	ConfigPath         string
	RuntimeManager     *control.RuntimeManager
	Logs               *control.LogBuffer
	InstallAutostart   func(executablePath, configPath string) (autostart.Status, error)
	UninstallAutostart func() (autostart.Status, error)
	KickNetworkDevice  func(context.Context, config.ClientConfig, proto.KickDeviceRequest) (proto.KickDeviceResponse, error)
	LoadOverview       func(context.Context, string, string, config.ClientConfig, bool, error, control.OverviewOptions) (control.Overview, error)
}

type App struct {
	cfg    Config
	app    fyne.App
	window fyne.Window

	overviewGrid *widget.TextGrid
	statusGrid   *widget.TextGrid
	peersGrid    *widget.TextGrid
	routesGrid   *widget.TextGrid
	traceGrid    *widget.TextGrid
	networkGrid  *widget.TextGrid
	logsGrid     *widget.TextGrid

	serverURLEntry       *widget.Entry
	allowInsecureCheck   *widget.Check
	passwordEntry        *widget.Entry
	clearPasswordCheck   *widget.Check
	passwordStatusLabel  *widget.Label
	adminPasswordEntry   *widget.Entry
	clearAdminPassCheck  *widget.Check
	adminPassStatusLabel *widget.Label
	deviceNameEntry      *widget.Entry
	autoConnectCheck     *widget.Check
	udpListenEntry       *widget.Entry
	adminListenEntry     *widget.Entry
	publishEntry         *widget.Entry
	bindsEntry           *widget.Entry
	kickDeviceNameEntry  *widget.Entry
	kickDeviceIDEntry    *widget.Entry
	lastRefreshLabel     *widget.Label
	messageLabel         *widget.Label

	mu              sync.Mutex
	refreshInFlight bool
	closeOnce       sync.Once
	refreshHook     func()
}

func New(cfg Config) (*App, error) {
	if cfg.RuntimeManager == nil {
		cfg.RuntimeManager = control.NewRuntimeManager()
	}
	if cfg.Logs == nil {
		cfg.Logs = control.NewLogBuffer(500)
	}
	if cfg.InstallAutostart == nil {
		cfg.InstallAutostart = autostart.Install
	}
	if cfg.UninstallAutostart == nil {
		cfg.UninstallAutostart = autostart.Uninstall
	}
	if cfg.KickNetworkDevice == nil {
		cfg.KickNetworkDevice = client.KickNetworkDevice
	}
	if cfg.LoadOverview == nil {
		cfg.LoadOverview = control.LoadOverviewForConfig
	}

	fyneApp := app.NewWithID("simple-nat-traversal.gui")
	win := fyneApp.NewWindow("SNT GUI")
	win.SetMaster()
	win.Resize(fyne.NewSize(1280, 860))

	a := &App{
		cfg:    cfg,
		app:    fyneApp,
		window: win,
	}
	a.refreshHook = a.refreshAll
	a.buildUI()
	return a, nil
}

func (a *App) Run(ctx context.Context) error {
	a.window.SetCloseIntercept(func() {
		a.requestClose()
	})

	go func() {
		<-ctx.Done()
		a.requestClose()
	}()

	a.loadConfigIntoForm()
	if err := a.tryAutoConnect(); err != nil {
		log.Printf("fyne gui auto-connect skipped: %v", err)
	}
	a.refreshAll()
	go a.refreshLoop(ctx)

	a.window.Show()
	a.app.Run()
	return nil
}

func (a *App) tryAutoConnect() error {
	cfg, err := config.LoadClientConfig(a.cfg.ConfigPath)
	if err != nil {
		return err
	}
	if !cfg.AutoConnect {
		return errors.New("auto_connect is disabled")
	}
	_, err = a.cfg.RuntimeManager.Start(a.cfg.ConfigPath)
	return err
}

func (a *App) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshAll()
		}
	}
}

func (a *App) requestClose() {
	a.closeOnce.Do(func() {
		go func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = a.cfg.RuntimeManager.Stop(stopCtx)
			fyne.Do(func() {
				a.window.SetCloseIntercept(nil)
				a.window.Close()
				a.app.Quit()
			})
		}()
	})
}

func (a *App) buildUI() {
	a.overviewGrid = newDisplayGrid()
	a.statusGrid = newDisplayGrid()
	a.peersGrid = newDisplayGrid()
	a.routesGrid = newDisplayGrid()
	a.traceGrid = newDisplayGrid()
	a.networkGrid = newDisplayGrid()
	a.logsGrid = newDisplayGrid()

	a.lastRefreshLabel = widget.NewLabel("Last refresh: -")
	a.messageLabel = widget.NewLabel("Ready")

	a.serverURLEntry = widget.NewEntry()
	a.serverURLEntry.SetPlaceHolder("https://YOUR_VPS_PUBLIC_IP")
	a.allowInsecureCheck = widget.NewCheck("allow_insecure_http (仅联调)", nil)
	a.passwordEntry = widget.NewPasswordEntry()
	a.passwordEntry.SetPlaceHolder("留空表示保持已保存 password")
	a.clearPasswordCheck = widget.NewCheck("清空已保存 password", nil)
	a.passwordStatusLabel = widget.NewLabel("password 未保存")
	a.adminPasswordEntry = widget.NewPasswordEntry()
	a.adminPasswordEntry.SetPlaceHolder("留空表示保持已保存 admin_password")
	a.clearAdminPassCheck = widget.NewCheck("清空已保存 admin_password", nil)
	a.adminPassStatusLabel = widget.NewLabel("admin_password 未保存")
	a.deviceNameEntry = widget.NewEntry()
	a.autoConnectCheck = widget.NewCheck("auto_connect", nil)
	a.udpListenEntry = widget.NewEntry()
	a.adminListenEntry = widget.NewEntry()
	a.publishEntry = widget.NewMultiLineEntry()
	a.publishEntry.SetMinRowsVisible(8)
	a.publishEntry.Wrapping = fyne.TextWrapOff
	a.publishEntry.Scroll = fyne.ScrollBoth
	a.bindsEntry = widget.NewMultiLineEntry()
	a.bindsEntry.SetMinRowsVisible(8)
	a.bindsEntry.Wrapping = fyne.TextWrapOff
	a.bindsEntry.Scroll = fyne.ScrollBoth
	a.kickDeviceNameEntry = widget.NewEntry()
	a.kickDeviceNameEntry.SetPlaceHolder("device_name")
	a.kickDeviceIDEntry = widget.NewEntry()
	a.kickDeviceIDEntry.SetPlaceHolder("device_id")

	configForm := widget.NewForm(
		widget.NewFormItem("server_url", a.serverURLEntry),
		widget.NewFormItem("", a.allowInsecureCheck),
		widget.NewFormItem("password", a.passwordEntry),
		widget.NewFormItem("", a.clearPasswordCheck),
		widget.NewFormItem("", a.passwordStatusLabel),
		widget.NewFormItem("admin_password", a.adminPasswordEntry),
		widget.NewFormItem("", a.clearAdminPassCheck),
		widget.NewFormItem("", a.adminPassStatusLabel),
		widget.NewFormItem("device_name", a.deviceNameEntry),
		widget.NewFormItem("", a.autoConnectCheck),
		widget.NewFormItem("udp_listen", a.udpListenEntry),
		widget.NewFormItem("admin_listen", a.adminListenEntry),
		widget.NewFormItem("publish(JSON)", a.publishEntry),
		widget.NewFormItem("binds(JSON)", a.bindsEntry),
	)
	configForm.Orientation = widget.Vertical

	reloadConfigButton := widget.NewButtonWithIcon("Reload Config", theme.ViewRefreshIcon(), func() {
		a.loadConfigIntoForm()
	})
	saveConfigButton := widget.NewButtonWithIcon("Save Config", theme.DocumentSaveIcon(), func() {
		if err := a.saveConfig(); err != nil {
			a.showError(err)
		}
	})
	startButton := widget.NewButtonWithIcon("Start", theme.MediaPlayIcon(), func() {
		if err := a.startClient(); err != nil {
			a.showError(err)
		}
	})
	stopButton := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		if err := a.stopClient(); err != nil {
			a.showError(err)
		}
	})
	installAutostartButton := widget.NewButtonWithIcon("Install Autostart", theme.DownloadIcon(), func() {
		if err := a.installAutostart(); err != nil {
			a.showError(err)
		}
	})
	uninstallAutostartButton := widget.NewButtonWithIcon("Remove Autostart", theme.DeleteIcon(), func() {
		if err := a.uninstallAutostart(); err != nil {
			a.showError(err)
		}
	})
	refreshButton := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		a.refreshAll()
	})
	kickByNameButton := widget.NewButtonWithIcon("Kick By Name", theme.CancelIcon(), func() {
		if err := a.kickDevice(proto.KickDeviceRequest{DeviceName: strings.TrimSpace(a.kickDeviceNameEntry.Text)}); err != nil {
			a.showError(err)
		}
	})
	kickByIDButton := widget.NewButtonWithIcon("Kick By ID", theme.CancelIcon(), func() {
		if err := a.kickDevice(proto.KickDeviceRequest{DeviceID: strings.TrimSpace(a.kickDeviceIDEntry.Text)}); err != nil {
			a.showError(err)
		}
	})

	topBar := container.NewVBox(
		widget.NewLabelWithStyle("Simple NAT Traversal", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel("Native Fyne GUI wrapping config/runtime/status/network/autostart"),
		container.NewHBox(
			refreshButton,
			reloadConfigButton,
			saveConfigButton,
			startButton,
			stopButton,
			installAutostartButton,
			uninstallAutostartButton,
		),
		container.NewHBox(
			widget.NewLabel("Config: "+a.cfg.ConfigPath),
			widget.NewSeparator(),
			a.lastRefreshLabel,
		),
		a.messageLabel,
	)

	networkTab := container.NewBorder(
		container.NewVBox(
			widget.NewLabel("Kick device by device_name or device_id"),
			container.NewHBox(a.kickDeviceNameEntry, kickByNameButton),
			container.NewHBox(a.kickDeviceIDEntry, kickByIDButton),
		),
		nil,
		nil,
		nil,
		a.networkGrid,
	)

	statusTab := container.NewBorder(
		widget.NewLabel("Raw runtime and status snapshot"),
		nil,
		nil,
		nil,
		a.statusGrid,
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Overview", a.overviewGrid),
		container.NewTabItem("Config", container.NewScroll(configForm)),
		container.NewTabItem("Status", statusTab),
		container.NewTabItem("Peers", a.peersGrid),
		container.NewTabItem("Routes", a.routesGrid),
		container.NewTabItem("Trace", a.traceGrid),
		container.NewTabItem("Network", networkTab),
		container.NewTabItem("Logs", a.logsGrid),
	)

	a.window.SetContent(container.NewBorder(topBar, nil, nil, nil, tabs))
}

func (a *App) loadConfigIntoForm() {
	cfg, exists, err := loadConfigOrDefault(a.cfg.ConfigPath)
	if err != nil {
		a.showError(err)
		return
	}
	a.fillConfigForm(cfg, exists)
}

func (a *App) fillConfigForm(cfg config.ClientConfig, exists bool) {
	a.serverURLEntry.SetText(cfg.ServerURL)
	a.allowInsecureCheck.SetChecked(cfg.AllowInsecureHTTP)
	a.passwordEntry.SetText("")
	a.clearPasswordCheck.SetChecked(false)
	a.adminPasswordEntry.SetText("")
	a.clearAdminPassCheck.SetChecked(false)
	a.deviceNameEntry.SetText(cfg.DeviceName)
	a.autoConnectCheck.SetChecked(cfg.AutoConnect)
	a.udpListenEntry.SetText(cfg.UDPListen)
	a.adminListenEntry.SetText(cfg.AdminListen)
	a.publishEntry.SetText(mustPrettyJSON(cfg.Publish))
	a.bindsEntry.SetText(mustPrettyJSON(cfg.Binds))

	if exists && strings.TrimSpace(cfg.Password) != "" {
		a.passwordStatusLabel.SetText("password 已保存，留空表示不修改")
	} else {
		a.passwordStatusLabel.SetText("password 未保存")
	}
	if exists && strings.TrimSpace(cfg.AdminPassword) != "" {
		a.adminPassStatusLabel.SetText("admin_password 已保存，留空表示不修改")
	} else {
		a.adminPassStatusLabel.SetText("admin_password 未保存")
	}
}

func (a *App) saveConfig() error {
	cfg, err := a.collectConfigFromForm()
	if err != nil {
		return err
	}
	if err := config.SaveClientConfig(a.cfg.ConfigPath, cfg); err != nil {
		return err
	}
	a.setMessage("Config saved")
	a.loadConfigIntoForm()
	return nil
}

func (a *App) startClient() error {
	if err := a.saveConfig(); err != nil {
		return err
	}
	status, err := a.cfg.RuntimeManager.Start(a.cfg.ConfigPath)
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("Runtime %s", status.State))
	a.triggerRefresh()
	return nil
}

func (a *App) stopClient() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := a.cfg.RuntimeManager.Stop(ctx)
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("Runtime %s", status.State))
	a.triggerRefresh()
	return nil
}

func (a *App) installAutostart() error {
	if err := a.saveConfig(); err != nil {
		return err
	}
	status, err := a.cfg.InstallAutostart(a.cfg.ExecutablePath, a.cfg.ConfigPath)
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("Autostart installed=%t", status.Installed))
	a.triggerRefresh()
	return nil
}

func (a *App) uninstallAutostart() error {
	status, err := a.cfg.UninstallAutostart()
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("Autostart installed=%t", status.Installed))
	a.triggerRefresh()
	return nil
}

func (a *App) collectConfigFromForm() (config.ClientConfig, error) {
	cfg, draftErr := a.draftConfigFromForm()
	if draftErr != nil {
		return config.ClientConfig{}, draftErr
	}
	if err := config.ValidateClientConfig(&cfg); err != nil {
		return config.ClientConfig{}, err
	}
	return cfg, nil
}

func (a *App) draftConfigFromForm() (config.ClientConfig, error) {
	cfg := config.ClientDefaults()
	existingCfg, err := config.LoadClientConfig(a.cfg.ConfigPath)
	switch {
	case err == nil:
		cfg = existingCfg
	case errors.Is(err, os.ErrNotExist):
	default:
		return config.ClientConfig{}, err
	}

	if a.clearPasswordCheck.Checked && strings.TrimSpace(a.passwordEntry.Text) != "" {
		return config.ClientConfig{}, errors.New("password cannot be replaced and cleared at the same time")
	}
	if a.clearAdminPassCheck.Checked && strings.TrimSpace(a.adminPasswordEntry.Text) != "" {
		return config.ClientConfig{}, errors.New("admin_password cannot be replaced and cleared at the same time")
	}

	cfg.ServerURL = strings.TrimSpace(a.serverURLEntry.Text)
	cfg.AllowInsecureHTTP = a.allowInsecureCheck.Checked
	cfg.DeviceName = strings.TrimSpace(a.deviceNameEntry.Text)
	cfg.AutoConnect = a.autoConnectCheck.Checked
	cfg.UDPListen = strings.TrimSpace(a.udpListenEntry.Text)
	cfg.AdminListen = strings.TrimSpace(a.adminListenEntry.Text)

	if a.clearPasswordCheck.Checked {
		cfg.Password = ""
	} else if strings.TrimSpace(a.passwordEntry.Text) != "" {
		cfg.Password = a.passwordEntry.Text
	}
	if a.clearAdminPassCheck.Checked {
		cfg.AdminPassword = ""
	} else if strings.TrimSpace(a.adminPasswordEntry.Text) != "" {
		cfg.AdminPassword = a.adminPasswordEntry.Text
	}

	publish, err := parsePublishJSON(a.publishEntry.Text)
	if err != nil {
		return config.ClientConfig{}, err
	}
	binds, err := parseBindJSON(a.bindsEntry.Text)
	if err != nil {
		return config.ClientConfig{}, err
	}
	cfg.Publish = publish
	cfg.Binds = binds
	return cfg, nil
}

func (a *App) kickDevice(req proto.KickDeviceRequest) error {
	if strings.TrimSpace(req.DeviceName) == "" && strings.TrimSpace(req.DeviceID) == "" {
		return errors.New("device_name or device_id is required")
	}

	cfg, err := a.draftConfigFromForm()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := a.cfg.KickNetworkDevice(ctx, cfg, req)
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("Kicked %s (%s)", resp.DeviceName, resp.DeviceID))
	a.kickDeviceNameEntry.SetText("")
	a.kickDeviceIDEntry.SetText("")
	a.triggerRefresh()
	return nil
}

func (a *App) triggerRefresh() {
	if a.refreshHook != nil {
		a.refreshHook()
		return
	}
	a.refreshAll()
}

func (a *App) refreshAll() {
	a.mu.Lock()
	if a.refreshInFlight {
		a.mu.Unlock()
		return
	}
	a.refreshInFlight = true
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			a.refreshInFlight = false
			a.mu.Unlock()
		}()

		executablePath := a.cfg.ExecutablePath
		if executablePath == "" {
			executablePath = "snt"
		}

		draftCfg, draftErr := a.draftConfigFromForm()
		configExists := false
		if _, err := os.Stat(a.cfg.ConfigPath); err == nil {
			configExists = true
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		overview, err := a.cfg.LoadOverview(ctx, executablePath, a.cfg.ConfigPath, draftCfg, configExists, draftErr, control.OverviewOptions{IncludeNetwork: true})
		if err != nil {
			fyne.Do(func() {
				a.setMessage("Refresh failed")
				dialog.ShowError(err, a.window)
			})
			return
		}

		runtimeStatus := a.cfg.RuntimeManager.Snapshot()
		statusText := mustPrettyJSON(runtimeStatus)
		if overview.Status != nil {
			statusText = joinSections(statusText, mustPrettyJSON(overview.Status))
		}
		peersText := textOrMessageFromStatus(overview.Status, overview.StatusError, client.RenderPeersStatus)
		routesText := textOrMessageFromStatus(overview.Status, overview.StatusError, client.RenderRoutesStatus)
		traceText := textOrMessageFromStatus(overview.Status, overview.StatusError, client.RenderTraceStatus)
		networkText := textOrMessageFromNetwork(overview.Network, overview.NetworkError)
		logsText := strings.Join(a.cfg.Logs.Snapshot(), "\n")
		if strings.TrimSpace(logsText) == "" {
			logsText = "logs: none"
		}

		fyne.Do(func() {
			a.overviewGrid.SetText(control.RenderOverview(overview))
			a.statusGrid.SetText(statusText)
			a.peersGrid.SetText(peersText)
			a.routesGrid.SetText(routesText)
			a.traceGrid.SetText(traceText)
			a.networkGrid.SetText(networkText)
			a.logsGrid.SetText(logsText)
			a.logsGrid.ScrollToBottom()
			a.lastRefreshLabel.SetText("Last refresh: " + time.Now().Format(time.RFC3339))
			a.setMessage("Refreshed")
		})
	}()
}

func (a *App) setMessage(message string) {
	a.messageLabel.SetText(message)
}

func (a *App) showError(err error) {
	a.setMessage(err.Error())
	dialog.ShowError(err, a.window)
}

func newDisplayGrid() *widget.TextGrid {
	grid := widget.NewTextGrid()
	grid.Scroll = fyne.ScrollBoth
	grid.SetText("loading...")
	return grid
}

func mustPrettyJSON(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("encode_failed: %v", err)
	}
	return string(raw)
}

func parsePublishJSON(text string) (map[string]config.PublishConfig, error) {
	if strings.TrimSpace(text) == "" {
		return map[string]config.PublishConfig{}, nil
	}
	out := map[string]config.PublishConfig{}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("parse publish JSON: %w", err)
	}
	return out, nil
}

func parseBindJSON(text string) (map[string]config.BindConfig, error) {
	if strings.TrimSpace(text) == "" {
		return map[string]config.BindConfig{}, nil
	}
	out := map[string]config.BindConfig{}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("parse binds JSON: %w", err)
	}
	return out, nil
}

func textOrMessageFromStatus(snapshot *client.StatusSnapshot, statusErr string, render func(client.StatusSnapshot) string) string {
	if snapshot == nil {
		if strings.TrimSpace(statusErr) != "" {
			return "status_unavailable\n" + statusErr
		}
		return "status_unavailable"
	}
	return render(*snapshot)
}

func textOrMessageFromNetwork(snapshot *proto.NetworkDevicesResponse, networkErr string) string {
	if snapshot == nil {
		if strings.TrimSpace(networkErr) != "" {
			return "network_unavailable\n" + networkErr
		}
		return "network_unavailable"
	}
	return client.RenderNetworkDevicesStatus(*snapshot)
}

func joinSections(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return strings.Join(out, "\n\n")
}

func loadConfigOrDefault(path string) (config.ClientConfig, bool, error) {
	cfg, err := config.LoadClientConfig(path)
	if err == nil {
		return cfg, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return config.ClientDefaults(), false, nil
	}
	return config.ClientConfig{}, false, err
}
