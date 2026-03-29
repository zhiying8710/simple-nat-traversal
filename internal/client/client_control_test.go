package client

import (
	"net"
	"testing"

	"simple-nat-traversal/internal/proto"
)

func TestHandleDatagramIgnoresServerControlMessagesFromUnexpectedAddr(t *testing.T) {
	t.Parallel()

	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3479}
	otherAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3480}

	c := &Client{
		deviceID:      "dev-local",
		serverUDPAddr: cloneUDPAddr(serverAddr),
		rejoinCh:      make(chan string, 1),
		peers: map[string]*peerState{
			"peer-1": {
				info: proto.PeerInfo{
					DeviceID:   "peer-1",
					DeviceName: "win-b",
				},
			},
		},
	}

	c.handleDatagram(otherAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type: proto.TypeRegisterAck,
		RegisterAck: &proto.RegisterAckMessage{
			ObservedAddr: "198.51.100.10:40000",
		},
	}))
	if c.observedAddr != "" {
		t.Fatalf("expected unexpected register_ack to be ignored, got observed_addr=%q", c.observedAddr)
	}

	c.handleDatagram(otherAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type:     proto.TypePeerSync,
		PeerSync: &proto.PeerSyncMessage{},
	}))
	if got := len(c.peers); got != 1 {
		t.Fatalf("expected unexpected peer_sync to be ignored, got peers=%d", got)
	}

	c.handleDatagram(otherAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type: proto.TypeError,
		Error: &proto.ErrorMessage{
			Message: "invalid device session",
		},
	}))
	select {
	case reason := <-c.rejoinCh:
		t.Fatalf("expected unexpected error packet to be ignored, got rejoin=%q", reason)
	default:
	}
}

func TestHandleDatagramAcceptsServerControlMessagesFromExpectedAddr(t *testing.T) {
	t.Parallel()

	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 3479}

	c := &Client{
		deviceID:      "dev-local",
		serverUDPAddr: cloneUDPAddr(serverAddr),
		rejoinCh:      make(chan string, 1),
		peers:         map[string]*peerState{},
	}

	c.handleDatagram(serverAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type: proto.TypeRegisterAck,
		RegisterAck: &proto.RegisterAckMessage{
			ObservedAddr: "198.51.100.10:40000",
		},
	}))
	if c.observedAddr != "198.51.100.10:40000" {
		t.Fatalf("expected register_ack to update observed_addr, got %q", c.observedAddr)
	}

	c.handleDatagram(serverAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type: proto.TypePeerSync,
		PeerSync: &proto.PeerSyncMessage{
			Peers: []proto.PeerInfo{
				{
					DeviceID:       "peer-1",
					DeviceName:     "win-b",
					Candidates:     []string{"198.51.100.20:41000"},
					IdentityPublic: make([]byte, 32),
				},
			},
		},
	}))
	if got := len(c.peers); got != 1 {
		t.Fatalf("expected peer_sync from server to be applied, got peers=%d", got)
	}

	c.handleDatagram(serverAddr, mustMarshalEnvelope(t, proto.Envelope{
		Type: proto.TypeError,
		Error: &proto.ErrorMessage{
			Message: "invalid device session",
		},
	}))
	select {
	case reason := <-c.rejoinCh:
		if reason != "invalid_device_session" {
			t.Fatalf("unexpected rejoin reason: %q", reason)
		}
	default:
		t.Fatal("expected server error packet to trigger rejoin")
	}
}

func mustMarshalEnvelope(t *testing.T, env proto.Envelope) []byte {
	t.Helper()
	raw, err := proto.MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}
