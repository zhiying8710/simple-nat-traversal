package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

type tcpBindStream struct {
	peerID    string
	peerName  string
	bindName  string
	service   string
	protocol  string
	sessionID string
	conn      net.Conn

	sender     *tcpReliableSender
	openResult chan error
	inboundCh  chan tcpFrameEvent
	done       chan struct{}
	closeAcked atomic.Bool
	lastSeen   atomic.Int64
	startedAt  time.Time

	mu                  sync.Mutex
	nextSeq             uint64
	pending             map[uint64][]byte
	openReady           bool
	remoteClosePending  bool
	remoteCloseFinalSeq uint64
	remoteCloseErrText  string
	closeOnce           sync.Once
	onClose             func()
}

func newTCPBindStream(peerID, peerName string, bind *bindProxy, conn net.Conn, onClose func()) *tcpBindStream {
	sessionID, err := secure.RandomID("tcp")
	if err != nil {
		sessionID = fmt.Sprintf("tcp-%d", time.Now().UnixNano())
	}
	stream := &tcpBindStream{
		peerID:     peerID,
		peerName:   peerName,
		bindName:   bind.name,
		service:    bind.cfg.Service,
		protocol:   bind.cfg.Protocol,
		sessionID:  sessionID,
		conn:       conn,
		openResult: make(chan error, 1),
		inboundCh:  make(chan tcpFrameEvent, tcpSendWindow*2),
		done:       make(chan struct{}),
		startedAt:  time.Now(),
		pending:    map[uint64][]byte{},
		onClose:    onClose,
	}
	stream.touch()
	return stream
}

func (s *tcpBindStream) touch() {
	s.lastSeen.Store(time.Now().UnixNano())
}

func (s *tcpBindStream) lastSeenTime() time.Time {
	ns := s.lastSeen.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (s *tcpBindStream) resolveOpen(err error) {
	select {
	case s.openResult <- err:
	default:
	}
}

func (s *tcpBindStream) close() {
	s.closeOnce.Do(func() {
		if s.sender != nil {
			s.sender.close()
		}
		if s.done != nil {
			close(s.done)
		}
		_ = s.conn.Close()
		if s.onClose != nil {
			s.onClose()
		}
	})
}

func (s *tcpBindStream) enqueueInbound(frame tcpFrameEvent) bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.inboundCh <- frame:
		return true
	case <-s.done:
		return false
	default:
		return false
	}
}

func (s *tcpBindStream) snapshotStatus() (state string, startedAt, lastSeen time.Time, bufferedInbound, unackedOutbound int) {
	if s == nil {
		return "closed", time.Time{}, time.Time{}, 0, 0
	}
	s.mu.Lock()
	state = "opening"
	if s.openReady {
		state = "open"
	}
	if s.remoteClosePending {
		state = "closing"
	}
	startedAt = s.startedAt
	bufferedInbound = len(s.pending)
	s.mu.Unlock()
	lastSeen = s.lastSeenTime()
	if s.sender != nil {
		unackedOutbound = s.sender.pendingCount()
	}
	return state, startedAt, lastSeen, bufferedInbound, unackedOutbound
}

func (c *Client) runTCPBind(bind *bindProxy) {
	retryDelay := tcpAcceptRetryMin
	for {
		conn, err := bind.tcp.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logx.Warnf("bind %s accept failed; retrying in %s: %v", bind.name, retryDelay, err)
			time.Sleep(retryDelay)
			if retryDelay < tcpAcceptRetryMax {
				retryDelay *= 2
				if retryDelay > tcpAcceptRetryMax {
					retryDelay = tcpAcceptRetryMax
				}
			}
			continue
		}
		retryDelay = tcpAcceptRetryMin
		go c.handleTCPBindConn(bind, conn)
	}
}

func (c *Client) handleTCPBindConn(bind *bindProxy, conn net.Conn) {
	peerID, peerName, err := c.peerIDForBind(bind.cfg.Peer, bind.cfg.Service, bind.cfg.Protocol)
	if err != nil {
		bind.logDrop(bind.name, err)
		_ = conn.Close()
		return
	}

	var stream *tcpBindStream
	stream = newTCPBindStream(peerID, peerName, bind, conn, func() {
		bind.mu.Lock()
		delete(bind.tcpStreams, stream.sessionID)
		bind.mu.Unlock()
	})
	stream.sender = newTCPReliableSender(proto.ServicePayload{
		Protocol:  config.ServiceProtocolTCP,
		BindName:  stream.bindName,
		Service:   stream.service,
		SessionID: stream.sessionID,
	}, func(payload proto.ServicePayload) error {
		return c.sendServicePayload(stream.peerID, payload)
	}, stream.touch)

	bind.mu.Lock()
	bind.tcpStreams[stream.sessionID] = stream
	bind.mu.Unlock()

	go stream.sender.run()
	go c.runTCPBindInbound(stream)
	c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_open_started", fmt.Sprintf("bind=%s service=%s session=%s", stream.bindName, stream.service, stream.sessionID))

	if err := c.openTCPStream(stream); err != nil {
		c.failTCPBindOpen(stream, err)
		return
	}
	c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_open_ok", fmt.Sprintf("bind=%s service=%s session=%s", stream.bindName, stream.service, stream.sessionID))

	go c.runTCPBindOutbound(stream)
}

func (c *Client) failTCPBindOpen(stream *tcpBindStream, err error) {
	if stream == nil || err == nil {
		return
	}
	logx.Warnf("bind %s open tcp stream failed: %v", stream.bindName, err)
	c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_open_failed", fmt.Sprintf("bind=%s service=%s session=%s err=%v", stream.bindName, stream.service, stream.sessionID, err))
	c.closeTCPBindOutbound(stream, err.Error())
}

func finishTCPOpen(stream *tcpBindStream, err error) error {
	if err != nil {
		return err
	}
	stream.mu.Lock()
	stream.openReady = true
	stream.mu.Unlock()
	return nil
}

func openTCPResultAfterClose(stream *tcpBindStream) error {
	select {
	case err := <-stream.openResult:
		return finishTCPOpen(stream, err)
	default:
		return net.ErrClosed
	}
}

func (c *Client) openTCPStream(stream *tcpBindStream) error {
	payload := proto.ServicePayload{
		Kind:      proto.DataKindTCPOpen,
		Protocol:  config.ServiceProtocolTCP,
		BindName:  stream.bindName,
		Service:   stream.service,
		SessionID: stream.sessionID,
	}

	deadline := time.NewTimer(tcpOpenTimeout)
	defer deadline.Stop()
	retry := time.NewTicker(300 * time.Millisecond)
	defer retry.Stop()

	if err := c.sendServicePayload(stream.peerID, payload); err != nil {
		return err
	}

	for {
		select {
		case err := <-stream.openResult:
			return finishTCPOpen(stream, err)
		case <-retry.C:
			if err := c.sendServicePayload(stream.peerID, payload); err != nil {
				return err
			}
		case <-stream.done:
			return openTCPResultAfterClose(stream)
		case <-deadline.C:
			return errors.New("tcp open timed out")
		}
	}
}

func (c *Client) runTCPBindOutbound(stream *tcpBindStream) {
	err := tcpReadChunks(stream.conn, func(chunk []byte) error {
		stream.touch()
		return stream.sender.sendChunk(chunk)
	})
	if err != nil && !errors.Is(err, net.ErrClosed) {
		logx.Warnf("bind %s read tcp stream failed: %v", stream.bindName, err)
	}
	if err == nil || errors.Is(err, io.EOF) {
		c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_local_close", fmt.Sprintf("bind=%s service=%s session=%s", stream.bindName, stream.service, stream.sessionID))
	} else if !errors.Is(err, net.ErrClosed) {
		c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_local_error", fmt.Sprintf("bind=%s service=%s session=%s err=%v", stream.bindName, stream.service, stream.sessionID, err))
	}
	c.closeTCPBindOutbound(stream, "")
}

func (c *Client) runTCPBindInbound(stream *tcpBindStream) {
	for {
		var frame tcpFrameEvent
		select {
		case <-stream.done:
			return
		case frame = <-stream.inboundCh:
		}
		stream.touch()
		if frame.close {
			stream.resolveOpen(tcpCloseError(frame.errText))
			stream.mu.Lock()
			readyToClose, errText := noteTCPRemoteClose(&stream.nextSeq, &stream.remoteClosePending, &stream.remoteCloseFinalSeq, &stream.remoteCloseErrText, frame.finalSeq, frame.errText)
			stream.mu.Unlock()
			if !readyToClose {
				continue
			}
			if strings.TrimSpace(errText) != "" {
				logx.Infof("bind %s remote tcp close: %s", stream.bindName, errText)
			}
			c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_remote_close", fmt.Sprintf("bind=%s service=%s session=%s err=%s", stream.bindName, stream.service, stream.sessionID, firstNonEmpty(errText, "-")))
			stream.close()
			return
		}

		stream.mu.Lock()
		ready, ack := processTCPInbound(&stream.nextSeq, stream.pending, frame.seq, frame.payload)
		closeReady := tcpRemoteCloseReady(stream.nextSeq, stream.remoteClosePending, stream.remoteCloseFinalSeq)
		closeErrText := stream.remoteCloseErrText
		stream.mu.Unlock()

		for _, chunk := range ready {
			if err := writeAll(stream.conn, chunk); err != nil {
				logx.Warnf("bind %s write to local tcp client failed: %v", stream.bindName, err)
				c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_local_write_failed", fmt.Sprintf("bind=%s service=%s session=%s err=%v", stream.bindName, stream.service, stream.sessionID, err))
				_ = c.sendServicePayload(stream.peerID, proto.ServicePayload{
					Kind:      proto.DataKindTCPClose,
					Protocol:  config.ServiceProtocolTCP,
					BindName:  stream.bindName,
					Service:   stream.service,
					SessionID: stream.sessionID,
					Error:     err.Error(),
				})
				stream.close()
				return
			}
		}
		if ack > 0 {
			_ = c.sendServicePayload(stream.peerID, proto.ServicePayload{
				Kind:      proto.DataKindTCPAck,
				Protocol:  config.ServiceProtocolTCP,
				BindName:  stream.bindName,
				Service:   stream.service,
				SessionID: stream.sessionID,
				Ack:       ack,
			})
		}
		if closeReady {
			if strings.TrimSpace(closeErrText) != "" {
				logx.Infof("bind %s remote tcp close: %s", stream.bindName, closeErrText)
			}
			c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_remote_close", fmt.Sprintf("bind=%s service=%s session=%s err=%s", stream.bindName, stream.service, stream.sessionID, firstNonEmpty(closeErrText, "-")))
			stream.close()
			return
		}
	}
}

func (c *Client) getOrCreateTCPServiceProxy(peerID, bindName, service, sessionID, target string) (*serviceProxy, error) {
	key := strings.Join([]string{peerID, bindName, service, sessionID}, "|")

	c.mu.RLock()
	existing := c.serviceProxies[key]
	c.mu.RUnlock()
	if existing != nil {
		existing.touch()
		return existing, nil
	}

	conn, err := net.Dial("tcp", target)
	if err != nil {
		return nil, fmt.Errorf("dial publish target: %w", err)
	}

	proxy := &serviceProxy{
		key:       key,
		peerID:    peerID,
		peerName:  c.peerDisplayNameByID(peerID),
		bindName:  bindName,
		service:   service,
		sessionID: sessionID,
		protocol:  config.ServiceProtocolTCP,
		target:    target,
		tcpConn:   conn,
		inboundCh: make(chan tcpFrameEvent, tcpSendWindow*2),
		done:      make(chan struct{}),
		startedAt: time.Now(),
		pending:   map[uint64][]byte{},
		onClose: func() {
			c.mu.Lock()
			delete(c.serviceProxies, key)
			c.mu.Unlock()
		},
	}
	proxy.touch()
	proxy.sender = newTCPReliableSender(proto.ServicePayload{
		Protocol:  config.ServiceProtocolTCP,
		BindName:  bindName,
		Service:   service,
		SessionID: sessionID,
	}, func(payload proto.ServicePayload) error {
		return c.sendServicePayload(peerID, payload)
	}, proxy.touch)

	c.mu.Lock()
	if existing = c.serviceProxies[key]; existing != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	c.serviceProxies[key] = proxy
	c.mu.Unlock()

	go proxy.sender.run()
	go c.runTCPServiceProxyInbound(proxy)
	go c.runTCPServiceProxyOutbound(proxy)
	c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_opened", fmt.Sprintf("bind=%s service=%s session=%s target=%s", proxy.bindName, proxy.service, proxy.sessionID, proxy.target))
	return proxy, nil
}

func (c *Client) runTCPServiceProxyOutbound(proxy *serviceProxy) {
	err := tcpReadChunks(proxy.tcpConn, func(chunk []byte) error {
		proxy.touch()
		return proxy.sender.sendChunk(chunk)
	})
	if err != nil && !errors.Is(err, net.ErrClosed) {
		logx.Warnf("tcp publish proxy %s/%s read failed: %v", proxy.peerID, proxy.service, err)
	}
	if err == nil || errors.Is(err, io.EOF) {
		c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_local_close", fmt.Sprintf("bind=%s service=%s session=%s target=%s", proxy.bindName, proxy.service, proxy.sessionID, proxy.target))
	} else if !errors.Is(err, net.ErrClosed) {
		c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_local_error", fmt.Sprintf("bind=%s service=%s session=%s target=%s err=%v", proxy.bindName, proxy.service, proxy.sessionID, proxy.target, err))
	}
	c.closeTCPServiceProxyOutbound(proxy, "")
}

func (c *Client) runTCPServiceProxyInbound(proxy *serviceProxy) {
	for {
		var frame tcpFrameEvent
		select {
		case <-proxy.done:
			return
		case frame = <-proxy.inboundCh:
		}
		proxy.touch()
		if frame.close {
			proxy.mu.Lock()
			readyToClose, errText := noteTCPRemoteClose(&proxy.nextSeq, &proxy.remoteClosePending, &proxy.remoteCloseFinalSeq, &proxy.remoteCloseErrText, frame.finalSeq, frame.errText)
			proxy.mu.Unlock()
			if !readyToClose {
				continue
			}
			c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_remote_close", fmt.Sprintf("bind=%s service=%s session=%s target=%s err=%s", proxy.bindName, proxy.service, proxy.sessionID, proxy.target, firstNonEmpty(errText, "-")))
			proxy.close()
			return
		}

		proxy.mu.Lock()
		ready, ack := processTCPInbound(&proxy.nextSeq, proxy.pending, frame.seq, frame.payload)
		closeReady := tcpRemoteCloseReady(proxy.nextSeq, proxy.remoteClosePending, proxy.remoteCloseFinalSeq)
		closeErrText := proxy.remoteCloseErrText
		proxy.mu.Unlock()

		for _, chunk := range ready {
			if err := writeAll(proxy.tcpConn, chunk); err != nil {
				logx.Warnf("tcp publish proxy %s/%s write failed: %v", proxy.peerID, proxy.service, err)
				c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_write_failed", fmt.Sprintf("bind=%s service=%s session=%s target=%s err=%v", proxy.bindName, proxy.service, proxy.sessionID, proxy.target, err))
				_ = c.sendServicePayload(proxy.peerID, proto.ServicePayload{
					Kind:      proto.DataKindTCPClose,
					Protocol:  config.ServiceProtocolTCP,
					BindName:  proxy.bindName,
					Service:   proxy.service,
					SessionID: proxy.sessionID,
					Error:     err.Error(),
				})
				proxy.close()
				return
			}
		}
		if ack > 0 {
			_ = c.sendServicePayload(proxy.peerID, proto.ServicePayload{
				Kind:      proto.DataKindTCPAck,
				Protocol:  config.ServiceProtocolTCP,
				BindName:  proxy.bindName,
				Service:   proxy.service,
				SessionID: proxy.sessionID,
				Ack:       ack,
			})
		}
		if closeReady {
			c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_remote_close", fmt.Sprintf("bind=%s service=%s session=%s target=%s err=%s", proxy.bindName, proxy.service, proxy.sessionID, proxy.target, firstNonEmpty(closeErrText, "-")))
			proxy.close()
			return
		}
	}
}

func (p *serviceProxy) snapshotStatus() (state string, startedAt, lastSeen time.Time, bufferedInbound, unackedOutbound int) {
	if p == nil {
		return "closed", time.Time{}, time.Time{}, 0, 0
	}
	p.mu.Lock()
	state = "open"
	if p.remoteClosePending {
		state = "closing"
	}
	startedAt = p.startedAt
	bufferedInbound = len(p.pending)
	p.mu.Unlock()
	lastSeen = p.lastSeenTime()
	if p.sender != nil {
		unackedOutbound = p.sender.pendingCount()
	}
	return state, startedAt, lastSeen, bufferedInbound, unackedOutbound
}

func (p *serviceProxy) close() {
	p.closeOnce.Do(func() {
		if p.sender != nil {
			p.sender.close()
		}
		if p.done != nil {
			close(p.done)
		}
		if p.udpConn != nil {
			_ = p.udpConn.Close()
		}
		if p.tcpConn != nil {
			_ = p.tcpConn.Close()
		}
		if p.onClose != nil {
			p.onClose()
		}
	})
}

func (p *serviceProxy) enqueueInbound(frame tcpFrameEvent) bool {
	if p == nil || p.inboundCh == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
	}
	select {
	case p.inboundCh <- frame:
		return true
	case <-p.done:
		return false
	default:
		return false
	}
}

func (c *Client) handleTCPOpen(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolTCP {
		return
	}
	publish, ok := c.cfg.Publish[payload.Service]
	if !ok || publish.Protocol != config.ServiceProtocolTCP {
		c.recordEvent("tcp", peerID, c.peerDisplayNameByID(peerID), "tcp_open_rejected", fmt.Sprintf("bind=%s service=%s session=%s err=service_not_published", payload.BindName, payload.Service, payload.SessionID))
		_ = c.sendServicePayload(peerID, proto.ServicePayload{
			Kind:      proto.DataKindTCPOk,
			Protocol:  config.ServiceProtocolTCP,
			BindName:  payload.BindName,
			Service:   payload.Service,
			SessionID: payload.SessionID,
			Error:     fmt.Sprintf("service %s is not published over tcp", payload.Service),
		})
		return
	}

	if _, err := c.getOrCreateTCPServiceProxy(peerID, payload.BindName, payload.Service, payload.SessionID, publish.Local); err != nil {
		c.recordEvent("tcp", peerID, c.peerDisplayNameByID(peerID), "tcp_open_rejected", fmt.Sprintf("bind=%s service=%s session=%s err=%v", payload.BindName, payload.Service, payload.SessionID, err))
		_ = c.sendServicePayload(peerID, proto.ServicePayload{
			Kind:      proto.DataKindTCPOk,
			Protocol:  config.ServiceProtocolTCP,
			BindName:  payload.BindName,
			Service:   payload.Service,
			SessionID: payload.SessionID,
			Error:     err.Error(),
		})
		return
	}
	c.recordEvent("tcp", peerID, c.peerDisplayNameByID(peerID), "tcp_open_accepted", fmt.Sprintf("bind=%s service=%s session=%s target=%s", payload.BindName, payload.Service, payload.SessionID, publish.Local))

	_ = c.sendServicePayload(peerID, proto.ServicePayload{
		Kind:      proto.DataKindTCPOk,
		Protocol:  config.ServiceProtocolTCP,
		BindName:  payload.BindName,
		Service:   payload.Service,
		SessionID: payload.SessionID,
	})
}

func (c *Client) handleTCPOpenResult(peerID string, payload proto.ServicePayload) {
	c.mu.RLock()
	bind := c.binds[payload.BindName]
	c.mu.RUnlock()
	if bind == nil {
		return
	}

	bind.mu.Lock()
	stream := bind.tcpStreams[payload.SessionID]
	bind.mu.Unlock()
	if stream == nil || stream.peerID != peerID {
		return
	}
	if payload.Error != "" {
		stream.resolveOpen(errors.New(payload.Error))
		c.recordEvent("tcp", peerID, stream.peerName, "tcp_open_failed", fmt.Sprintf("bind=%s service=%s session=%s err=%s", stream.bindName, stream.service, stream.sessionID, payload.Error))
		return
	}
	stream.resolveOpen(nil)
}

func (c *Client) handleTCPData(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolTCP {
		return
	}

	c.mu.RLock()
	bind := c.binds[payload.BindName]
	proxy := c.serviceProxies[strings.Join([]string{peerID, payload.BindName, payload.Service, payload.SessionID}, "|")]
	c.mu.RUnlock()

	if bind != nil {
		bind.mu.Lock()
		stream := bind.tcpStreams[payload.SessionID]
		bind.mu.Unlock()
		if stream != nil && stream.peerID == peerID {
			if !stream.enqueueInbound(tcpFrameEvent{seq: payload.StreamSeq, payload: slices.Clone(payload.Payload)}) {
				logx.Warnf("bind %s inbound tcp queue full; closing stream %s", bind.name, payload.SessionID)
				c.recordEvent("tcp", peerID, stream.peerName, "tcp_bind_queue_overflow", fmt.Sprintf("bind=%s service=%s session=%s", stream.bindName, stream.service, stream.sessionID))
				stream.close()
			}
			return
		}
	}

	if proxy != nil && proxy.protocol == config.ServiceProtocolTCP {
		if !proxy.enqueueInbound(tcpFrameEvent{seq: payload.StreamSeq, payload: slices.Clone(payload.Payload)}) {
			logx.Warnf("tcp publish proxy inbound queue full; closing stream %s", payload.SessionID)
			c.recordEvent("tcp", peerID, proxy.peerName, "tcp_publish_queue_overflow", fmt.Sprintf("bind=%s service=%s session=%s target=%s", proxy.bindName, proxy.service, proxy.sessionID, proxy.target))
			proxy.close()
		}
	}
}

func (c *Client) handleTCPAck(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolTCP {
		return
	}

	c.mu.RLock()
	bind := c.binds[payload.BindName]
	proxy := c.serviceProxies[strings.Join([]string{peerID, payload.BindName, payload.Service, payload.SessionID}, "|")]
	c.mu.RUnlock()

	if bind != nil {
		bind.mu.Lock()
		stream := bind.tcpStreams[payload.SessionID]
		bind.mu.Unlock()
		if stream != nil && stream.peerID == peerID && stream.sender != nil {
			stream.touch()
			stream.sender.ack(payload.Ack)
			return
		}
	}

	if proxy != nil && proxy.sender != nil {
		proxy.touch()
		proxy.sender.ack(payload.Ack)
	}
}

func (c *Client) handleTCPCloseAck(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolTCP {
		return
	}

	c.mu.RLock()
	bind := c.binds[payload.BindName]
	proxy := c.serviceProxies[strings.Join([]string{peerID, payload.BindName, payload.Service, payload.SessionID}, "|")]
	c.mu.RUnlock()

	if bind != nil {
		bind.mu.Lock()
		stream := bind.tcpStreams[payload.SessionID]
		bind.mu.Unlock()
		if stream != nil && stream.peerID == peerID {
			stream.touch()
			stream.closeAcked.Store(true)
			return
		}
	}

	if proxy != nil {
		proxy.touch()
		proxy.closeAcked.Store(true)
	}
}

func (c *Client) sendTCPCloseAck(peerID string, payload proto.ServicePayload) {
	_ = c.sendServicePayload(peerID, proto.ServicePayload{
		Kind:      proto.DataKindTCPCloseAck,
		Protocol:  config.ServiceProtocolTCP,
		BindName:  payload.BindName,
		Service:   payload.Service,
		SessionID: payload.SessionID,
		Ack:       payload.Ack,
	})
}

func (c *Client) handleTCPClose(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolTCP {
		return
	}
	c.sendTCPCloseAck(peerID, payload)

	c.mu.RLock()
	bind := c.binds[payload.BindName]
	proxy := c.serviceProxies[strings.Join([]string{peerID, payload.BindName, payload.Service, payload.SessionID}, "|")]
	c.mu.RUnlock()

	if bind != nil {
		bind.mu.Lock()
		stream := bind.tcpStreams[payload.SessionID]
		bind.mu.Unlock()
		if stream != nil && stream.peerID == peerID {
			if !stream.enqueueInbound(tcpFrameEvent{close: true, errText: payload.Error, finalSeq: payload.Ack}) {
				stream.close()
			}
			return
		}
	}

	if proxy != nil {
		if !proxy.enqueueInbound(tcpFrameEvent{close: true, errText: payload.Error, finalSeq: payload.Ack}) {
			proxy.close()
		}
	}
}

func (c *Client) closeTCPBindOutbound(stream *tcpBindStream, errText string) {
	if stream == nil {
		return
	}
	finalSeq := uint64(0)
	if stream.sender != nil {
		finalSeq = stream.sender.finalSeq()
	}
	select {
	case <-stream.done:
		return
	default:
	}
	sendClose := func() error {
		return c.sendServicePayload(stream.peerID, proto.ServicePayload{
			Kind:      proto.DataKindTCPClose,
			Protocol:  config.ServiceProtocolTCP,
			BindName:  stream.bindName,
			Service:   stream.service,
			SessionID: stream.sessionID,
			Ack:       finalSeq,
			Error:     errText,
		})
	}
	drained, acked, closed := waitTCPCloseHandshake(stream.done, stream.sender, func() bool {
		return stream.closeAcked.Load()
	}, sendClose, tcpCloseFlushTimeout)
	if !closed && (!drained || !acked) {
		pending := 0
		if stream.sender != nil {
			pending = stream.sender.pendingCount()
		}
		c.recordEvent("tcp", stream.peerID, stream.peerName, "tcp_bind_close_timeout", fmt.Sprintf("bind=%s service=%s session=%s pending=%d close_acked=%t", stream.bindName, stream.service, stream.sessionID, pending, acked))
	}
	stream.close()
}

func (c *Client) closeTCPServiceProxyOutbound(proxy *serviceProxy, errText string) {
	if proxy == nil {
		return
	}
	finalSeq := uint64(0)
	if proxy.sender != nil {
		finalSeq = proxy.sender.finalSeq()
	}
	select {
	case <-proxy.done:
		return
	default:
	}
	sendClose := func() error {
		return c.sendServicePayload(proxy.peerID, proto.ServicePayload{
			Kind:      proto.DataKindTCPClose,
			Protocol:  config.ServiceProtocolTCP,
			BindName:  proxy.bindName,
			Service:   proxy.service,
			SessionID: proxy.sessionID,
			Ack:       finalSeq,
			Error:     errText,
		})
	}
	drained, acked, closed := waitTCPCloseHandshake(proxy.done, proxy.sender, func() bool {
		return proxy.closeAcked.Load()
	}, sendClose, tcpCloseFlushTimeout)
	if !closed && (!drained || !acked) {
		pending := 0
		if proxy.sender != nil {
			pending = proxy.sender.pendingCount()
		}
		c.recordEvent("tcp", proxy.peerID, proxy.peerName, "tcp_publish_close_timeout", fmt.Sprintf("bind=%s service=%s session=%s target=%s pending=%d close_acked=%t", proxy.bindName, proxy.service, proxy.sessionID, proxy.target, pending, acked))
	}
	proxy.close()
}
