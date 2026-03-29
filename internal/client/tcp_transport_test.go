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
	doneCh := make(chan struct{})
	go func() {
		c.failTCPBindOpen(stream, openErr)
		close(doneCh)
	}()

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

	stream.closeAcked.Store(true)

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("expected failTCPBindOpen to finish after close ack")
	}
}

func TestCloseTCPBindOutboundWaitsForAckAndSendsFinalSeq(t *testing.T) {
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
		sessionID: "tcp-close-drain",
		conn:      clientConn,
		done:      make(chan struct{}),
	}
	stream.sender = newTCPReliableSender(proto.ServicePayload{
		Protocol:  config.ServiceProtocolTCP,
		BindName:  stream.bindName,
		Service:   stream.service,
		SessionID: stream.sessionID,
	}, func(payload proto.ServicePayload) error {
		return c.sendServicePayload(stream.peerID, payload)
	}, nil)
	stream.sender.pending[1] = &tcpPendingChunk{payload: []byte("tail"), sentAt: time.Now()}
	stream.sender.lastSentSeq = 1

	doneCh := make(chan struct{})
	go func() {
		c.closeTCPBindOutbound(stream, "")
		close(doneCh)
	}()

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
	if payload.Ack != 1 {
		t.Fatalf("expected tcp close final seq 1, got %d", payload.Ack)
	}

	if err := peerUDP.SetReadDeadline(time.Now().Add(2 * tcpResendAfter)); err != nil {
		t.Fatalf("set resend read deadline: %v", err)
	}
	n, _, err = peerUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read retransmitted close datagram: %v", err)
	}
	env, err = proto.UnmarshalEnvelope(buf[:n])
	if err != nil {
		t.Fatalf("unmarshal retransmit envelope: %v", err)
	}
	plaintext, err = secure.DecryptPacket(sessionKey, env.Data.Seq, env.Data.Ciphertext)
	if err != nil {
		t.Fatalf("decrypt retransmit payload: %v", err)
	}
	payload, err = proto.UnmarshalServicePayload(plaintext)
	if err != nil {
		t.Fatalf("unmarshal retransmit service payload: %v", err)
	}
	if payload.Kind != proto.DataKindTCPClose {
		t.Fatalf("expected retransmitted tcp close payload, got %s", payload.Kind)
	}

	select {
	case <-doneCh:
		t.Fatal("expected close path to wait for outbound ack and close ack")
	case <-time.After(150 * time.Millisecond):
	}

	stream.sender.ack(1)

	select {
	case <-doneCh:
		t.Fatal("expected close path to wait for close ack after outbound ack")
	case <-time.After(150 * time.Millisecond):
	}

	stream.closeAcked.Store(true)

	select {
	case <-doneCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected close path to finish after close ack")
	}
}

func TestRunTCPBindInboundWaitsForFinalSeqBeforeClosing(t *testing.T) {
	t.Parallel()

	clientConn, remoteConn := net.Pipe()
	defer remoteConn.Close()

	stream := &tcpBindStream{
		peerID:     "peer-1",
		peerName:   "win-b",
		bindName:   "win-rdp",
		service:    "rdp",
		sessionID:  "tcp-final-seq",
		conn:       clientConn,
		openResult: make(chan error, 1),
		inboundCh:  make(chan tcpFrameEvent, 4),
		done:       make(chan struct{}),
		pending:    map[uint64][]byte{},
	}

	c := &Client{}
	go c.runTCPBindInbound(stream)

	if !stream.enqueueInbound(tcpFrameEvent{close: true, finalSeq: 2}) {
		t.Fatal("enqueue close frame failed")
	}
	select {
	case <-stream.done:
		t.Fatal("expected stream to stay open until final seq arrives")
	case <-time.After(100 * time.Millisecond):
	}

	if !stream.enqueueInbound(tcpFrameEvent{seq: 1, payload: []byte("hello")}) {
		t.Fatal("enqueue seq1 failed")
	}
	readBuf := make([]byte, 16)
	if err := remoteConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, err := remoteConn.Read(readBuf)
	if err != nil {
		t.Fatalf("read seq1 payload: %v", err)
	}
	if got := string(readBuf[:n]); got != "hello" {
		t.Fatalf("unexpected seq1 payload: %q", got)
	}
	select {
	case <-stream.done:
		t.Fatal("expected stream to remain open while final seq is missing")
	case <-time.After(100 * time.Millisecond):
	}

	if !stream.enqueueInbound(tcpFrameEvent{seq: 2, payload: []byte("world")}) {
		t.Fatal("enqueue seq2 failed")
	}
	if err := remoteConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, err = remoteConn.Read(readBuf)
	if err != nil {
		t.Fatalf("read seq2 payload: %v", err)
	}
	if got := string(readBuf[:n]); got != "world" {
		t.Fatalf("unexpected seq2 payload: %q", got)
	}

	select {
	case <-stream.done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected stream to close after final seq arrives")
	}
}

func TestOpenTCPStreamStopsRetryWhenStreamClosed(t *testing.T) {
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
		peerID:     "peer-1",
		peerName:   "win-b",
		bindName:   "win-rdp",
		service:    "rdp",
		sessionID:  "tcp-open-cancel",
		conn:       clientConn,
		openResult: make(chan error, 1),
		inboundCh:  make(chan tcpFrameEvent, tcpSendWindow*2),
		done:       make(chan struct{}),
		pending:    map[uint64][]byte{},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.openTCPStream(stream)
	}()

	buf := make([]byte, maxDatagramSize)
	if err := peerUDP.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := peerUDP.ReadFromUDP(buf); err != nil {
		t.Fatalf("read first open attempt: %v", err)
	}

	stream.close()

	select {
	case err := <-errCh:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("expected open to stop with net.ErrClosed, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected open retry loop to stop after stream close")
	}

	if err := peerUDP.SetReadDeadline(time.Now().Add(450 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := peerUDP.ReadFromUDP(buf); err == nil {
		t.Fatal("expected no retry after stream close")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("expected read timeout after stream close, got %v", err)
	}
}

func TestOpenTCPResultAfterCloseReturnsSuccessWhenOpenCompleted(t *testing.T) {
	t.Parallel()

	stream := &tcpBindStream{
		openResult: make(chan error, 1),
	}
	stream.resolveOpen(nil)

	if err := openTCPResultAfterClose(stream); err != nil {
		t.Fatalf("expected successful open result, got %v", err)
	}

	stream.mu.Lock()
	openReady := stream.openReady
	stream.mu.Unlock()
	if !openReady {
		t.Fatal("expected stream to be marked open after successful open result")
	}
}

func TestHandleTCPCloseAcknowledgesUnknownSession(t *testing.T) {
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

	c.handleTCPClose("peer-1", proto.ServicePayload{
		Protocol:  config.ServiceProtocolTCP,
		BindName:  "win-rdp",
		Service:   "rdp",
		SessionID: "already-closed",
		Ack:       7,
	})

	buf := make([]byte, maxDatagramSize)
	n, _, err := peerUDP.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read udp datagram: %v", err)
	}
	env, err := proto.UnmarshalEnvelope(buf[:n])
	if err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	plaintext, err := secure.DecryptPacket(sessionKey, env.Data.Seq, env.Data.Ciphertext)
	if err != nil {
		t.Fatalf("decrypt payload: %v", err)
	}
	payload, err := proto.UnmarshalServicePayload(plaintext)
	if err != nil {
		t.Fatalf("unmarshal service payload: %v", err)
	}
	if payload.Kind != proto.DataKindTCPCloseAck {
		t.Fatalf("expected tcp close ack payload, got %s", payload.Kind)
	}
	if payload.BindName != "win-rdp" || payload.Service != "rdp" || payload.SessionID != "already-closed" {
		t.Fatalf("unexpected close ack identity: %+v", payload)
	}
	if payload.Ack != 7 {
		t.Fatalf("unexpected close ack final seq: %d", payload.Ack)
	}
}

func TestRunTCPBindRetriesAcceptErrors(t *testing.T) {
	t.Parallel()

	listener := &scriptedListener{
		results: []scriptedAcceptResult{
			{err: temporaryAcceptError{}},
			{err: net.ErrClosed},
		},
	}

	doneCh := make(chan struct{})
	go func() {
		(&Client{}).runTCPBind(&bindProxy{
			name: "win-rdp",
			tcp:  listener,
		})
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected accept loop to retry and stop after listener close")
	}

	if listener.calls != len(listener.results) {
		t.Fatalf("expected %d accept calls, got %d", len(listener.results), listener.calls)
	}
}

type scriptedAcceptResult struct {
	conn net.Conn
	err  error
}

type scriptedListener struct {
	results []scriptedAcceptResult
	calls   int
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	if l.calls >= len(l.results) {
		return nil, net.ErrClosed
	}
	result := l.results[l.calls]
	l.calls++
	return result.conn, result.err
}

func (l *scriptedListener) Close() error {
	return nil
}

func (l *scriptedListener) Addr() net.Addr {
	return testAddr("tcp")
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string {
	return "temporary accept failure"
}

func (temporaryAcceptError) Timeout() bool {
	return false
}

func (temporaryAcceptError) Temporary() bool {
	return true
}

type testAddr string

func (a testAddr) Network() string {
	return string(a)
}

func (a testAddr) String() string {
	return string(a)
}
