package client

import (
	"errors"
	"net"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

func TestFailTCPBindOpenNotifiesPeer(t *testing.T) {
	t.Parallel()

	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen local udp: %v", err)
	}
	defer localUDP.Close()

	peerUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen peer udp: %v", err)
	}
	defer peerUDP.Close()
	if err := peerUDP.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	sessionKey := make([]byte, 32)
	for i := range sessionKey {
		sessionKey[i] = byte(i + 1)
	}

	clientConn, remoteConn := net.Pipe()
	defer remoteConn.Close()

	c := &Client{
		cfg:      config.ClientConfig{DeviceName: "mac-a"},
		deviceID: "dev-local",
		udpConn:  localUDP,
		peers: map[string]*peerState{
			"peer-1": {
				info: proto.PeerInfo{
					DeviceID:   "peer-1",
					DeviceName: "win-b",
				},
				session: &sessionState{
					key:  sessionKey,
					seen: map[uint64]struct{}{},
				},
				chosenAddr: cloneUDPAddr(peerUDP.LocalAddr().(*net.UDPAddr)),
			},
		},
	}

	stream := &tcpBindStream{
		peerID:    "peer-1",
		peerName:  "win-b",
		bindName:  "win-rdp",
		service:   "rdp",
		sessionID: "tcp-open-failed",
		conn:      clientConn,
		done:      make(chan struct{}),
	}

	openErr := errors.New("tcp open timed out")
	c.failTCPBindOpen(stream, openErr)

	buf := make([]byte, maxDatagramSize)
	n, _, err := peerUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read udp datagram: %v", err)
	}
	env, err := proto.UnmarshalEnvelope(buf[:n])
	if err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != proto.TypeData || env.Data == nil {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	plaintext, err := secure.DecryptPacket(sessionKey, env.Data.Seq, env.Data.Ciphertext)
	if err != nil {
		t.Fatalf("decrypt payload: %v", err)
	}
	payload, err := proto.UnmarshalServicePayload(plaintext)
	if err != nil {
		t.Fatalf("unmarshal service payload: %v", err)
	}
	if payload.Kind != proto.DataKindTCPClose {
		t.Fatalf("expected tcp close payload, got %s", payload.Kind)
	}
	if payload.BindName != "win-rdp" || payload.Service != "rdp" || payload.SessionID != "tcp-open-failed" {
		t.Fatalf("unexpected close payload identity: %+v", payload)
	}
	if payload.Error != openErr.Error() {
		t.Fatalf("unexpected close payload error: %q", payload.Error)
	}
}
