package client

import (
	"errors"
	"fmt"
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
	lastSeen   atomic.Int64

	mu        sync.Mutex
	nextSeq   uint64
	pending   map[uint64][]byte
	openReady bool
	closeOnce sync.Once
	onClose   func()
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

func (c *Client) runTCPBind(bind *bindProxy) {
	for {
		conn, err := bind.tcp.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logx.Warnf("bind %s accept failed: %v", bind.name, err)
			return
		}
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

	if err := c.openTCPStream(stream); err != nil {
		logx.Warnf("bind %s open tcp stream failed: %v", bind.name, err)
		stream.close()
		return
	}

	go c.runTCPBindOutbound(stream)
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
			if err != nil {
				return err
			}
			stream.mu.Lock()
			stream.openReady = true
			stream.mu.Unlock()
			return nil
		case <-retry.C:
			if err := c.sendServicePayload(stream.peerID, payload); err != nil {
				return err
			}
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
	_ = c.sendServicePayload(stream.peerID, proto.ServicePayload{
		Kind:      proto.DataKindTCPClose,
		Protocol:  config.ServiceProtocolTCP,
		BindName:  stream.bindName,
		Service:   stream.service,
		SessionID: stream.sessionID,
	})
	stream.close()
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
			if strings.TrimSpace(frame.errText) != "" {
				logx.Infof("bind %s remote tcp close: %s", stream.bindName, frame.errText)
			}
			stream.close()
			return
		}

		stream.mu.Lock()
		ready, ack := processTCPInbound(&stream.nextSeq, stream.pending, frame.seq, frame.payload)
		stream.mu.Unlock()

		for _, chunk := range ready {
			if err := writeAll(stream.conn, chunk); err != nil {
				logx.Warnf("bind %s write to local tcp client failed: %v", stream.bindName, err)
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
		bindName:  bindName,
		service:   service,
		sessionID: sessionID,
		protocol:  config.ServiceProtocolTCP,
		tcpConn:   conn,
		inboundCh: make(chan tcpFrameEvent, tcpSendWindow*2),
		done:      make(chan struct{}),
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
	_ = c.sendServicePayload(proxy.peerID, proto.ServicePayload{
		Kind:      proto.DataKindTCPClose,
		Protocol:  config.ServiceProtocolTCP,
		BindName:  proxy.bindName,
		Service:   proxy.service,
		SessionID: proxy.sessionID,
	})
	proxy.close()
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
			proxy.close()
			return
		}

		proxy.mu.Lock()
		ready, ack := processTCPInbound(&proxy.nextSeq, proxy.pending, frame.seq, frame.payload)
		proxy.mu.Unlock()

		for _, chunk := range ready {
			if err := writeAll(proxy.tcpConn, chunk); err != nil {
				logx.Warnf("tcp publish proxy %s/%s write failed: %v", proxy.peerID, proxy.service, err)
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
	}
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
				stream.close()
			}
			return
		}
	}

	if proxy != nil && proxy.protocol == config.ServiceProtocolTCP {
		if !proxy.enqueueInbound(tcpFrameEvent{seq: payload.StreamSeq, payload: slices.Clone(payload.Payload)}) {
			logx.Warnf("tcp publish proxy inbound queue full; closing stream %s", payload.SessionID)
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

func (c *Client) handleTCPClose(peerID string, payload proto.ServicePayload) {
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
			if !stream.enqueueInbound(tcpFrameEvent{close: true, errText: payload.Error}) {
				stream.close()
			}
			return
		}
	}

	if proxy != nil {
		if !proxy.enqueueInbound(tcpFrameEvent{close: true, errText: payload.Error}) {
			proxy.close()
		}
	}
}
