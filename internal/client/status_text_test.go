package client

import (
	"strings"
	"testing"
	"time"

	"simple-nat-traversal/internal/proto"
)

func TestRenderPeersStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 28, 22, 0, 0, 0, time.UTC)
	rendered := RenderPeersStatus(StatusSnapshot{
		GeneratedAt:   now,
		DeviceID:      "dev_local",
		DeviceName:    "mac-a",
		LocalUDPAddr:  "0.0.0.0:50000",
		ObservedAddr:  "1.2.3.4:50000",
		ServerUDPAddr: "8.8.8.8:3479",
		Peers: []PeerStatus{
			{
				DeviceID:        "dev_win",
				DeviceName:      "win-b",
				State:           "connected",
				ChosenAddr:      "9.9.9.9:45678",
				RouteReason:     "received_punch_hello",
				Services:        []string{"echo", "game"},
				PunchAttempts:   4,
				SentPackets:     10,
				RecvPackets:     12,
				SentBytes:       1000,
				RecvBytes:       1200,
				SessionLastSeen: now,
			},
		},
	})

	for _, want := range []string{
		"device\tmac-a",
		"PEER",
		"win-b",
		"connected",
		"9.9.9.9:45678",
		"received_punch_hello",
		"echo,game",
		"10/12",
		"1000/1200",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered peers output missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderRoutesStatus(t *testing.T) {
	t.Parallel()

	rendered := RenderRoutesStatus(StatusSnapshot{
		DeviceName:           "mac-a",
		LocalUDPAddr:         "0.0.0.0:50000",
		ObservedAddr:         "1.2.3.4:50000",
		ServerUDPAddr:        "8.8.8.8:3479",
		ActiveServiceProxies: 2,
		Publish: []PublishStatus{
			{Name: "echo", Local: "127.0.0.1:19132"},
		},
		Binds: []BindStatus{
			{
				Name:           "echo-b",
				ListenAddr:     "127.0.0.1:29132",
				Peer:           "win-b",
				Service:        "echo",
				ActiveSessions: 1,
			},
		},
		Peers: []PeerStatus{
			{
				DeviceName:  "win-b",
				State:       "connected",
				ChosenAddr:  "9.9.9.9:45678",
				RouteReason: "received_encrypted_data",
			},
		},
	})

	for _, want := range []string{
		"publish",
		"echo",
		"127.0.0.1:19132",
		"bind",
		"echo-b",
		"win-b",
		"connected",
		"9.9.9.9:45678",
		"received_encrypted_data",
		"service_proxies\t2",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered routes output missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderTraceStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 28, 22, 5, 0, 0, time.UTC)
	rendered := RenderTraceStatus(StatusSnapshot{
		GeneratedAt:               now,
		DeviceID:                  "dev_local",
		DeviceName:                "mac-a",
		NetworkState:              "rejoining",
		LocalUDPAddr:              "0.0.0.0:50000",
		ObservedAddr:              "1.2.3.4:50000",
		ServerUDPAddr:             "8.8.8.8:3479",
		LastRegisterAt:            now,
		LastRegisterError:         "network session is not ready",
		RejoinCount:               2,
		LastRejoinReason:          "invalid_device_session",
		LastRejoinAttemptAt:       now,
		LastRejoinAt:              now,
		LastRejoinError:           "join network timeout",
		ConsecutiveRejoinFailures: 3,
		Peers: []PeerStatus{
			{
				DeviceName: "win-b",
				CandidateStats: []CandidateStatus{
					{
						Addr:                  "9.9.9.9:45678",
						CurrentRoute:          true,
						Attempts:              3,
						FirstAttemptAt:        now,
						LastAttemptAt:         now,
						LastInboundAt:         now,
						LastSuccessAt:         now,
						FirstSuccessLatencyMS: 42,
						LastSuccessSource:     "received_punch_hello",
					},
				},
			},
		},
		RecentEvents: []TraceEvent{
			{
				At:       now,
				Scope:    "peer",
				PeerName: "win-b",
				Event:    "route_selected",
				Detail:   "reason=received_punch_hello addr=9.9.9.9:45678",
			},
		},
	})

	for _, want := range []string{
		"network_state\trejoining",
		"last_register_error\tnetwork session is not ready",
		"rejoin_count\t2",
		"last_rejoin_reason\tinvalid_device_session",
		"last_rejoin_error\tjoin network timeout",
		"consecutive_rejoin_failures\t3",
		"peer_candidates",
		"win-b",
		"9.9.9.9:45678",
		"true",
		"3",
		"42",
		"received_punch_hello",
		"recent_events",
		"route_selected",
		"reason=received_punch_hello",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered trace output missing %q:\n%s", want, rendered)
		}
	}
}

func TestRenderNetworkDevicesStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 28, 22, 10, 0, 0, time.UTC)
	rendered := RenderNetworkDevicesStatus(proto.NetworkDevicesResponse{
		GeneratedAt: now,
		Devices: []proto.NetworkDeviceStatus{
			{
				DeviceID:     "dev_mac",
				DeviceName:   "mac-a",
				State:        "online",
				ObservedAddr: "1.2.3.4:50000",
				LastSeen:     now,
				Services:     []string{"echo", "game"},
				Candidates:   []string{"1.2.3.4:50000", "192.168.1.10:50000"},
			},
		},
	})

	for _, want := range []string{
		"generated_at",
		"DEVICE",
		"mac-a",
		"dev_mac",
		"online",
		"1.2.3.4:50000",
		"echo,game",
		"192.168.1.10:50000",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered network output missing %q:\n%s", want, rendered)
		}
	}
}
