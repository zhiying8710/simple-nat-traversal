package client

import (
	"fmt"
	"simple-nat-traversal/internal/proto"
	"strings"
	"text/tabwriter"
	"time"
)

func RenderPeersStatus(snapshot StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "device\t%s\n", snapshot.DeviceName)
	fmt.Fprintf(&out, "device_id\t%s\n", emptyDash(snapshot.DeviceID))
	fmt.Fprintf(&out, "local_udp\t%s\n", emptyDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "public_udp\t%s\n", emptyDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "server_udp\t%s\n", emptyDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "generated_at\t%s\n", snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	if len(snapshot.Peers) == 0 {
		out.WriteString("peers: none\n")
		return out.String()
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "PEER\tSTATE\tROUTE\tREASON\tSERVICES\tPUNCH\tPKTS(tx/rx)\tBYTES(tx/rx)\tLAST_SEEN\tERROR")
	for _, peer := range snapshot.Peers {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%d\t%d/%d\t%d/%d\t%s\t%s\n",
			emptyDash(peer.DeviceName),
			emptyDash(peer.State),
			emptyDash(peer.ChosenAddr),
			emptyDash(peer.RouteReason),
			joinOrDash(peer.Services),
			peer.PunchAttempts,
			peer.SentPackets,
			peer.RecvPackets,
			peer.SentBytes,
			peer.RecvBytes,
			timeOrDash(peer.SessionLastSeen, peer.LastSeen),
			emptyDash(peer.LastError),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func RenderRoutesStatus(snapshot StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "device\t%s\n", snapshot.DeviceName)
	fmt.Fprintf(&out, "local_udp\t%s\n", emptyDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "public_udp\t%s\n", emptyDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "server_udp\t%s\n", emptyDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "service_proxies\t%d\n", snapshot.ActiveServiceProxies)
	out.WriteString("\n")

	out.WriteString("publish\n")
	if len(snapshot.Publish) == 0 {
		out.WriteString("  none\n\n")
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tLOCAL")
		for _, publish := range snapshot.Publish {
			fmt.Fprintf(tw, "%s\t%s\n", emptyDash(publish.Name), emptyDash(publish.Local))
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString("bind\n")
	if len(snapshot.Binds) == 0 {
		out.WriteString("  none\n")
		return out.String()
	}

	peersByName := make(map[string]PeerStatus, len(snapshot.Peers))
	for _, peer := range snapshot.Peers {
		peersByName[peer.DeviceName] = peer
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLISTEN\tPEER\tSTATE\tROUTE\tREASON\tSERVICE\tSESSIONS")
	for _, bind := range snapshot.Binds {
		peer, ok := peersByName[bind.Peer]
		state := "offline"
		route := "-"
		reason := "-"
		if ok {
			state = emptyDash(peer.State)
			route = emptyDash(peer.ChosenAddr)
			reason = emptyDash(peer.RouteReason)
		}
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			emptyDash(bind.Name),
			emptyDash(bind.ListenAddr),
			emptyDash(bind.Peer),
			state,
			route,
			reason,
			emptyDash(bind.Service),
			bind.ActiveSessions,
		)
	}
	_ = tw.Flush()
	return out.String()
}

func RenderTraceStatus(snapshot StatusSnapshot) string {
	var out strings.Builder

	fmt.Fprintf(&out, "device\t%s\n", snapshot.DeviceName)
	fmt.Fprintf(&out, "device_id\t%s\n", emptyDash(snapshot.DeviceID))
	fmt.Fprintf(&out, "network_state\t%s\n", emptyDash(snapshot.NetworkState))
	fmt.Fprintf(&out, "local_udp\t%s\n", emptyDash(snapshot.LocalUDPAddr))
	fmt.Fprintf(&out, "public_udp\t%s\n", emptyDash(snapshot.ObservedAddr))
	fmt.Fprintf(&out, "server_udp\t%s\n", emptyDash(snapshot.ServerUDPAddr))
	fmt.Fprintf(&out, "last_register_at\t%s\n", timeOrDash(snapshot.LastRegisterAt))
	fmt.Fprintf(&out, "last_register_error\t%s\n", emptyDash(snapshot.LastRegisterError))
	fmt.Fprintf(&out, "rejoin_count\t%d\n", snapshot.RejoinCount)
	fmt.Fprintf(&out, "last_rejoin_reason\t%s\n", emptyDash(snapshot.LastRejoinReason))
	fmt.Fprintf(&out, "last_rejoin_attempt_at\t%s\n", timeOrDash(snapshot.LastRejoinAttemptAt))
	fmt.Fprintf(&out, "last_rejoin_at\t%s\n", timeOrDash(snapshot.LastRejoinAt))
	fmt.Fprintf(&out, "last_rejoin_error\t%s\n", emptyDash(snapshot.LastRejoinError))
	fmt.Fprintf(&out, "consecutive_rejoin_failures\t%d\n", snapshot.ConsecutiveRejoinFailures)
	fmt.Fprintf(&out, "generated_at\t%s\n", snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	out.WriteString("peer_candidates\n")
	if len(snapshot.Peers) == 0 {
		out.WriteString("  none\n\n")
	} else {
		tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "PEER\tADDR\tCURRENT\tATTEMPTS\tFIRST_ATTEMPT\tLAST_ATTEMPT\tLAST_INBOUND\tLAST_SUCCESS\tSUCCESS_MS\tSUCCESS_SOURCE")
		for _, peer := range snapshot.Peers {
			if len(peer.CandidateStats) == 0 {
				fmt.Fprintf(tw, "%s\t-\t-\t0\t-\t-\t-\t-\t-\t-\n", emptyDash(peer.DeviceName))
				continue
			}
			for _, candidate := range peer.CandidateStats {
				fmt.Fprintf(
					tw,
					"%s\t%s\t%t\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					emptyDash(peer.DeviceName),
					emptyDash(candidate.Addr),
					candidate.CurrentRoute,
					candidate.Attempts,
					timeOrDash(candidate.FirstAttemptAt),
					timeOrDash(candidate.LastAttemptAt),
					timeOrDash(candidate.LastInboundAt),
					timeOrDash(candidate.LastSuccessAt),
					int64OrDash(candidate.FirstSuccessLatencyMS),
					emptyDash(candidate.LastSuccessSource),
				)
			}
		}
		_ = tw.Flush()
		out.WriteString("\n")
	}

	out.WriteString("recent_events\n")
	if len(snapshot.RecentEvents) == 0 {
		out.WriteString("  none\n")
		return out.String()
	}
	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "AT\tSCOPE\tPEER\tEVENT\tDETAIL")
	for _, event := range snapshot.RecentEvents {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\n",
			timeOrDash(event.At),
			emptyDash(event.Scope),
			emptyDash(firstNonEmpty(event.PeerName, event.PeerID)),
			emptyDash(event.Event),
			emptyDash(event.Detail),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func RenderNetworkDevicesStatus(snapshot proto.NetworkDevicesResponse) string {
	var out strings.Builder

	fmt.Fprintf(&out, "generated_at\t%s\n", snapshot.GeneratedAt.Format(time.RFC3339))
	out.WriteString("\n")

	if len(snapshot.Devices) == 0 {
		out.WriteString("devices: none\n")
		return out.String()
	}

	tw := tabwriter.NewWriter(&out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tID\tSTATE\tPUBLIC_UDP\tLAST_SEEN\tSERVICES\tCANDIDATES")
	for _, device := range snapshot.Devices {
		fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			emptyDash(device.DeviceName),
			emptyDash(device.DeviceID),
			emptyDash(device.State),
			emptyDash(device.ObservedAddr),
			timeOrDash(device.LastSeen),
			joinOrDash(device.Services),
			joinOrDash(device.Candidates),
		)
	}
	_ = tw.Flush()
	return out.String()
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

func timeOrDash(values ...time.Time) string {
	for _, value := range values {
		if !value.IsZero() {
			return value.Format(time.RFC3339)
		}
	}
	return "-"
}

func int64OrDash(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", value)
}
