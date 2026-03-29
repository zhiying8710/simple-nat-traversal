package fyneapp

import (
	"fmt"
	"strings"
	"time"

	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/control"
)

func (a *App) updateShellSummary(overview control.Overview, runtimeStatus control.RuntimeStatus, logsText string) {
	if a.sidebarStatusLabel != nil {
		a.sidebarStatusLabel.SetText(fmt.Sprintf("%s: %s", a.t("runtime_state"), a.localizedRuntimeState(runtimeStatus.State)))
	}
	if a.sidebarDeviceLabel != nil {
		deviceName := "-"
		deviceID := "-"
		if overview.Status != nil {
			deviceName = valueOrDash(overview.Status.DeviceName)
			deviceID = valueOrDash(overview.Status.DeviceID)
		} else if overview.Config != nil {
			deviceName = valueOrDash(overview.Config.DeviceName)
		}
		a.sidebarDeviceLabel.SetText(fmt.Sprintf("%s: %s\n%s: %s", a.t("device_name"), deviceName, a.t("table_device_id"), deviceID))
	}
	if a.heroTitleLabel != nil && a.heroDetailLabel != nil {
		title, detail := a.heroContent(overview, runtimeStatus)
		a.heroTitleLabel.SetText(title)
		a.heroDetailLabel.SetText(detail)
	}
	if a.runtimeSummaryLabel != nil {
		a.runtimeSummaryLabel.SetText(a.renderRuntimeSummary(overview, runtimeStatus))
	}
	if a.serviceSummaryLabel != nil {
		a.serviceSummaryLabel.SetText(a.renderServiceSummary(overview))
	}
	if a.alertSummaryLabel != nil {
		a.alertSummaryLabel.SetText(a.renderAlertSummary(overview, runtimeStatus))
	}
	if a.topologySummaryLabel != nil {
		a.topologySummaryLabel.SetText(a.renderTopologySummary(overview))
	}
	if a.alertsDetailLabel != nil {
		a.alertsDetailLabel.SetText(a.renderAlertDetails(overview, runtimeStatus))
	}
	if a.eventsGrid != nil {
		a.eventsGrid.SetText(a.renderRecentEvents(overview.Status, 6))
	}
	if a.logTailGrid != nil {
		a.logTailGrid.SetText(tailLines(logsText, 8))
	}
}

func (a *App) heroContent(overview control.Overview, runtimeStatus control.RuntimeStatus) (string, string) {
	if issues := a.collectIssues(overview, runtimeStatus); len(issues) > 0 {
		return a.t("hero_attention_title"), issues[0]
	}

	if overview.ConfigValid && runtimeStatus.State == "running" && overview.Status != nil {
		return a.t("hero_running_title"), fmt.Sprintf(
			"%s: %s\n%s: %s\n%s: %s",
			a.t("device_name"),
			valueOrDash(overview.Status.DeviceName),
			a.t("overview_public_udp"),
			valueOrDash(overview.Status.ObservedAddr),
			a.t("overview_network_state"),
			a.localizedStatusValue(overview.Status.NetworkState),
		)
	}

	if overview.ConfigValid && overview.Config != nil {
		return a.t("hero_idle_title"), fmt.Sprintf(
			"%s: %s\n%s: %s",
			a.t("server_url"),
			valueOrDash(overview.Config.ServerURL),
			a.t("device_name"),
			valueOrDash(overview.Config.DeviceName),
		)
	}

	return a.t("hero_setup_title"), a.t("hero_idle_detail")
}

func (a *App) renderRuntimeSummary(overview control.Overview, runtimeStatus control.RuntimeStatus) string {
	lines := []string{
		fmt.Sprintf("%s: %s", a.t("runtime_state"), a.localizedRuntimeState(runtimeStatus.State)),
	}

	if !runtimeStatus.StartedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("%s: %s", a.t("dashboard_runtime_started"), runtimeStatus.StartedAt.Format(time.RFC3339)))
	}
	if overview.Status != nil {
		lines = append(lines,
			fmt.Sprintf("%s: %s", a.t("overview_public_udp"), valueOrDash(overview.Status.ObservedAddr)),
			fmt.Sprintf("%s: %s", a.t("overview_network_state"), a.localizedStatusValue(overview.Status.NetworkState)),
			fmt.Sprintf("%s: %d", a.t("overview_peer_count"), len(overview.Status.Peers)),
		)
	} else if !ignoreDashboardRuntimeFetchErrors(runtimeStatus) {
		statusDetail := strings.TrimSpace(a.localizeErrorText(overview.StatusError))
		if statusDetail == "" {
			statusDetail = a.t("status_unavailable")
		}
		lines = append(lines, fmt.Sprintf("%s: %s", a.t("overview_status_error"), statusDetail))
	}

	if runtimeStatus.LastError != "" {
		lines = append(lines, fmt.Sprintf("%s: %s", a.t("dashboard_runtime_last_error"), a.localizeErrorText(runtimeStatus.LastError)))
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderServiceSummary(overview control.Overview) string {
	publishCount := 0
	bindCount := 0
	discoveredCount := len(buildDiscoveredServices(&overview, preferredOverviewDeviceName(overview)))
	activeProxies := 0
	if overview.Config != nil {
		publishCount = len(overview.Config.Publish)
		bindCount = len(overview.Config.Binds)
	}
	if overview.Status != nil {
		activeProxies = overview.Status.ActiveServiceProxies
	}
	lines := []string{
		fmt.Sprintf("%s: %d", a.t("overview_publish_count"), publishCount),
		fmt.Sprintf("%s: %d", a.t("overview_bind_count"), bindCount),
		fmt.Sprintf("%s: %d", a.t("dashboard_discovered_count"), discoveredCount),
		fmt.Sprintf("%s: %d", a.t("dashboard_active_proxies"), activeProxies),
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderAlertSummary(overview control.Overview, runtimeStatus control.RuntimeStatus) string {
	issues := a.collectIssues(overview, runtimeStatus)
	if len(issues) == 0 {
		return a.t("dashboard_alerts_clear")
	}
	if len(issues) > 4 {
		issues = issues[:4]
	}
	return strings.Join(issues, "\n")
}

func (a *App) renderTopologySummary(overview control.Overview) string {
	deviceName := valueOrDash(preferredOverviewDeviceName(overview))
	deviceID := "-"
	peerCount := 0
	networkDevices := 0
	if overview.Status != nil {
		deviceID = valueOrDash(overview.Status.DeviceID)
		peerCount = len(overview.Status.Peers)
	}
	if overview.Network != nil {
		networkDevices = len(overview.Network.Devices)
	}
	publishCount := 0
	bindCount := 0
	if overview.Config != nil {
		publishCount = len(overview.Config.Publish)
		bindCount = len(overview.Config.Binds)
	}
	return strings.Join([]string{
		fmt.Sprintf("%s: %s", a.t("device_name"), deviceName),
		fmt.Sprintf("%s: %s", a.t("table_device_id"), deviceID),
		fmt.Sprintf("%s: %d", a.t("overview_peer_count"), peerCount),
		fmt.Sprintf("%s: %d", a.t("overview_network_devices"), networkDevices),
		fmt.Sprintf("%s / %s: %d / %d", a.t("section_publish"), a.t("section_bind"), publishCount, bindCount),
	}, "\n")
}

func (a *App) renderAlertDetails(overview control.Overview, runtimeStatus control.RuntimeStatus) string {
	issues := a.collectIssues(overview, runtimeStatus)
	if len(issues) == 0 {
		return a.t("dashboard_alerts_clear")
	}
	return strings.Join(issues, "\n\n")
}

func (a *App) renderRecentEvents(status *client.StatusSnapshot, limit int) string {
	if status == nil || len(status.RecentEvents) == 0 {
		return a.t("insight_events_empty")
	}

	events := status.RecentEvents
	if len(events) > limit {
		events = events[len(events)-limit:]
	}

	lines := make([]string, 0, len(events))
	for _, event := range events {
		scope := valueOrDash(event.Scope)
		peer := valueOrDash(event.PeerName)
		if peer == "-" {
			peer = valueOrDash(event.PeerID)
		}
		line := fmt.Sprintf("%s  %s  %s  %s", event.At.Format("15:04:05"), scope, peer, valueOrDash(event.Event))
		if strings.TrimSpace(event.Detail) != "" {
			line += "  " + event.Detail
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (a *App) localizedRuntimeState(state string) string {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "running":
		return a.t("runtime_state_running")
	case "starting":
		return a.t("runtime_state_starting")
	case "stopping":
		return a.t("runtime_state_stopping")
	case "stopped":
		return a.t("runtime_state_stopped")
	default:
		if strings.TrimSpace(state) == "" {
			return a.t("runtime_state_unknown")
		}
		return state
	}
}

func (a *App) collectIssues(overview control.Overview, runtimeStatus control.RuntimeStatus) []string {
	issues := make([]string, 0, 4)
	values := []string{
		a.localizeErrorText(overview.ConfigError),
		a.localizeErrorText(runtimeStatus.LastError),
	}
	if !ignoreDashboardRuntimeFetchErrors(runtimeStatus) {
		values = append(values, a.localizeErrorText(overview.StatusError), a.localizeErrorText(overview.NetworkError))
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		issues = append(issues, value)
	}
	return issues
}

func ignoreDashboardRuntimeFetchErrors(runtimeStatus control.RuntimeStatus) bool {
	return strings.EqualFold(strings.TrimSpace(runtimeStatus.State), "stopped")
}

func preferredOverviewDeviceName(overview control.Overview) string {
	if overview.Status != nil && strings.TrimSpace(overview.Status.DeviceName) != "" {
		return overview.Status.DeviceName
	}
	if overview.Config != nil {
		return strings.TrimSpace(overview.Config.DeviceName)
	}
	return ""
}

func tailLines(text string, limit int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
