package fyneapp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/control"
	"simple-nat-traversal/internal/proto"
)

func (a *App) mustPrettyJSON(value any) string {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf(a.t("encode_failed"), err)
	}
	return string(raw)
}

func (a *App) textOrMessageFromStatus(snapshot *client.StatusSnapshot, statusErr string, render func(client.StatusSnapshot) string) string {
	if snapshot == nil {
		if detail := strings.TrimSpace(a.localizeErrorText(statusErr)); detail != "" {
			return a.t("status_unavailable") + "\n" + detail
		}
		return a.t("status_unavailable")
	}
	return render(*snapshot)
}

func (a *App) textOrMessageFromNetwork(snapshot *proto.NetworkDevicesResponse, networkErr string, render func(proto.NetworkDevicesResponse) string) string {
	if snapshot == nil {
		if detail := strings.TrimSpace(a.localizeErrorText(networkErr)); detail != "" {
			return a.t("network_unavailable") + "\n" + detail
		}
		return a.t("network_unavailable")
	}
	return render(*snapshot)
}

func (a *App) renderOverview(overview control.Overview) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_generated_at"), overview.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_executable"), valueOrDash(overview.ExecutablePath))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_config"), valueOrDash(overview.ConfigPath))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_config_exists"), a.localizedYesNo(overview.ConfigExists))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_config_valid"), a.localizedYesNo(overview.ConfigValid))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_config_error"), valueOrDash(a.localizeErrorText(overview.ConfigError)))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_client_running"), a.localizedYesNo(overview.ClientRunning))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_status_error"), valueOrDash(a.localizeErrorText(overview.StatusError)))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_network_error"), valueOrDash(a.localizeErrorText(overview.NetworkError)))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_autostart_installed"), a.localizedYesNo(overview.Autostart.Installed))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_autostart_file"), valueOrDash(overview.Autostart.FilePath))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_autostart_error"), valueOrDash(a.localizeErrorText(overview.AutostartError)))

	if overview.Config != nil {
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_device_name"), valueOrDash(overview.Config.DeviceName))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_server_url"), valueOrDash(overview.Config.ServerURL))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_allow_insecure_http"), a.localizedYesNo(overview.Config.AllowInsecureHTTP))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_udp_listen"), valueOrDash(overview.Config.UDPListen))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_admin_listen"), valueOrDash(overview.Config.AdminListen))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_log_level"), valueOrDash(overview.Config.LogLevel))
		fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_publish_count"), len(overview.Config.Publish))
		fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_bind_count"), len(overview.Config.Binds))
	}
	if overview.Status != nil {
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_public_udp"), valueOrDash(overview.Status.ObservedAddr))
		fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_network_state"), a.localizedStatusValue(overview.Status.NetworkState))
		fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_peer_count"), len(overview.Status.Peers))
		fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_rejoin_count"), overview.Status.RejoinCount)
	}
	if overview.Network != nil {
		fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_network_devices"), len(overview.Network.Devices))
	}
	return out.String()
}

func (a *App) renderPublishConfigs(values map[string]config.PublishConfig, empty string) string {
	if len(values) == 0 {
		return empty
	}
	names := sortedPublishNames(values)
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\n", a.t("table_name"), a.t("table_protocol"), a.t("table_local"))
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

func (a *App) renderBindConfigs(values map[string]config.BindConfig, empty string) string {
	if len(values) == 0 {
		return empty
	}
	names := sortedBindNames(values)
	var out strings.Builder
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.t("table_name"), a.t("table_protocol"), a.t("table_peer"), a.t("table_service"), a.t("table_local"))
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

func (a *App) renderDiscoveredServices(values []discoveredService, empty string) string {
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
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.t("table_device"), a.t("table_service"), a.t("table_protocol"), a.t("table_device_id"))
	for _, item := range sorted {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.DeviceName, item.ServiceName, item.normalizedProtocol(), valueOrDash(item.DeviceID))
	}
	_ = tw.Flush()
	return out.String()
}

func (a *App) renderPeersStatus(snapshot client.StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s\t%s\n", a.t("device_name"), valueOrDash(snapshot.DeviceName))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("table_device_id"), valueOrDash(snapshot.DeviceID))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_local_udp"), valueOrDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_public_udp"), valueOrDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_server_udp"), valueOrDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_generated_at"), snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	if len(snapshot.Peers) == 0 {
		fmt.Fprintf(&out, "%s: %s\n", a.t("section_peers"), a.t("none_value"))
		return out.String()
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		a.t("table_peer"),
		a.t("table_state"),
		a.t("table_route"),
		a.t("table_reason"),
		a.t("table_services"),
		a.t("table_punch"),
		a.t("table_packets"),
		a.t("table_bytes"),
		a.t("table_last_seen"),
		a.t("table_error"),
	)
	for _, peer := range snapshot.Peers {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%d\t%d/%d\t%d/%d\t%s\t%s\n",
			valueOrDash(peer.DeviceName),
			a.localizedStatusValue(peer.State),
			valueOrDash(peer.ChosenAddr),
			valueOrDash(peer.RouteReason),
			joinListOrDash(peerServiceLabels(peer)),
			peer.PunchAttempts,
			peer.SentPackets,
			peer.RecvPackets,
			peer.SentBytes,
			peer.RecvBytes,
			formatTimesOrDash(peer.SessionLastSeen, peer.LastSeen),
			valueOrDash(a.localizeErrorText(peer.LastError)),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func (a *App) renderRoutesStatus(snapshot client.StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s\t%s\n", a.t("device_name"), valueOrDash(snapshot.DeviceName))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_local_udp"), valueOrDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_public_udp"), valueOrDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_server_udp"), valueOrDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_service_proxies"), snapshot.ActiveServiceProxies)
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_tcp_bind_streams"), len(snapshot.TCPBindStreams))
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_tcp_publish_proxies"), len(snapshot.TCPProxies))
	out.WriteString("\n")

	out.WriteString(a.t("section_publish") + "\n")
	if len(snapshot.Publish) == 0 {
		writeNoneBlock(&out, a.t("none_value"))
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\n", a.t("table_name"), a.t("table_protocol"), a.t("table_local"))
		for _, publish := range snapshot.Publish {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", valueOrDash(publish.Name), valueOrDash(publish.Protocol), valueOrDash(publish.Local))
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString(a.t("section_bind") + "\n")
	if len(snapshot.Binds) == 0 {
		writeNoneBlock(&out, a.t("none_value"))
	} else {
		peersByName := make(map[string]client.PeerStatus, len(snapshot.Peers))
		for _, peer := range snapshot.Peers {
			peersByName[peer.DeviceName] = peer
		}

		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.t("table_name"),
			a.t("table_protocol"),
			a.t("table_listen"),
			a.t("table_peer"),
			a.t("table_state"),
			a.t("table_route"),
			a.t("table_reason"),
			a.t("table_service"),
			a.t("table_sessions"),
		)
		for _, bind := range snapshot.Binds {
			state := a.localizedStatusValue("offline")
			route := "-"
			reason := "-"
			if peer, ok := peersByName[bind.Peer]; ok {
				state = a.localizedStatusValue(peer.State)
				route = valueOrDash(peer.ChosenAddr)
				reason = valueOrDash(peer.RouteReason)
			}
			fmt.Fprintf(
				tw,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
				valueOrDash(bind.Name),
				valueOrDash(bind.Protocol),
				valueOrDash(bind.ListenAddr),
				valueOrDash(bind.Peer),
				state,
				route,
				reason,
				valueOrDash(bind.Service),
				bind.ActiveSessions,
			)
		}
		_ = tw.Flush()
		out.WriteString("\n\n")
	}

	out.WriteString(a.t("section_tcp_bind_streams") + "\n")
	if len(snapshot.TCPBindStreams) == 0 {
		writeNoneBlock(&out, a.t("none_value"))
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.t("section_bind"),
			a.t("table_peer"),
			a.t("table_service"),
			a.t("table_session"),
			a.t("table_state"),
			a.t("table_started"),
			a.t("table_last_seen"),
			a.t("table_buffered_in"),
			a.t("table_unacked_out"),
		)
		for _, stream := range snapshot.TCPBindStreams {
			fmt.Fprintf(
				tw,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
				valueOrDash(stream.BindName),
				valueOrDash(firstNonEmpty(stream.PeerName, stream.PeerID)),
				valueOrDash(stream.Service),
				valueOrDash(stream.SessionID),
				a.localizedStatusValue(stream.State),
				formatTimesOrDash(stream.StartedAt),
				formatTimesOrDash(stream.LastSeen),
				stream.BufferedInbound,
				stream.UnackedOutbound,
			)
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString(a.t("section_tcp_publish_proxies") + "\n")
	if len(snapshot.TCPProxies) == 0 {
		out.WriteString("  " + a.t("none_value") + "\n")
		return out.String()
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		a.t("table_peer"),
		a.t("section_bind"),
		a.t("table_service"),
		a.t("table_session"),
		a.t("table_state"),
		a.t("table_target"),
		a.t("table_started"),
		a.t("table_last_seen"),
		a.t("table_buffered_in"),
		a.t("table_unacked_out"),
	)
	for _, proxy := range snapshot.TCPProxies {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
			valueOrDash(firstNonEmpty(proxy.PeerName, proxy.PeerID)),
			valueOrDash(proxy.BindName),
			valueOrDash(proxy.Service),
			valueOrDash(proxy.SessionID),
			a.localizedStatusValue(proxy.State),
			valueOrDash(proxy.Target),
			formatTimesOrDash(proxy.StartedAt),
			formatTimesOrDash(proxy.LastSeen),
			proxy.BufferedInbound,
			proxy.UnackedOutbound,
		)
	}
	_ = tw.Flush()
	return out.String()
}

func (a *App) renderTraceStatus(snapshot client.StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s\t%s\n", a.t("device_name"), valueOrDash(snapshot.DeviceName))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("table_device_id"), valueOrDash(snapshot.DeviceID))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_network_state"), a.localizedStatusValue(snapshot.NetworkState))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_local_udp"), valueOrDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_public_udp"), valueOrDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_server_udp"), valueOrDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_register_at"), formatTimesOrDash(snapshot.LastRegisterAt))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_register_error"), valueOrDash(a.localizeErrorText(snapshot.LastRegisterError)))
	fmt.Fprintf(&out, "%s\t%d\n", a.t("overview_rejoin_count"), snapshot.RejoinCount)
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_rejoin_reason"), valueOrDash(snapshot.LastRejoinReason))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_rejoin_attempt_at"), formatTimesOrDash(snapshot.LastRejoinAttemptAt))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_rejoin_at"), formatTimesOrDash(snapshot.LastRejoinAt))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("field_last_rejoin_error"), valueOrDash(a.localizeErrorText(snapshot.LastRejoinError)))
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_consecutive_rejoin_failures"), snapshot.ConsecutiveRejoinFailures)
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_tcp_bind_streams"), len(snapshot.TCPBindStreams))
	fmt.Fprintf(&out, "%s\t%d\n", a.t("field_tcp_publish_proxies"), len(snapshot.TCPProxies))
	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_generated_at"), snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	out.WriteString(a.t("section_peer_candidates") + "\n")
	if len(snapshot.Peers) == 0 {
		writeNoneBlock(&out, a.t("none_value"))
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.t("table_peer"),
			a.t("table_address"),
			a.t("table_current"),
			a.t("table_attempts"),
			a.t("table_first_attempt"),
			a.t("table_last_attempt"),
			a.t("table_last_inbound"),
			a.t("table_last_success"),
			a.t("table_success_ms"),
			a.t("table_success_source"),
		)
		for _, peer := range snapshot.Peers {
			if len(peer.CandidateStats) == 0 {
				fmt.Fprintf(tw, "%s\t-\t-\t0\t-\t-\t-\t-\t-\t-\n", valueOrDash(peer.DeviceName))
				continue
			}
			for _, candidate := range peer.CandidateStats {
				fmt.Fprintf(
					tw,
					"%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					valueOrDash(peer.DeviceName),
					valueOrDash(candidate.Addr),
					a.localizedYesNo(candidate.CurrentRoute),
					candidate.Attempts,
					formatTimesOrDash(candidate.FirstAttemptAt),
					formatTimesOrDash(candidate.LastAttemptAt),
					formatTimesOrDash(candidate.LastInboundAt),
					formatTimesOrDash(candidate.LastSuccessAt),
					formatInt64OrDash(candidate.FirstSuccessLatencyMS),
					valueOrDash(candidate.LastSuccessSource),
				)
			}
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString(a.t("section_tcp_runtime") + "\n")
	if len(snapshot.TCPBindStreams) == 0 && len(snapshot.TCPProxies) == 0 {
		writeNoneBlock(&out, a.t("none_value"))
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.t("table_role"),
			a.t("table_peer"),
			a.t("section_bind"),
			a.t("table_service"),
			a.t("table_session"),
			a.t("table_state"),
			a.t("table_target"),
			a.t("table_started"),
			a.t("table_last_seen"),
			a.t("table_buffered_in"),
			a.t("table_unacked_out"),
		)
		for _, stream := range snapshot.TCPBindStreams {
			fmt.Fprintf(
				tw,
				"%s\t%s\t%s\t%s\t%s\t%s\t-\t%s\t%s\t%d\t%d\n",
				a.t("role_bind"),
				valueOrDash(firstNonEmpty(stream.PeerName, stream.PeerID)),
				valueOrDash(stream.BindName),
				valueOrDash(stream.Service),
				valueOrDash(stream.SessionID),
				a.localizedStatusValue(stream.State),
				formatTimesOrDash(stream.StartedAt),
				formatTimesOrDash(stream.LastSeen),
				stream.BufferedInbound,
				stream.UnackedOutbound,
			)
		}
		for _, proxy := range snapshot.TCPProxies {
			fmt.Fprintf(
				tw,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\n",
				a.t("role_publish"),
				valueOrDash(firstNonEmpty(proxy.PeerName, proxy.PeerID)),
				valueOrDash(proxy.BindName),
				valueOrDash(proxy.Service),
				valueOrDash(proxy.SessionID),
				a.localizedStatusValue(proxy.State),
				valueOrDash(proxy.Target),
				formatTimesOrDash(proxy.StartedAt),
				formatTimesOrDash(proxy.LastSeen),
				proxy.BufferedInbound,
				proxy.UnackedOutbound,
			)
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString(a.t("section_recent_events") + "\n")
	if len(snapshot.RecentEvents) == 0 {
		out.WriteString("  " + a.t("none_value") + "\n")
		return out.String()
	}
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
		a.t("table_at"),
		a.t("table_scope"),
		a.t("table_peer"),
		a.t("table_event"),
		a.t("table_detail"),
	)
	for _, event := range snapshot.RecentEvents {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\n",
			formatTimesOrDash(event.At),
			valueOrDash(event.Scope),
			valueOrDash(firstNonEmpty(event.PeerName, event.PeerID)),
			valueOrDash(event.Event),
			valueOrDash(event.Detail),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func (a *App) renderNetworkDevicesStatus(snapshot proto.NetworkDevicesResponse) string {
	var out strings.Builder

	fmt.Fprintf(&out, "%s\t%s\n", a.t("overview_generated_at"), snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	if len(snapshot.Devices) == 0 {
		fmt.Fprintf(&out, "%s: %s\n", a.t("section_devices"), a.t("none_value"))
		return out.String()
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		a.t("table_device"),
		a.t("table_device_id"),
		a.t("table_state"),
		a.t("overview_public_udp"),
		a.t("table_last_seen"),
		a.t("table_services"),
		a.t("table_candidates"),
	)
	for _, device := range snapshot.Devices {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			valueOrDash(device.DeviceName),
			valueOrDash(device.DeviceID),
			a.localizedStatusValue(device.State),
			valueOrDash(device.ObservedAddr),
			formatTimesOrDash(device.LastSeen),
			joinListOrDash(networkDeviceServiceLabels(device)),
			joinListOrDash(device.Candidates),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func (a *App) localizedYesNo(value bool) string {
	if value {
		return a.t("yes_value")
	}
	return a.t("no_value")
}

func (a *App) localizedStatusValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "connected":
		return a.t("state_connected")
	case "joined":
		return a.t("state_joined")
	case "rejoining":
		return a.t("state_rejoining")
	case "rejoin_pending":
		return a.t("state_rejoin_pending")
	case "online":
		return a.t("state_online")
	case "offline":
		return a.t("state_offline")
	case "opening":
		return a.t("state_opening")
	case "open":
		return a.t("state_open")
	case "closing":
		return a.t("state_closing")
	case "closed":
		return a.t("state_closed")
	default:
		return valueOrDash(value)
	}
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func writeNoneBlock(out *strings.Builder, noneValue string) {
	out.WriteString("  " + noneValue + "\n\n")
}

func joinListOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func formatTimesOrDash(values ...time.Time) string {
	for _, value := range values {
		if !value.IsZero() {
			return value.Format(time.RFC3339)
		}
	}
	return "-"
}

func formatInt64OrDash(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}

func peerServiceLabels(peer client.PeerStatus) []string {
	if len(peer.ServiceDetails) == 0 {
		return peer.Services
	}
	out := make([]string, 0, len(peer.ServiceDetails))
	for _, service := range peer.ServiceDetails {
		out = append(out, displayServiceLabel(service))
	}
	sort.Strings(out)
	return out
}

func networkDeviceServiceLabels(device proto.NetworkDeviceStatus) []string {
	if len(device.ServiceDetails) == 0 {
		return device.Services
	}
	out := make([]string, 0, len(device.ServiceDetails))
	for _, service := range device.ServiceDetails {
		out = append(out, displayServiceLabel(service))
	}
	sort.Strings(out)
	return out
}

func displayServiceLabel(service proto.ServiceInfo) string {
	protocol := strings.TrimSpace(service.Protocol)
	if protocol == "" {
		protocol = config.ServiceProtocolUDP
	}
	return service.Name + "/" + protocol
}
