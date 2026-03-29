package fyneapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
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
	"simple-nat-traversal/internal/logx"
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
	SetRuntimeLogLevel func(context.Context, config.ClientConfig, string) (proto.LogLevelResponse, error)
	LoadOverview       func(context.Context, string, string, config.ClientConfig, bool, error, control.OverviewOptions) (control.Overview, error)
}

type discoveredService struct {
	DeviceID    string
	DeviceName  string
	ServiceName string
	Protocol    string
}

var serviceProtocolOptions = []string{
	config.ServiceProtocolUDP,
	config.ServiceProtocolTCP,
}

func (d discoveredService) normalizedProtocol() string {
	protocol := strings.TrimSpace(d.Protocol)
	if protocol == "" {
		return config.ServiceProtocolUDP
	}
	return protocol
}

func (d discoveredService) displayServiceName() string {
	protocol := d.normalizedProtocol()
	if protocol == config.ServiceProtocolUDP {
		return d.ServiceName
	}
	return d.ServiceName + "/" + protocol
}

func (d discoveredService) optionLabel() string {
	return fmt.Sprintf("%s / %s", d.DeviceName, d.displayServiceName())
}

func newServiceProtocolSelect() *widget.Select {
	selectWidget := widget.NewSelect(serviceProtocolOptions, nil)
	selectWidget.SetSelected(config.ServiceProtocolUDP)
	return selectWidget
}

func setServiceProtocolSelection(selectWidget *widget.Select, protocol string) {
	if selectWidget == nil {
		return
	}
	normalized, err := config.NormalizeServiceProtocol(protocol)
	if err != nil {
		normalized = config.ServiceProtocolUDP
	}
	selectWidget.SetSelected(normalized)
}

func selectedServiceProtocol(selectWidget *widget.Select) (string, error) {
	if selectWidget == nil {
		return config.ServiceProtocolUDP, nil
	}
	return config.NormalizeServiceProtocol(selectWidget.Selected)
}

type App struct {
	cfg               Config
	app               fyne.App
	window            fyne.Window
	locale            string
	defaultDeviceName string

	overviewGrid   *widget.TextGrid
	statusGrid     *widget.TextGrid
	peersGrid      *widget.TextGrid
	routesGrid     *widget.TextGrid
	traceGrid      *widget.TextGrid
	networkGrid    *widget.TextGrid
	logsGrid       *widget.TextGrid
	publishGrid    *widget.TextGrid
	bindGrid       *widget.TextGrid
	discoveredGrid *widget.TextGrid

	serverURLEntry       *widget.Entry
	allowInsecureCheck   *widget.Check
	passwordEntry        *widget.Entry
	clearPasswordCheck   *widget.Check
	passwordStatusLabel  *widget.Label
	adminPasswordEntry   *widget.Entry
	clearAdminPassCheck  *widget.Check
	adminPassStatusLabel *widget.Label
	deviceNameEntry      *widget.Entry
	deviceHintLabel      *widget.Label
	autoConnectCheck     *widget.Check
	udpListenEntry       *widget.Entry
	adminListenEntry     *widget.Entry
	logLevelSelect       *widget.Select

	publishSelect     *widget.Select
	publishProtocol   *widget.Select
	publishNameEntry  *widget.Entry
	publishLocalEntry *widget.Entry

	bindSelect       *widget.Select
	bindProtocol     *widget.Select
	bindNameEntry    *widget.Entry
	bindPeerEntry    *widget.Entry
	bindServiceEntry *widget.Entry
	bindLocalEntry   *widget.Entry

	discoveredSelect *widget.Select

	kickDeviceNameEntry *widget.Entry
	kickDeviceIDEntry   *widget.Entry

	lastRefreshLabel *widget.Label
	messageLabel     *widget.Label

	mu               sync.Mutex
	refreshInFlight  bool
	closeOnce        sync.Once
	refreshHook      func()
	refreshDoneHook  func()
	draftPublish     map[string]config.PublishConfig
	draftBinds       map[string]config.BindConfig
	discovered       []discoveredService
	currentOverview  *control.Overview
	lastConfigExists bool
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
	if cfg.SetRuntimeLogLevel == nil {
		cfg.SetRuntimeLogLevel = client.SetRuntimeLogLevel
	}
	if cfg.LoadOverview == nil {
		cfg.LoadOverview = control.LoadOverviewForConfig
	}

	locale := detectLocale()
	fyneApp := app.NewWithID("simple-nat-traversal.gui")
	fyneApp.SetIcon(appIconResource())
	win := fyneApp.NewWindow(translations[localeEnglish]["app_title"])
	win.SetIcon(appIconResource())
	win.SetMaster()
	win.Resize(fyne.NewSize(1360, 900))

	a := &App{
		cfg:               cfg,
		app:               fyneApp,
		window:            win,
		locale:            locale,
		defaultDeviceName: config.SuggestedDeviceName(),
		draftPublish:      map[string]config.PublishConfig{},
		draftBinds:        map[string]config.BindConfig{},
	}
	win.SetTitle(a.t("app_title"))
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
		logx.Warnf("fyne gui auto-connect failed: %v", err)
	}
	a.refreshAll()
	go a.refreshLoop(ctx)

	a.window.Show()
	a.app.Run()
	return nil
}

func (a *App) t(key string) string {
	if value := translations[a.locale][key]; strings.TrimSpace(value) != "" {
		return value
	}
	if value := translations[localeEnglish][key]; strings.TrimSpace(value) != "" {
		return value
	}
	return key
}

func (a *App) tryAutoConnect() error {
	cfg, err := config.LoadClientConfig(a.cfg.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !cfg.AutoConnect {
		return nil
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
	a.overviewGrid = newDisplayGrid(a.t("loading"))
	a.statusGrid = newDisplayGrid(a.t("loading"))
	a.peersGrid = newDisplayGrid(a.t("loading"))
	a.routesGrid = newDisplayGrid(a.t("loading"))
	a.traceGrid = newDisplayGrid(a.t("loading"))
	a.networkGrid = newDisplayGrid(a.t("loading"))
	a.logsGrid = newDisplayGrid(a.t("loading"))
	a.publishGrid = newDisplayGrid(a.t("publish_none"))
	a.bindGrid = newDisplayGrid(a.t("bind_none"))
	a.discoveredGrid = newDisplayGrid(a.t("discovered_none"))

	a.lastRefreshLabel = widget.NewLabel(a.t("last_refresh") + ": -")
	a.messageLabel = widget.NewLabel(a.t("ready"))

	a.serverURLEntry = widget.NewEntry()
	a.serverURLEntry.SetPlaceHolder("https://YOUR_VPS_PUBLIC_IP")
	a.allowInsecureCheck = widget.NewCheck(a.t("allow_insecure_http"), nil)

	a.passwordEntry = widget.NewPasswordEntry()
	a.passwordEntry.SetPlaceHolder(a.t("password_saved"))
	a.clearPasswordCheck = widget.NewCheck(a.t("clear_password"), nil)
	a.passwordStatusLabel = widget.NewLabel(a.t("password_missing"))

	a.adminPasswordEntry = widget.NewPasswordEntry()
	a.adminPasswordEntry.SetPlaceHolder(a.t("admin_password_saved"))
	a.clearAdminPassCheck = widget.NewCheck(a.t("clear_admin_password"), nil)
	a.adminPassStatusLabel = widget.NewLabel(a.t("admin_password_missing"))

	a.deviceNameEntry = widget.NewEntry()
	a.deviceHintLabel = widget.NewLabel(a.t("device_name_hint"))
	a.autoConnectCheck = widget.NewCheck(a.t("auto_connect"), nil)
	a.udpListenEntry = widget.NewEntry()
	a.adminListenEntry = widget.NewEntry()
	a.logLevelSelect = widget.NewSelect([]string{
		config.LogLevelDebug,
		config.LogLevelInfo,
		config.LogLevelWarn,
		config.LogLevelError,
	}, nil)
	a.logLevelSelect.SetSelected(config.LogLevelInfo)

	a.publishSelect = widget.NewSelect(nil, func(name string) {
		a.loadSelectedPublish(name)
	})
	a.publishProtocol = newServiceProtocolSelect()
	a.publishNameEntry = widget.NewEntry()
	a.publishNameEntry.SetPlaceHolder(a.t("publish_placeholder_name"))
	a.publishLocalEntry = widget.NewEntry()
	a.publishLocalEntry.SetPlaceHolder(a.t("publish_placeholder_local"))

	a.bindSelect = widget.NewSelect(nil, func(name string) {
		a.loadSelectedBind(name)
	})
	a.bindProtocol = newServiceProtocolSelect()
	a.bindNameEntry = widget.NewEntry()
	a.bindNameEntry.SetPlaceHolder(a.t("bind_placeholder_name"))
	a.bindPeerEntry = widget.NewEntry()
	a.bindPeerEntry.SetPlaceHolder(a.t("bind_placeholder_peer"))
	a.bindServiceEntry = widget.NewEntry()
	a.bindServiceEntry.SetPlaceHolder(a.t("bind_placeholder_service"))
	a.bindLocalEntry = widget.NewEntry()
	a.bindLocalEntry.SetPlaceHolder(a.t("bind_placeholder_local"))

	a.discoveredSelect = widget.NewSelect(nil, nil)

	a.kickDeviceNameEntry = widget.NewEntry()
	a.kickDeviceNameEntry.SetPlaceHolder(a.t("kick_target_name"))
	a.kickDeviceIDEntry = widget.NewEntry()
	a.kickDeviceIDEntry.SetPlaceHolder(a.t("kick_target_id"))

	refreshButton := widget.NewButtonWithIcon(a.t("refresh"), theme.ViewRefreshIcon(), func() {
		a.refreshAll()
	})
	reloadConfigButton := widget.NewButtonWithIcon(a.t("reload_config"), theme.ViewRefreshIcon(), func() {
		a.loadConfigIntoForm()
	})
	saveConfigButton := widget.NewButtonWithIcon(a.t("save_config"), theme.DocumentSaveIcon(), func() {
		if err := a.saveConfig(); err != nil {
			a.showError(err)
		}
	})
	startButton := widget.NewButtonWithIcon(a.t("start_client"), theme.MediaPlayIcon(), func() {
		if err := a.startClient(); err != nil {
			a.showError(err)
		}
	})
	stopButton := widget.NewButtonWithIcon(a.t("stop_client"), theme.MediaStopIcon(), func() {
		if err := a.stopClient(); err != nil {
			a.showError(err)
		}
	})
	installAutostartButton := widget.NewButtonWithIcon(a.t("install_autostart"), theme.DownloadIcon(), func() {
		if err := a.installAutostart(); err != nil {
			a.showError(err)
		}
	})
	uninstallAutostartButton := widget.NewButtonWithIcon(a.t("remove_autostart"), theme.DeleteIcon(), func() {
		if err := a.uninstallAutostart(); err != nil {
			a.showError(err)
		}
	})
	applyLogLevelButton := widget.NewButton(a.t("apply_log_level"), func() {
		if err := a.applyLogLevel(); err != nil {
			a.showError(err)
		}
	})

	publishActions := container.NewHBox(
		widget.NewButton(a.t("add_or_update"), func() {
			if err := a.upsertPublish(); err != nil {
				a.showError(err)
			}
		}),
		widget.NewButton(a.t("delete_selected"), func() {
			if err := a.deleteSelectedPublish(); err != nil {
				a.showError(err)
			}
		}),
		widget.NewButton(a.t("load_selected"), func() {
			a.loadSelectedPublish(a.publishSelect.Selected)
		}),
	)

	bindActions := container.NewHBox(
		widget.NewButton(a.t("add_or_update"), func() {
			if err := a.upsertBind(); err != nil {
				a.showError(err)
			}
		}),
		widget.NewButton(a.t("delete_selected"), func() {
			if err := a.deleteSelectedBind(); err != nil {
				a.showError(err)
			}
		}),
		widget.NewButton(a.t("load_selected"), func() {
			a.loadSelectedBind(a.bindSelect.Selected)
		}),
	)

	quickBindButton := widget.NewButtonWithIcon(a.t("quick_bind"), theme.ContentAddIcon(), func() {
		if err := a.quickBindDiscoveredService(); err != nil {
			a.showError(err)
		}
	})

	kickByNameButton := widget.NewButtonWithIcon(a.t("kick_by_name"), theme.CancelIcon(), func() {
		if err := a.kickDevice(proto.KickDeviceRequest{DeviceName: strings.TrimSpace(a.kickDeviceNameEntry.Text)}); err != nil {
			a.showError(err)
		}
	})
	kickByIDButton := widget.NewButtonWithIcon(a.t("kick_by_id"), theme.CancelIcon(), func() {
		if err := a.kickDevice(proto.KickDeviceRequest{DeviceID: strings.TrimSpace(a.kickDeviceIDEntry.Text)}); err != nil {
			a.showError(err)
		}
	})

	connectionCard := widget.NewCard(
		a.t("section_connection"),
		"",
		widget.NewForm(
			widget.NewFormItem(a.t("server_url"), a.serverURLEntry),
			widget.NewFormItem("", a.allowInsecureCheck),
			widget.NewFormItem(a.t("device_name"), a.deviceNameEntry),
			widget.NewFormItem("", a.deviceHintLabel),
			widget.NewFormItem("", a.autoConnectCheck),
			widget.NewFormItem(a.t("udp_listen"), a.udpListenEntry),
			widget.NewFormItem(a.t("admin_listen"), a.adminListenEntry),
		),
	)
	connectionCard.SetSubTitle("")

	credentialsCard := widget.NewCard(
		a.t("section_credentials"),
		"",
		widget.NewForm(
			widget.NewFormItem(a.t("password"), a.passwordEntry),
			widget.NewFormItem("", a.clearPasswordCheck),
			widget.NewFormItem("", a.passwordStatusLabel),
			widget.NewFormItem(a.t("admin_password"), a.adminPasswordEntry),
			widget.NewFormItem("", a.clearAdminPassCheck),
			widget.NewFormItem("", a.adminPassStatusLabel),
		),
	)

	runtimeCard := widget.NewCard(
		a.t("section_runtime"),
		"",
		container.NewVBox(
			container.NewHBox(
				refreshButton,
				reloadConfigButton,
				saveConfigButton,
				startButton,
				stopButton,
			),
			container.NewHBox(
				installAutostartButton,
				uninstallAutostartButton,
			),
			widget.NewForm(
				widget.NewFormItem(a.t("log_level"), a.logLevelSelect),
			),
			applyLogLevelButton,
		),
	)

	publishCard := widget.NewCard(
		a.t("section_publish"),
		"",
		container.NewVBox(
			widget.NewLabel(a.t("publish_list")),
			a.publishGrid,
			container.NewGridWithColumns(2, a.publishSelect, widget.NewLabel("")),
			widget.NewForm(
				widget.NewFormItem(a.t("service_protocol"), a.publishProtocol),
				widget.NewFormItem(a.t("publish_name"), a.publishNameEntry),
				widget.NewFormItem(a.t("publish_local"), a.publishLocalEntry),
			),
			publishActions,
		),
	)

	discoveredCard := widget.NewCard(
		a.t("section_discovered"),
		a.t("auto_bind_note"),
		container.NewVBox(
			a.discoveredGrid,
			a.discoveredSelect,
			quickBindButton,
		),
	)

	bindCard := widget.NewCard(
		a.t("section_bind"),
		"",
		container.NewVBox(
			widget.NewLabel(a.t("bind_list")),
			a.bindGrid,
			container.NewGridWithColumns(2, a.bindSelect, widget.NewLabel("")),
			widget.NewForm(
				widget.NewFormItem(a.t("service_protocol"), a.bindProtocol),
				widget.NewFormItem(a.t("bind_name"), a.bindNameEntry),
				widget.NewFormItem(a.t("bind_peer"), a.bindPeerEntry),
				widget.NewFormItem(a.t("bind_service"), a.bindServiceEntry),
				widget.NewFormItem(a.t("bind_local"), a.bindLocalEntry),
			),
			bindActions,
		),
	)

	connectionTab := container.NewScroll(container.NewVBox(
		connectionCard,
		credentialsCard,
		runtimeCard,
	))

	servicesTab := container.NewScroll(container.NewVBox(
		publishCard,
		discoveredCard,
		bindCard,
	))

	networkTab := container.NewScroll(container.NewVBox(
		widget.NewCard(
			a.t("section_network_devices"),
			"",
			a.networkGrid,
		),
		widget.NewCard(
			a.t("section_kick"),
			"",
			container.NewVBox(
				container.NewHBox(a.kickDeviceNameEntry, kickByNameButton),
				container.NewHBox(a.kickDeviceIDEntry, kickByIDButton),
			),
		),
	))

	diagnosticsTabs := container.NewAppTabs(
		container.NewTabItem(a.t("tab_status"), container.NewBorder(widget.NewLabel(a.t("raw_status")), nil, nil, nil, a.statusGrid)),
		container.NewTabItem(a.t("tab_peers"), a.peersGrid),
		container.NewTabItem(a.t("tab_routes"), a.routesGrid),
		container.NewTabItem(a.t("tab_trace"), a.traceGrid),
		container.NewTabItem(a.t("tab_logs"), a.logsGrid),
	)

	topBar := container.NewVBox(
		widget.NewLabelWithStyle(a.t("app_title"), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewLabel(a.t("app_subtitle")),
		container.NewHBox(
			widget.NewLabel(a.t("config_path")+": "+a.cfg.ConfigPath),
			widget.NewSeparator(),
			a.lastRefreshLabel,
		),
		a.messageLabel,
	)

	tabs := container.NewAppTabs(
		container.NewTabItem(a.t("tab_overview"), a.overviewGrid),
		container.NewTabItem(a.t("tab_connection"), connectionTab),
		container.NewTabItem(a.t("tab_services"), servicesTab),
		container.NewTabItem(a.t("tab_network"), networkTab),
		container.NewTabItem(a.t("tab_diagnostics"), diagnosticsTabs),
	)

	a.window.SetContent(container.NewBorder(topBar, nil, nil, nil, tabs))
}

func (a *App) loadConfigIntoForm() {
	cfg, exists, err := loadConfigOrDefault(a.cfg.ConfigPath, a.defaultDeviceName)
	if err != nil {
		a.showError(err)
		return
	}
	a.fillConfigForm(cfg, exists)
	if !exists {
		a.setMessage(a.t("config_missing"))
	}
}

func (a *App) fillConfigForm(cfg config.ClientConfig, exists bool) {
	if strings.TrimSpace(cfg.DeviceName) == "" {
		cfg.DeviceName = a.defaultDeviceName
	}
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
	a.logLevelSelect.SetSelected(cfg.LogLevel)
	setServiceProtocolSelection(a.publishProtocol, config.ServiceProtocolUDP)
	setServiceProtocolSelection(a.bindProtocol, config.ServiceProtocolUDP)

	if exists && strings.TrimSpace(cfg.Password) != "" {
		a.passwordStatusLabel.SetText(a.t("password_saved"))
	} else {
		a.passwordStatusLabel.SetText(a.t("password_missing"))
	}
	if exists && strings.TrimSpace(cfg.AdminPassword) != "" {
		a.adminPassStatusLabel.SetText(a.t("admin_password_saved"))
	} else {
		a.adminPassStatusLabel.SetText(a.t("admin_password_missing"))
	}

	a.mu.Lock()
	a.draftPublish = clonePublishMap(cfg.Publish)
	a.draftBinds = cloneBindMap(cfg.Binds)
	a.lastConfigExists = exists
	a.mu.Unlock()
	a.updateServiceViews()
}

func (a *App) saveConfig() error {
	cfg, err := a.collectConfigFromForm()
	if err != nil {
		return err
	}
	if err := config.SaveClientConfig(a.cfg.ConfigPath, cfg); err != nil {
		return err
	}
	a.setMessage(a.t("config_saved"))
	a.loadConfigIntoForm()
	return nil
}

func (a *App) applyLogLevel() error {
	level, err := config.NormalizeLogLevel(a.selectedLogLevel())
	if err != nil {
		return err
	}

	cfg, exists, err := loadConfigOrDefault(a.cfg.ConfigPath, a.defaultDeviceName)
	if err != nil {
		return err
	}
	if !exists {
		cfg, err = a.collectConfigFromForm()
		if err != nil {
			return err
		}
	}
	cfg.LogLevel = level
	if err := config.SaveClientConfig(a.cfg.ConfigPath, cfg); err != nil {
		return err
	}

	if a.cfg.RuntimeManager.Snapshot().State == "running" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := a.cfg.SetRuntimeLogLevel(ctx, cfg, level)
		a.loadConfigIntoForm()
		a.triggerRefresh()
		if err != nil {
			return fmt.Errorf("log level saved to config but failed to update running client: %w", err)
		}
		a.setMessage(fmt.Sprintf("%s: %s", a.t("log_level_applied"), resp.LogLevel))
		return nil
	}

	a.loadConfigIntoForm()
	a.setMessage(fmt.Sprintf("%s: %s", a.t("log_level_saved"), level))
	a.triggerRefresh()
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
	a.setMessage(fmt.Sprintf("%s: %s", a.t("runtime_state"), status.State))
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
	a.setMessage(fmt.Sprintf("%s: %s", a.t("runtime_state"), status.State))
	a.triggerRefresh()
	return nil
}

func (a *App) restartClientIfRunning() error {
	if a.cfg.RuntimeManager.Snapshot().State != "running" {
		a.triggerRefresh()
		return nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := a.cfg.RuntimeManager.Stop(stopCtx); err != nil {
		return err
	}
	if _, err := a.cfg.RuntimeManager.Start(a.cfg.ConfigPath); err != nil {
		return err
	}
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
	a.setMessage(fmt.Sprintf("%s=%t", a.t("autostart_state"), status.Installed))
	a.triggerRefresh()
	return nil
}

func (a *App) uninstallAutostart() error {
	status, err := a.cfg.UninstallAutostart()
	if err != nil {
		return err
	}
	a.setMessage(fmt.Sprintf("%s=%t", a.t("autostart_state"), status.Installed))
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
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	return a.draftConfigFromFormWithServices(publish, binds)
}

func (a *App) draftConfigFromFormWithServices(publish map[string]config.PublishConfig, binds map[string]config.BindConfig) (config.ClientConfig, error) {
	cfg := config.ClientDefaults()
	cfg.DeviceName = a.defaultDeviceName

	existingCfg, err := config.LoadClientConfig(a.cfg.ConfigPath)
	switch {
	case err == nil:
		cfg = existingCfg
	case errors.Is(err, os.ErrNotExist):
	default:
		return config.ClientConfig{}, err
	}

	if strings.TrimSpace(cfg.DeviceName) == "" {
		cfg.DeviceName = a.defaultDeviceName
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
	if cfg.DeviceName == "" {
		cfg.DeviceName = a.defaultDeviceName
	}
	cfg.AutoConnect = a.autoConnectCheck.Checked
	cfg.UDPListen = strings.TrimSpace(a.udpListenEntry.Text)
	cfg.AdminListen = strings.TrimSpace(a.adminListenEntry.Text)
	cfg.LogLevel = a.selectedLogLevel()

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

	cfg.Publish = clonePublishMap(publish)
	cfg.Binds = cloneBindMap(binds)
	return cfg, nil
}

func (a *App) upsertPublish() error {
	name := sanitizeConfigKey(a.publishNameEntry.Text)
	local := strings.TrimSpace(a.publishLocalEntry.Text)
	if name == "" || local == "" {
		return errors.New("publish name and local address are required")
	}
	protocol, err := selectedServiceProtocol(a.publishProtocol)
	if err != nil {
		return err
	}
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	publish[name] = config.PublishConfig{
		Protocol: protocol,
		Local:    local,
	}
	return a.persistServiceDrafts(publish, binds, a.t("service_added"))
}

func (a *App) deleteSelectedPublish() error {
	name := strings.TrimSpace(a.publishSelect.Selected)
	if name == "" {
		return errors.New("select a publish item first")
	}
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	delete(publish, name)
	return a.persistServiceDrafts(publish, binds, a.t("service_removed"))
}

func (a *App) loadSelectedPublish(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	a.mu.Lock()
	publish, ok := a.draftPublish[name]
	a.mu.Unlock()
	if !ok {
		return
	}
	a.publishNameEntry.SetText(name)
	a.publishLocalEntry.SetText(publish.Local)
	setServiceProtocolSelection(a.publishProtocol, publish.Protocol)
}

func (a *App) upsertBind() error {
	name := sanitizeConfigKey(a.bindNameEntry.Text)
	peer := strings.TrimSpace(a.bindPeerEntry.Text)
	service := strings.TrimSpace(a.bindServiceEntry.Text)
	local := strings.TrimSpace(a.bindLocalEntry.Text)
	if name == "" || peer == "" || service == "" || local == "" {
		return errors.New("bind name, peer, service and local address are required")
	}
	protocol, err := selectedServiceProtocol(a.bindProtocol)
	if err != nil {
		return err
	}
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	binds[name] = config.BindConfig{
		Protocol: protocol,
		Peer:     peer,
		Service:  service,
		Local:    local,
	}
	return a.persistServiceDrafts(publish, binds, a.t("service_added"))
}

func (a *App) deleteSelectedBind() error {
	name := strings.TrimSpace(a.bindSelect.Selected)
	if name == "" {
		return errors.New("select a bind item first")
	}
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	delete(binds, name)
	return a.persistServiceDrafts(publish, binds, a.t("service_removed"))
}

func (a *App) loadSelectedBind(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	a.mu.Lock()
	bind, ok := a.draftBinds[name]
	a.mu.Unlock()
	if !ok {
		return
	}
	a.bindNameEntry.SetText(name)
	a.bindPeerEntry.SetText(bind.Peer)
	a.bindServiceEntry.SetText(bind.Service)
	a.bindLocalEntry.SetText(bind.Local)
	setServiceProtocolSelection(a.bindProtocol, bind.Protocol)
}

func (a *App) quickBindDiscoveredService() error {
	selected := strings.TrimSpace(a.discoveredSelect.Selected)
	if selected == "" {
		return errors.New("select a discovered service first")
	}
	service, ok := a.discoveredServiceByLabel(selected)
	if !ok {
		return errors.New("selected discovered service is no longer available")
	}

	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	a.mu.Unlock()
	baseName := service.DeviceName + "-" + service.ServiceName
	if service.normalizedProtocol() != config.ServiceProtocolUDP {
		baseName += "-" + service.normalizedProtocol()
	}
	name := uniqueConfigKey(sanitizeConfigKey(baseName), binds)
	binds[name] = config.BindConfig{
		Protocol: service.normalizedProtocol(),
		Peer:     service.DeviceName,
		Service:  service.ServiceName,
		Local:    "127.0.0.1:0",
	}
	return a.persistServiceDrafts(publish, binds, a.t("config_applied"))
}

func (a *App) persistServiceDrafts(publish map[string]config.PublishConfig, binds map[string]config.BindConfig, successMessage string) error {
	cfg, err := a.draftConfigFromFormWithServices(publish, binds)
	if err != nil {
		return err
	}
	if err := config.ValidateClientConfig(&cfg); err != nil {
		return err
	}
	if err := config.SaveClientConfig(a.cfg.ConfigPath, cfg); err != nil {
		return err
	}
	a.fillConfigForm(cfg, true)
	if err := a.restartClientIfRunning(); err != nil {
		return err
	}
	a.publishSelect.SetSelected("")
	setServiceProtocolSelection(a.publishProtocol, config.ServiceProtocolUDP)
	a.publishNameEntry.SetText("")
	a.publishLocalEntry.SetText("")
	a.bindSelect.SetSelected("")
	setServiceProtocolSelection(a.bindProtocol, config.ServiceProtocolUDP)
	a.bindNameEntry.SetText("")
	a.bindPeerEntry.SetText("")
	a.bindServiceEntry.SetText("")
	a.bindLocalEntry.SetText("")
	if a.cfg.RuntimeManager.Snapshot().State == "running" {
		a.setMessage(a.t("config_applied_restart"))
	} else {
		a.setMessage(successMessage)
	}
	a.triggerRefresh()
	return nil
}

func (a *App) kickDevice(req proto.KickDeviceRequest) error {
	if strings.TrimSpace(req.DeviceName) == "" && strings.TrimSpace(req.DeviceID) == "" {
		return errors.New(a.t("device_name_or_id_required"))
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
				a.setMessage(a.t("refresh_failed"))
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
			logsText = a.t("logs_none")
		}

		discovered := buildDiscoveredServices(&overview, draftCfg.DeviceName)

		fyne.Do(func() {
			a.mu.Lock()
			a.currentOverview = &overview
			a.discovered = discovered
			a.mu.Unlock()
			a.overviewGrid.SetText(control.RenderOverview(overview))
			a.statusGrid.SetText(statusText)
			a.peersGrid.SetText(peersText)
			a.routesGrid.SetText(routesText)
			a.traceGrid.SetText(traceText)
			a.networkGrid.SetText(networkText)
			a.logsGrid.SetText(logsText)
			a.logsGrid.ScrollToBottom()
			a.updateServiceViews()
			a.lastRefreshLabel.SetText(a.t("last_refresh") + ": " + time.Now().Format(time.RFC3339))
			a.setMessage(a.t("refreshed"))
			if a.refreshDoneHook != nil {
				a.refreshDoneHook()
			}
		})
	}()
}

func (a *App) updateServiceViews() {
	a.mu.Lock()
	publish := clonePublishMap(a.draftPublish)
	binds := cloneBindMap(a.draftBinds)
	discovered := append([]discoveredService(nil), a.discovered...)
	a.mu.Unlock()

	a.publishGrid.SetText(renderPublishConfigs(publish, a.t("publish_none")))
	a.bindGrid.SetText(renderBindConfigs(binds, a.t("bind_none")))
	a.discoveredGrid.SetText(renderDiscoveredServices(discovered, a.t("discovered_none")))

	setSelectOptions(a.publishSelect, sortedPublishNames(publish))
	setSelectOptions(a.bindSelect, sortedBindNames(binds))

	options := make([]string, 0, len(discovered))
	for _, item := range discovered {
		options = append(options, item.optionLabel())
	}
	sort.Strings(options)
	setSelectOptions(a.discoveredSelect, options)
}

func (a *App) discoveredServiceByLabel(label string) (discoveredService, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range a.discovered {
		if item.optionLabel() == label {
			return item, true
		}
	}
	return discoveredService{}, false
}

func (a *App) setMessage(message string) {
	a.messageLabel.SetText(message)
}

func (a *App) showError(err error) {
	a.setMessage(err.Error())
	dialog.ShowError(err, a.window)
}

func newDisplayGrid(initial string) *widget.TextGrid {
	grid := widget.NewTextGrid()
	grid.Scroll = fyne.ScrollBoth
	grid.SetText(initial)
	return grid
}

func mustPrettyJSON(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("encode_failed: %v", err)
	}
	return string(raw)
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

func renderPublishConfigs(values map[string]config.PublishConfig, empty string) string {
	if len(values) == 0 {
		return empty
	}
	names := sortedPublishNames(values)
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROTOCOL\tLOCAL")
	for _, name := range names {
		publish := values[name]
		protocol := strings.TrimSpace(publish.Protocol)
		if protocol == "" {
			protocol = config.ServiceProtocolUDP
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, protocol, publish.Local)
	}
	_ = tw.Flush()
	return out.String()
}

func renderBindConfigs(values map[string]config.BindConfig, empty string) string {
	if len(values) == 0 {
		return empty
	}
	names := sortedBindNames(values)
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROTOCOL\tPEER\tSERVICE\tLOCAL")
	for _, name := range names {
		bind := values[name]
		protocol := strings.TrimSpace(bind.Protocol)
		if protocol == "" {
			protocol = config.ServiceProtocolUDP
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, protocol, bind.Peer, bind.Service, bind.Local)
	}
	_ = tw.Flush()
	return out.String()
}

func renderDiscoveredServices(values []discoveredService, empty string) string {
	if len(values) == 0 {
		return empty
	}
	sorted := append([]discoveredService(nil), values...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].DeviceName == sorted[j].DeviceName {
			if sorted[i].ServiceName == sorted[j].ServiceName {
				return sorted[i].normalizedProtocol() < sorted[j].normalizedProtocol()
			}
			return sorted[i].ServiceName < sorted[j].ServiceName
		}
		return sorted[i].DeviceName < sorted[j].DeviceName
	})

	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tSERVICE\tPROTOCOL\tDEVICE_ID")
	for _, item := range sorted {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.DeviceName, item.ServiceName, item.normalizedProtocol(), emptyDash(item.DeviceID))
	}
	_ = tw.Flush()
	return out.String()
}

func buildDiscoveredServices(overview *control.Overview, selfName string) []discoveredService {
	if overview == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]discoveredService, 0)
	addServiceDetails := func(deviceID, deviceName string, services []proto.ServiceInfo) {
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
			out = append(out, discoveredService{
				DeviceID:    deviceID,
				DeviceName:  deviceName,
				ServiceName: name,
				Protocol:    protocol,
			})
		}
	}
	addServiceNames := func(deviceID, deviceName string, services []string) {
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
		addServiceDetails(deviceID, deviceName, details)
	}

	if overview.Status != nil {
		for _, peer := range overview.Status.Peers {
			if len(peer.ServiceDetails) > 0 {
				addServiceDetails(peer.DeviceID, peer.DeviceName, peer.ServiceDetails)
				continue
			}
			addServiceNames(peer.DeviceID, peer.DeviceName, peer.Services)
		}
	}
	if overview.Network != nil {
		for _, device := range overview.Network.Devices {
			if len(device.ServiceDetails) > 0 {
				addServiceDetails(device.DeviceID, device.DeviceName, device.ServiceDetails)
				continue
			}
			addServiceNames(device.DeviceID, device.DeviceName, device.Services)
		}
	}
	return out
}

func sortedPublishNames(values map[string]config.PublishConfig) []string {
	out := make([]string, 0, len(values))
	for name := range values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedBindNames(values map[string]config.BindConfig) []string {
	out := make([]string, 0, len(values))
	for name := range values {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func setSelectOptions(selectWidget *widget.Select, options []string) {
	current := selectWidget.Selected
	selectWidget.SetOptions(options)
	if current != "" {
		for _, option := range options {
			if option == current {
				selectWidget.SetSelected(current)
				return
			}
		}
	}
	selectWidget.ClearSelected()
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

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func (a *App) selectedLogLevel() string {
	level := strings.TrimSpace(a.logLevelSelect.Selected)
	if level == "" {
		return config.LogLevelInfo
	}
	return level
}
