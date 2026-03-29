package client

import (
	"context"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
)

func TestSetRuntimeLogLevel(t *testing.T) {
	previous := logx.CurrentLevel()
	defer func() {
		_, _ = logx.SetLevel(previous)
	}()
	_, _ = logx.SetLevel(config.LogLevelInfo)

	c := &Client{
		cfg: config.ClientConfig{
			AdminListen: "127.0.0.1:0",
			LogLevel:    config.LogLevelInfo,
		},
	}
	if err := c.startAdminServer(); err != nil {
		t.Fatalf("startAdminServer: %v", err)
	}
	defer func() {
		if c.adminServer != nil {
			_ = c.adminServer.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := SetRuntimeLogLevel(ctx, config.ClientConfig{AdminListen: c.adminAddr}, config.LogLevelDebug)
	if err != nil {
		t.Fatalf("SetRuntimeLogLevel: %v", err)
	}
	if resp.LogLevel != config.LogLevelDebug {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := c.snapshotStatus().LogLevel; got != config.LogLevelDebug {
		t.Fatalf("unexpected runtime log level: %s", got)
	}
}

func TestSnapshotStatusIncludesTCPDiagnostics(t *testing.T) {
	now := time.Date(2026, 3, 29, 1, 2, 3, 0, time.UTC)

	streamSender := newTCPReliableSender(proto.ServicePayload{}, func(proto.ServicePayload) error { return nil }, nil)
	streamSender.pending[1] = &tcpPendingChunk{}
	stream := &tcpBindStream{
		peerID:    "dev-win",
		peerName:  "win-b",
		bindName:  "win-rdp",
		service:   "rdp",
		sessionID: "tcp-1",
		sender:    streamSender,
		startedAt: now,
		pending: map[uint64][]byte{
			2: []byte("hello"),
		},
	}
	stream.touch()
	stream.mu.Lock()
	stream.openReady = true
	stream.mu.Unlock()

	proxySender := newTCPReliableSender(proto.ServicePayload{}, func(proto.ServicePayload) error { return nil }, nil)
	proxySender.pending[3] = &tcpPendingChunk{}
	proxy := &serviceProxy{
		key:       "dev-win|win-rdp|rdp|tcp-1",
		peerID:    "dev-win",
		peerName:  "win-b",
		bindName:  "win-rdp",
		service:   "rdp",
		sessionID: "tcp-1",
		protocol:  config.ServiceProtocolTCP,
		target:    "127.0.0.1:3389",
		sender:    proxySender,
		startedAt: now,
		pending: map[uint64][]byte{
			4: []byte("world"),
			5: []byte("again"),
		},
	}
	proxy.touch()

	c := &Client{
		cfg: config.ClientConfig{
			DeviceName: "mac-a",
			Publish: map[string]config.PublishConfig{
				"rdp": {Protocol: config.ServiceProtocolTCP, Local: "127.0.0.1:3389"},
			},
		},
		binds: map[string]*bindProxy{
			"win-rdp": {
				name: "win-rdp",
				cfg: config.BindConfig{
					Protocol: config.ServiceProtocolTCP,
					Peer:     "win-b",
					Service:  "rdp",
					Local:    "127.0.0.1:13389",
				},
				tcpStreams: map[string]*tcpBindStream{
					"tcp-1": stream,
				},
				sessions: map[string]*bindSession{},
			},
		},
		serviceProxies: map[string]*serviceProxy{
			proxy.key: proxy,
		},
		peers: map[string]*peerState{
			"dev-win": {
				info: proto.PeerInfo{
					DeviceID:   "dev-win",
					DeviceName: "win-b",
				},
			},
		},
	}

	snapshot := c.snapshotStatus()
	if got := len(snapshot.TCPBindStreams); got != 1 {
		t.Fatalf("unexpected tcp bind stream count: %d", got)
	}
	if got := snapshot.TCPBindStreams[0]; got.BindName != "win-rdp" || got.PeerName != "win-b" || got.State != "open" || got.BufferedInbound != 1 || got.UnackedOutbound != 1 {
		t.Fatalf("unexpected tcp bind stream snapshot: %+v", got)
	}
	if got := len(snapshot.TCPProxies); got != 1 {
		t.Fatalf("unexpected tcp proxy count: %d", got)
	}
	if got := snapshot.TCPProxies[0]; got.Target != "127.0.0.1:3389" || got.PeerName != "win-b" || got.BufferedInbound != 2 || got.UnackedOutbound != 1 {
		t.Fatalf("unexpected tcp proxy snapshot: %+v", got)
	}
}
