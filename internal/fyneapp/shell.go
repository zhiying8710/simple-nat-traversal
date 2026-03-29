package fyneapp

import (
	"image/color"
	"strings"

	fyne "fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
)

const (
	pageDashboard   = "dashboard"
	pageServices    = "services"
	pageDevices     = "devices"
	pageDiagnostics = "diagnostics"
	pageSettings    = "settings"
)

var (
	lightPanelFill   = color.NRGBA{R: 0xFB, G: 0xF8, B: 0xF3, A: 0xFF}
	lightPanelStroke = color.NRGBA{R: 0xD8, G: 0xC9, B: 0xB7, A: 0xFF}
	darkPanelFill    = color.NRGBA{R: 0x10, G: 0x22, B: 0x37, A: 0xFF}
	darkPanelStroke  = color.NRGBA{R: 0x2C, G: 0x45, B: 0x62, A: 0xFF}
)

func (a *App) initWidgets() {
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
	a.dashboardPublishPreview = newDisplayGrid(a.t("publish_none"))
	a.dashboardBindPreview = newDisplayGrid(a.t("bind_none"))
	a.dashboardDiscoveredPreview = newDisplayGrid(a.t("discovered_none"))
	a.eventsGrid = newDisplayGrid(a.t("loading"))
	a.logTailGrid = newDisplayGrid(a.t("logs_none"))

	a.pageTitleLabel = newTitleLabel(a.t("tab_overview"))
	a.pageIntroLabel = newMutedWrapLabel(a.t("page_overview_intro"))
	a.lastRefreshLabel = newMutedWrapLabel(a.t("last_refresh") + ": -")
	a.messageLabel = newWrapLabel(a.t("ready"))
	a.messageLabel.Importance = widget.HighImportance

	a.sidebarStatusLabel = newWrapLabel(a.t("ready"))
	a.sidebarStatusLabel.Importance = widget.HighImportance
	a.sidebarDeviceLabel = newMutedWrapLabel(a.t("sidebar_device_pending"))

	a.heroTitleLabel = newTitleLabel(a.t("hero_idle_title"))
	a.heroDetailLabel = newMutedWrapLabel(a.t("hero_idle_detail"))
	a.runtimeSummaryLabel = newWrapLabel(a.t("loading"))
	a.serviceSummaryLabel = newWrapLabel(a.t("loading"))
	a.alertSummaryLabel = newWrapLabel(a.t("loading"))
	a.topologySummaryLabel = newWrapLabel(a.t("loading"))
	a.alertsDetailLabel = newWrapLabel(a.t("loading"))

	a.serverURLEntry = widget.NewEntry()
	a.serverURLEntry.SetPlaceHolder(a.t("server_url_placeholder"))
	a.allowInsecureCheck = widget.NewCheck(a.t("allow_insecure_http"), nil)

	a.passwordEntry = widget.NewPasswordEntry()
	a.passwordEntry.SetPlaceHolder(a.t("password_saved"))
	a.clearPasswordCheck = widget.NewCheck(a.t("clear_password"), nil)
	a.passwordStatusLabel = newMutedWrapLabel(a.t("password_missing"))

	a.adminPasswordEntry = widget.NewPasswordEntry()
	a.adminPasswordEntry.SetPlaceHolder(a.t("admin_password_saved"))
	a.clearAdminPassCheck = widget.NewCheck(a.t("clear_admin_password"), nil)
	a.adminPassStatusLabel = newMutedWrapLabel(a.t("admin_password_missing"))

	a.deviceNameEntry = widget.NewEntry()
	a.deviceHintLabel = newMutedWrapLabel(a.t("device_name_hint"))
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
}

func (a *App) buildShell() fyne.CanvasObject {
	a.pages = map[string]fyne.CanvasObject{
		pageDashboard:   a.buildDashboardPage(),
		pageServices:    a.buildServicesPage(),
		pageDevices:     a.buildDevicesPage(),
		pageDiagnostics: a.buildDiagnosticsPage(),
		pageSettings:    a.buildSettingsPage(),
	}
	a.navButtons = map[string]*widget.Button{
		pageDashboard:   a.newNavButton(pageDashboard, a.t("tab_overview"), theme.HomeIcon()),
		pageServices:    a.newNavButton(pageServices, a.t("tab_services"), theme.StorageIcon()),
		pageDevices:     a.newNavButton(pageDevices, a.t("tab_network"), theme.ComputerIcon()),
		pageDiagnostics: a.newNavButton(pageDiagnostics, a.t("tab_diagnostics"), theme.InfoIcon()),
		pageSettings:    a.newNavButton(pageSettings, a.t("tab_connection"), theme.SettingsIcon()),
	}
	a.contentHost = container.NewMax(a.pages[pageDashboard])

	center := container.NewBorder(a.buildPageHeader(), nil, nil, nil, container.NewPadded(a.contentHost))
	body := container.NewBorder(nil, nil, a.buildSidebar(), a.buildInsightRail(), center)

	background := canvas.NewRectangle(theme.Color(theme.ColorNameBackground))
	return container.NewStack(background, body)
}

func (a *App) buildPageHeader() fyne.CanvasObject {
	refreshButton := widget.NewButtonWithIcon(a.t("refresh"), theme.ViewRefreshIcon(), func() {
		a.refreshAll()
	})
	refreshButton.Importance = widget.HighImportance
	reloadButton := widget.NewButtonWithIcon(a.t("reload_config"), theme.ViewRefreshIcon(), func() {
		a.reloadConfig()
	})

	left := container.NewVBox(a.pageTitleLabel, a.pageIntroLabel)
	right := container.NewVBox(
		container.NewHBox(refreshButton, reloadButton),
		a.lastRefreshLabel,
	)
	content := container.NewVBox(
		container.NewBorder(nil, nil, nil, right, left),
		widget.NewSeparator(),
		a.messageLabel,
	)
	return newSurfacePanel("", "", content, sntAccentWarm, lightPanelStroke, nil)
}

func (a *App) buildSidebar() fyne.CanvasObject {
	icon := canvas.NewImageFromResource(appIconResource())
	icon.FillMode = canvas.ImageFillContain
	icon.SetMinSize(fyne.NewSize(42, 42))

	title := newTitleLabel(a.t("app_title"))
	subtitle := newMutedWrapLabel(a.t("app_subtitle"))
	configPathLabel := newMutedBreakLabel(a.t("config_path") + "\n" + a.cfg.ConfigPath)

	header := container.NewVBox(
		container.NewBorder(nil, nil, icon, nil, container.NewVBox(title, subtitle)),
		widget.NewSeparator(),
	)
	nav := container.NewVBox(
		a.navButtons[pageDashboard],
		a.navButtons[pageServices],
		a.navButtons[pageDevices],
		a.navButtons[pageDiagnostics],
		a.navButtons[pageSettings],
	)
	footer := container.NewVBox(
		widget.NewSeparator(),
		a.sidebarStatusLabel,
		a.sidebarDeviceLabel,
		configPathLabel,
	)

	content := container.NewBorder(header, footer, nil, nil, container.NewVBox(nav, layout.NewSpacer()))
	sidebar := container.NewPadded(container.NewThemeOverride(content, newSNTPanelTheme()))

	background := canvas.NewRectangle(darkPanelFill)
	background.StrokeColor = darkPanelStroke
	background.StrokeWidth = 1
	background.SetMinSize(fyne.NewSize(280, 0))
	return container.NewStack(background, sidebar)
}

func (a *App) buildInsightRail() fyne.CanvasObject {
	headerTitle := newTitleLabel(a.t("insight_title"))
	headerSubtitle := newMutedWrapLabel(a.t("insight_intro"))

	content := container.NewVBox(
		headerTitle,
		headerSubtitle,
		newSurfacePanel(a.t("insight_topology_title"), a.t("insight_topology_intro"), a.topologySummaryLabel, lightPanelFill, lightPanelStroke, nil),
		newSurfacePanel(a.t("insight_alerts_title"), a.t("insight_alerts_intro"), a.alertsDetailLabel, lightPanelFill, lightPanelStroke, nil),
		newSurfacePanel(a.t("insight_events_title"), a.t("insight_events_intro"), withMinHeight(a.eventsGrid, 160), lightPanelFill, lightPanelStroke, nil),
		newSurfacePanel(a.t("insight_logs_title"), a.t("insight_logs_intro"), withMinHeight(a.logTailGrid, 180), lightPanelFill, lightPanelStroke, nil),
	)

	scroll := container.NewScroll(content)
	background := canvas.NewRectangle(color.NRGBA{R: 0xF1, G: 0xE8, B: 0xDE, A: 0xFF})
	background.StrokeColor = lightPanelStroke
	background.StrokeWidth = 1
	background.SetMinSize(fyne.NewSize(360, 0))
	return container.NewStack(background, container.NewPadded(scroll))
}

func (a *App) buildDashboardPage() fyne.CanvasObject {
	refreshButton := widget.NewButtonWithIcon(a.t("refresh"), theme.ViewRefreshIcon(), func() {
		a.refreshAll()
	})
	refreshButton.Importance = widget.HighImportance
	servicesButton := widget.NewButtonWithIcon(a.t("dashboard_services_action"), theme.StorageIcon(), func() {
		a.switchPage(pageServices)
	})
	diagnosticsButton := widget.NewButtonWithIcon(a.t("dashboard_diagnostics_action"), theme.InfoIcon(), func() {
		a.switchPage(pageDiagnostics)
	})
	settingsButton := widget.NewButtonWithIcon(a.t("dashboard_settings_action"), theme.SettingsIcon(), func() {
		a.switchPage(pageSettings)
	})

	heroActions := container.NewVBox(refreshButton, servicesButton, diagnosticsButton, settingsButton)
	heroContent := container.NewBorder(
		nil,
		nil,
		nil,
		heroActions,
		container.NewVBox(a.heroTitleLabel, a.heroDetailLabel),
	)

	runtimeCard := newSurfacePanel(a.t("dashboard_runtime_title"), a.t("dashboard_runtime_intro"), a.runtimeSummaryLabel, color.NRGBA{R: 0xF4, G: 0xE2, B: 0xCF, A: 0xFF}, lightPanelStroke, nil)
	servicesCard := newSurfacePanel(a.t("dashboard_services_title"), a.t("dashboard_services_intro"), a.serviceSummaryLabel, color.NRGBA{R: 0xE4, G: 0xEC, B: 0xE5, A: 0xFF}, lightPanelStroke, nil)
	alertsCard := newSurfacePanel(a.t("dashboard_alerts_title"), a.t("dashboard_alerts_intro"), a.alertSummaryLabel, color.NRGBA{R: 0xF3, G: 0xDE, B: 0xD6, A: 0xFF}, lightPanelStroke, nil)

	publishPanel := newSurfacePanel(
		a.t("section_publish"),
		a.t("dashboard_publish_intro"),
		container.NewVBox(
			withMinHeight(a.dashboardPublishPreview, 170),
			widget.NewButtonWithIcon(a.t("dashboard_services_action"), theme.NavigateNextIcon(), func() {
				a.switchPage(pageServices)
			}),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)
	bindPanel := newSurfacePanel(
		a.t("section_bind"),
		a.t("dashboard_bind_intro"),
		container.NewVBox(
			withMinHeight(a.dashboardBindPreview, 170),
			widget.NewButtonWithIcon(a.t("dashboard_services_action"), theme.NavigateNextIcon(), func() {
				a.switchPage(pageServices)
			}),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)
	discoveredPanel := newSurfacePanel(
		a.t("section_discovered"),
		a.t("auto_bind_note"),
		container.NewVBox(
			withMinHeight(a.dashboardDiscoveredPreview, 180),
			func() fyne.CanvasObject {
				a.dashboardDiscoveredAction = widget.NewButtonWithIcon(a.t("dashboard_services_action"), theme.NavigateNextIcon(), func() {
					a.switchPage(pageServices)
				})
				return a.dashboardDiscoveredAction
			}(),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	content := container.NewVBox(
		newSurfacePanel(a.t("dashboard_console_title"), a.t("dashboard_console_intro"), heroContent, darkPanelFill, darkPanelStroke, newSNTPanelTheme()),
		container.NewGridWithColumns(3, runtimeCard, servicesCard, alertsCard),
		container.NewGridWithColumns(2, publishPanel, bindPanel),
		discoveredPanel,
	)
	return container.NewScroll(content)
}

func (a *App) buildServicesPage() fyne.CanvasObject {
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
	quickBindButton.Importance = widget.HighImportance

	publishPanel := newSurfacePanel(
		a.t("section_publish"),
		a.t("dashboard_publish_intro"),
		container.NewVBox(
			withMinHeight(a.publishGrid, 220),
			a.publishSelect,
			widget.NewForm(
				widget.NewFormItem(a.t("service_protocol"), a.publishProtocol),
				widget.NewFormItem(a.t("publish_name"), a.publishNameEntry),
				widget.NewFormItem(a.t("publish_local"), a.publishLocalEntry),
			),
			publishActions,
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	discoveredPanel := newSurfacePanel(
		a.t("section_discovered"),
		a.t("auto_bind_note"),
		container.NewVBox(
			withMinHeight(a.discoveredGrid, 220),
			a.discoveredSelect,
			quickBindButton,
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	bindPanel := newSurfacePanel(
		a.t("section_bind"),
		a.t("dashboard_bind_intro"),
		container.NewVBox(
			withMinHeight(a.bindGrid, 220),
			a.bindSelect,
			widget.NewForm(
				widget.NewFormItem(a.t("service_protocol"), a.bindProtocol),
				widget.NewFormItem(a.t("bind_name"), a.bindNameEntry),
				widget.NewFormItem(a.t("bind_peer"), a.bindPeerEntry),
				widget.NewFormItem(a.t("bind_service"), a.bindServiceEntry),
				widget.NewFormItem(a.t("bind_local"), a.bindLocalEntry),
			),
			bindActions,
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	return container.NewScroll(container.NewVBox(
		container.NewGridWithColumns(2, publishPanel, discoveredPanel),
		bindPanel,
	))
}

func (a *App) buildDevicesPage() fyne.CanvasObject {
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

	networkPanel := newSurfacePanel(
		a.t("section_network_devices"),
		a.t("devices_network_intro"),
		withMinHeight(a.networkGrid, 360),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)
	actionsPanel := newSurfacePanel(
		a.t("section_kick"),
		a.t("devices_kick_intro"),
		container.NewVBox(
			newActionRow(a.kickDeviceNameEntry, kickByNameButton),
			newActionRow(a.kickDeviceIDEntry, kickByIDButton),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	return container.NewScroll(container.NewVBox(networkPanel, actionsPanel))
}

func (a *App) buildDiagnosticsPage() fyne.CanvasObject {
	diagnosticsTabs := container.NewAppTabs(
		container.NewTabItem(a.t("tab_status"), container.NewBorder(widget.NewLabel(a.t("raw_status")), nil, nil, nil, a.statusGrid)),
		container.NewTabItem(a.t("tab_peers"), a.peersGrid),
		container.NewTabItem(a.t("tab_routes"), a.routesGrid),
		container.NewTabItem(a.t("tab_trace"), a.traceGrid),
		container.NewTabItem(a.t("tab_logs"), a.logsGrid),
	)

	overviewPanel := newSurfacePanel(
		a.t("tab_overview"),
		a.t("diagnostics_overview_intro"),
		withMinHeight(a.overviewGrid, 520),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)
	tabsPanel := newSurfacePanel(
		a.t("diagnostics_tabs_title"),
		a.t("diagnostics_tabs_intro"),
		withMinHeight(diagnosticsTabs, 520),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	split := container.NewHSplit(overviewPanel, tabsPanel)
	split.Offset = 0.34
	return split
}

func (a *App) buildSettingsPage() fyne.CanvasObject {
	saveConfigButton := widget.NewButtonWithIcon(a.t("save_config"), theme.DocumentSaveIcon(), func() {
		if err := a.saveConfig(); err != nil {
			a.showError(err)
		}
	})
	saveConfigButton.Importance = widget.HighImportance
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

	connectionPanel := newSurfacePanel(
		a.t("section_connection"),
		a.t("page_connection_intro"),
		widget.NewForm(
			widget.NewFormItem(a.t("server_url"), a.serverURLEntry),
			widget.NewFormItem("", a.allowInsecureCheck),
			widget.NewFormItem(a.t("device_name"), a.deviceNameEntry),
			widget.NewFormItem("", a.deviceHintLabel),
			widget.NewFormItem("", a.autoConnectCheck),
			widget.NewFormItem(a.t("udp_listen"), a.udpListenEntry),
			widget.NewFormItem(a.t("admin_listen"), a.adminListenEntry),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	credentialsPanel := newSurfacePanel(
		a.t("section_credentials"),
		a.t("page_credentials_intro"),
		widget.NewForm(
			widget.NewFormItem(a.t("password"), a.passwordEntry),
			widget.NewFormItem("", a.clearPasswordCheck),
			widget.NewFormItem("", a.passwordStatusLabel),
			widget.NewFormItem(a.t("admin_password"), a.adminPasswordEntry),
			widget.NewFormItem("", a.clearAdminPassCheck),
			widget.NewFormItem("", a.adminPassStatusLabel),
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	runtimePanel := newSurfacePanel(
		a.t("section_runtime"),
		a.t("page_runtime_intro"),
		container.NewVBox(
			container.NewHBox(saveConfigButton, startButton, stopButton),
			container.NewHBox(installAutostartButton, uninstallAutostartButton),
			widget.NewForm(widget.NewFormItem(a.t("log_level"), a.logLevelSelect)),
			applyLogLevelButton,
		),
		lightPanelFill,
		lightPanelStroke,
		nil,
	)

	return container.NewScroll(container.NewVBox(
		connectionPanel,
		credentialsPanel,
		runtimePanel,
	))
}

func (a *App) newNavButton(page, label string, icon fyne.Resource) *widget.Button {
	button := widget.NewButtonWithIcon(label, icon, func() {
		a.switchPage(page)
	})
	button.Alignment = widget.ButtonAlignLeading
	button.Importance = widget.LowImportance
	return button
}

func (a *App) switchPage(page string) {
	content, ok := a.pages[page]
	if !ok {
		page = pageDashboard
		content = a.pages[pageDashboard]
	}
	if a.contentHost != nil {
		a.contentHost.Objects = []fyne.CanvasObject{content}
		a.contentHost.Refresh()
	}
	a.activePage = page
	title, intro := a.pageMeta(page)
	if a.pageTitleLabel != nil {
		a.pageTitleLabel.SetText(title)
	}
	if a.pageIntroLabel != nil {
		a.pageIntroLabel.SetText(intro)
	}
	a.refreshNavButtons()
}

func (a *App) refreshNavButtons() {
	for page, button := range a.navButtons {
		if button == nil {
			continue
		}
		if page == a.activePage {
			button.Importance = widget.HighImportance
		} else {
			button.Importance = widget.LowImportance
		}
		button.Refresh()
	}
}

func (a *App) pageMeta(page string) (string, string) {
	switch page {
	case pageServices:
		return a.t("tab_services"), a.t("page_services_intro")
	case pageDevices:
		return a.t("tab_network"), a.t("page_network_intro")
	case pageDiagnostics:
		return a.t("tab_diagnostics"), a.t("page_diagnostics_intro")
	case pageSettings:
		return a.t("tab_connection"), a.t("page_connection_intro")
	default:
		return a.t("tab_overview"), a.t("page_overview_intro")
	}
}

func newSurfacePanel(title, subtitle string, content fyne.CanvasObject, fill, stroke color.Color, override fyne.Theme) fyne.CanvasObject {
	body := content
	if strings.TrimSpace(title) != "" || strings.TrimSpace(subtitle) != "" {
		header := []fyne.CanvasObject{
			newTitleLabel(title),
		}
		if strings.TrimSpace(subtitle) != "" {
			header = append(header, newMutedWrapLabel(subtitle))
		}
		header = append(header, widget.NewSeparator(), content)
		body = container.NewVBox(header...)
	}
	if override != nil {
		body = container.NewThemeOverride(body, override)
	}

	background := canvas.NewRectangle(fill)
	background.StrokeColor = stroke
	background.StrokeWidth = 1
	background.CornerRadius = 22
	return container.NewStack(background, container.NewPadded(body))
}

func newActionRow(input fyne.CanvasObject, action fyne.CanvasObject) fyne.CanvasObject {
	return container.NewBorder(nil, nil, nil, action, input)
}

func newTitleLabel(text string) *widget.Label {
	label := widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	label.Wrapping = fyne.TextWrapOff
	label.Truncation = fyne.TextTruncateEllipsis
	return label
}

func newWrapLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

func newMutedWrapLabel(text string) *widget.Label {
	label := newWrapLabel(text)
	label.Importance = widget.LowImportance
	return label
}

func newMutedBreakLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapBreak
	label.Importance = widget.LowImportance
	return label
}

func withMinHeight(obj fyne.CanvasObject, height float32) fyne.CanvasObject {
	spacer := canvas.NewRectangle(color.Transparent)
	spacer.SetMinSize(fyne.NewSize(0, height))
	return container.NewStack(spacer, obj)
}
