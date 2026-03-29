package client

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

const (
	maxDatagramSize  = 64 * 1024
	replayWindowSize = 4096
	bindSessionTTL   = 2 * time.Minute
	serviceProxyTTL  = 2 * time.Minute
	tcpIdleTTL       = 24 * time.Hour
	traceEventLimit  = 80
	tcpChunkSize     = 1200
	tcpSendWindow    = 32
	tcpResendAfter   = 250 * time.Millisecond
	tcpOpenTimeout   = 5 * time.Second
)

var joinRequestTimeout = 6 * time.Second
var joinHTTPClient = http.DefaultClient

type Client struct {
	cfg           config.ClientConfig
	deviceID      string
	sessionToken  string
	networkKey    []byte
	heartbeat     time.Duration
	punchInterval time.Duration
	startedAt     time.Time

	serverUDPAddr *net.UDPAddr
	udpConn       *net.UDPConn
	adminServer   *http.Server
	adminAddr     string
	rejoinCh      chan string

	identityPublic  ed25519.PublicKey
	identityPrivate ed25519.PrivateKey

	mu                        sync.RWMutex
	observedAddr              string
	networkState              string
	lastRegisterAt            time.Time
	lastRegisterError         string
	rejoinCount               uint64
	lastRejoinReason          string
	lastRejoinAttemptAt       time.Time
	lastRejoinAt              time.Time
	lastRejoinError           string
	consecutiveRejoinFailures uint64
	peers                     map[string]*peerState
	binds                     map[string]*bindProxy
	serviceProxies            map[string]*serviceProxy
	traceEvents               []TraceEvent
}

type networkSnapshot struct {
	deviceID      string
	sessionToken  string
	networkKey    []byte
	heartbeat     time.Duration
	punchInterval time.Duration
	serverUDPAddr *net.UDPAddr
}

type peerState struct {
	info              proto.PeerInfo
	candidates        []*net.UDPAddr
	candidateStats    map[string]*candidateState
	handshake         *handshakeState
	session           *sessionState
	chosenAddr        *net.UDPAddr
	lastSeen          time.Time
	lastError         string
	routeReason       string
	routeChangedAt    time.Time
	lastOfflineReason string

	punchAttempts     uint64
	punchingLogged    bool
	establishedLogged bool
}

type candidateState struct {
	addr                string
	attempts            uint64
	firstAttemptAt      time.Time
	lastAttemptAt       time.Time
	lastInboundAt       time.Time
	lastSuccessAt       time.Time
	firstSuccessLatency time.Duration
	lastSuccessSource   string
}

type handshakeState struct {
	private *ecdh.PrivateKey
	public  []byte
	nonce   []byte
}

type sessionState struct {
	key           []byte
	sendSeq       atomic.Uint64
	seen          map[uint64]struct{}
	seenOrder     []uint64
	lastSeen      time.Time
	establishedAt time.Time
	sentPackets   atomic.Uint64
	recvPackets   atomic.Uint64
	sentBytes     atomic.Uint64
	recvBytes     atomic.Uint64
}

type bindProxy struct {
	name string
	cfg  config.BindConfig
	udp  *net.UDPConn
	tcp  net.Listener

	mu          sync.Mutex
	sessions    map[string]*bindSession
	tcpStreams  map[string]*tcpBindStream
	lastDropLog time.Time
}

type bindSession struct {
	appAddr  *net.UDPAddr
	lastSeen time.Time
}

type serviceProxy struct {
	key       string
	peerID    string
	bindName  string
	service   string
	sessionID string
	protocol  string
	udpConn   *net.UDPConn
	tcpConn   net.Conn
	sender    *tcpReliableSender
	inboundCh chan tcpFrameEvent
	done      chan struct{}
	lastSeen  atomic.Int64
	closeOnce sync.Once
	onClose   func()

	mu      sync.Mutex
	nextSeq uint64
	pending map[uint64][]byte
}

func Run(ctx context.Context, cfg config.ClientConfig) error {
	if err := config.ValidateClientConfig(&cfg); err != nil {
		return err
	}
	client := &Client{
		cfg:            cfg,
		peers:          map[string]*peerState{},
		binds:          map[string]*bindProxy{},
		serviceProxies: map[string]*serviceProxy{},
		rejoinCh:       make(chan string, 1),
	}
	return client.Run(ctx)
}

func (c *Client) Run(ctx context.Context) error {
	if _, err := logx.SetLevel(c.cfg.LogLevel); err != nil {
		return err
	}
	c.startedAt = time.Now()
	if c.rejoinCh == nil {
		c.rejoinCh = make(chan string, 1)
	}
	if err := c.ensureIdentityKey(); err != nil {
		return err
	}

	if err := c.joinNetwork(ctx); err != nil {
		return err
	}
	c.markInitialJoinSuccess()
	session := c.networkSnapshot()
	c.recordEvent("client", "", "", "joined_network", fmt.Sprintf("server_udp=%s", session.serverUDPAddr))

	udpAddr, err := net.ResolveUDPAddr("udp", c.cfg.UDPListen)
	if err != nil {
		return fmt.Errorf("resolve local udp listen addr: %w", err)
	}
	c.udpConn, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	defer c.shutdown()

	if err := c.startBindListeners(); err != nil {
		return err
	}
	if err := c.startAdminServer(); err != nil {
		return err
	}

	logx.Infof("client %s joined network local_udp=%s server_udp=%s admin=%s", c.cfg.DeviceName, c.udpConn.LocalAddr(), session.serverUDPAddr, firstNonEmpty(c.adminAddr, "disabled"))

	errCh := make(chan error, 1)
	go c.readLoop(ctx, errCh)
	go c.rejoinLoop(ctx)
	go c.registerLoop(ctx)
	go c.punchLoop(ctx)
	go c.keepaliveLoop(ctx)
	go c.cleanupLoop(ctx)

	if err := c.sendRegister(); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (c *Client) joinNetwork(ctx context.Context) error {
	if err := c.ensureIdentityKey(); err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, joinRequestTimeout)
	defer cancel()

	reqBody, err := json.Marshal(proto.JoinNetworkRequest{
		Password:       c.cfg.Password,
		DeviceName:     c.cfg.DeviceName,
		IdentityPublic: slices.Clone(c.identityPublic),
	})
	if err != nil {
		return fmt.Errorf("encode join request: %w", err)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.cfg.ServerURL+"/v1/network/join", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("create join request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := joinHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("join network: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read join response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("join network failed: %w", readHTTPErrorBody(resp.StatusCode, respBody))
	}

	var joinResp proto.JoinNetworkResponse
	if err := json.Unmarshal(respBody, &joinResp); err != nil {
		return fmt.Errorf("decode join network response: %w", err)
	}

	networkKey, err := secure.DeriveNetworkKey(c.cfg.Password)
	if err != nil {
		return fmt.Errorf("derive network key: %w", err)
	}
	serverUDPAddr, err := c.resolveServerUDPAddr(joinResp.UDPAddr)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.deviceID = joinResp.DeviceID
	c.sessionToken = joinResp.SessionToken
	c.networkKey = slices.Clone(networkKey)
	c.serverUDPAddr = cloneUDPAddr(serverUDPAddr)
	c.heartbeat = time.Duration(joinResp.HeartbeatSeconds) * time.Second
	c.punchInterval = time.Duration(joinResp.PunchIntervalMS) * time.Millisecond
	if c.heartbeat <= 0 {
		c.heartbeat = 15 * time.Second
	}
	if c.punchInterval <= 0 {
		c.punchInterval = 700 * time.Millisecond
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.leaveNetwork(ctx)
	c.closeResources()
}

func (c *Client) leaveNetwork(ctx context.Context) error {
	session := c.networkSnapshot()
	if session.deviceID == "" || session.sessionToken == "" {
		return nil
	}
	_, err := LeaveNetworkSession(ctx, c.cfg.ServerURL, proto.LeaveNetworkRequest{
		DeviceID:     session.deviceID,
		SessionToken: session.sessionToken,
	})
	if err == nil {
		c.recordEvent("client", "", "", "left_network", fmt.Sprintf("device_id=%s", session.deviceID))
		return nil
	}
	c.recordEvent("client", "", "", "leave_network_failed", err.Error())
	return err
}

func (c *Client) networkSnapshot() networkSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	heartbeat := c.heartbeat
	if heartbeat <= 0 {
		heartbeat = 15 * time.Second
	}
	punchInterval := c.punchInterval
	if punchInterval <= 0 {
		punchInterval = 700 * time.Millisecond
	}
	return networkSnapshot{
		deviceID:      c.deviceID,
		sessionToken:  c.sessionToken,
		networkKey:    slices.Clone(c.networkKey),
		heartbeat:     heartbeat,
		punchInterval: punchInterval,
		serverUDPAddr: cloneUDPAddr(c.serverUDPAddr),
	}
}

func (c *Client) resolveServerUDPAddr(raw string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid udp_addr %q: %w", raw, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		parsed, err := url.Parse(c.cfg.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("parse server_url: %w", err)
		}
		host = parsed.Hostname()
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
}

func (c *Client) startBindListeners() error {
	names := make([]string, 0, len(c.cfg.Binds))
	for name := range c.cfg.Binds {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		bindCfg := c.cfg.Binds[name]
		bind := &bindProxy{
			name:       name,
			cfg:        bindCfg,
			sessions:   map[string]*bindSession{},
			tcpStreams: map[string]*tcpBindStream{},
		}
		var listenAddr string
		switch bindCfg.Protocol {
		case config.ServiceProtocolTCP:
			ln, err := net.Listen("tcp", bindCfg.Local)
			if err != nil {
				return fmt.Errorf("listen tcp bind %s: %w", name, err)
			}
			bind.tcp = ln
			listenAddr = ln.Addr().String()
			go c.runTCPBind(bind)
		default:
			addr, err := net.ResolveUDPAddr("udp", bindCfg.Local)
			if err != nil {
				return fmt.Errorf("resolve bind %s local addr: %w", name, err)
			}
			conn, err := net.ListenUDP("udp", addr)
			if err != nil {
				return fmt.Errorf("listen bind %s: %w", name, err)
			}
			bind.udp = conn
			listenAddr = conn.LocalAddr().String()
			go c.runBind(bind)
		}
		c.mu.Lock()
		c.binds[name] = bind
		c.mu.Unlock()
		logx.Infof("bind %s listening on %s -> protocol=%s peer=%s service=%s", name, listenAddr, bindCfg.Protocol, bindCfg.Peer, bindCfg.Service)
	}
	return nil
}

func (c *Client) closeResources() {
	c.mu.Lock()
	udpConn := c.udpConn
	adminServer := c.adminServer

	binds := make([]*bindProxy, 0, len(c.binds))
	for _, bind := range c.binds {
		binds = append(binds, bind)
	}
	proxies := make([]*serviceProxy, 0, len(c.serviceProxies))
	for key, proxy := range c.serviceProxies {
		delete(c.serviceProxies, key)
		proxies = append(proxies, proxy)
	}
	c.mu.Unlock()

	if udpConn != nil {
		_ = udpConn.Close()
	}
	if adminServer != nil {
		_ = adminServer.Close()
	}
	for _, bind := range binds {
		if bind.udp != nil {
			_ = bind.udp.Close()
		}
		if bind.tcp != nil {
			_ = bind.tcp.Close()
		}
		var streams []*tcpBindStream
		bind.mu.Lock()
		for sessionID, stream := range bind.tcpStreams {
			delete(bind.tcpStreams, sessionID)
			streams = append(streams, stream)
		}
		bind.mu.Unlock()
		for _, stream := range streams {
			stream.close()
		}
	}
	for _, proxy := range proxies {
		proxy.close()
	}
}

func (c *Client) readLoop(ctx context.Context, errCh chan<- error) {
	buf := make([]byte, maxDatagramSize)
	for {
		n, addr, err := c.udpConn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			select {
			case errCh <- err:
			default:
			}
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		c.handleDatagram(addr, data)
	}
}

func (c *Client) registerLoop(ctx context.Context) {
	for {
		timer := time.NewTimer(max(3*time.Second, c.networkSnapshot().heartbeat/2))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := c.sendRegister(); err != nil {
				logx.Warnf("register failed: %v", err)
			}
		}
	}
}

func (c *Client) keepaliveLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peerIDs := c.establishedPeerIDs()
			for _, peerID := range peerIDs {
				payload := proto.ServicePayload{Kind: proto.DataKindKeepalive}
				if err := c.sendServicePayload(peerID, payload); err != nil {
					logx.Warnf("keepalive to %s failed: %v", peerID, err)
				}
			}
		}
	}
}

func (c *Client) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanupStaleSessions()
		}
	}
}

func (c *Client) punchLoop(ctx context.Context) {
	for {
		timer := time.NewTimer(c.networkSnapshot().punchInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			targets := c.punchTargets()
			for _, target := range targets {
				for _, addr := range target.addrs {
					c.sendEnvelope(addr, target.env)
				}
			}
		}
	}
}

type punchTarget struct {
	addrs []*net.UDPAddr
	env   proto.Envelope
}

func (c *Client) punchTargets() []punchTarget {
	session := c.networkSnapshot()

	c.mu.Lock()
	defer c.mu.Unlock()

	targets := make([]punchTarget, 0, len(c.peers))
	for _, peer := range c.peers {
		if len(peer.candidates) == 0 {
			continue
		}
		if peer.session != nil && time.Since(peer.session.lastSeen) < 20*time.Second {
			continue
		}
		if peer.handshake == nil {
			peer.handshake = c.newHandshakeLocked()
			if peer.handshake == nil {
				continue
			}
		}
		peer.punchAttempts++
		if !peer.punchingLogged {
			logx.Debugf("peer discovered: name=%s id=%s candidates=%v services=%v", firstNonEmpty(peer.info.DeviceName, peer.info.DeviceID), peer.info.DeviceID, udpAddrsToStrings(peer.candidates), serviceNames(peer.info.Services))
			peer.punchingLogged = true
			c.recordEventLocked("peer", peer.info.DeviceID, peer.info.DeviceName, "punch_started", fmt.Sprintf("candidates=%s", candidateListString(peer.candidates)))
		}
		for _, addr := range peer.candidates {
			noteCandidateAttemptLocked(peer, addr)
		}
		env := proto.Envelope{
			Type: proto.TypePunchHello,
			PunchHello: &proto.PunchHelloMessage{
				FromID:    session.deviceID,
				FromName:  c.cfg.DeviceName,
				Nonce:     slices.Clone(peer.handshake.nonce),
				Public:    slices.Clone(peer.handshake.public),
				MAC:       secure.ComputePunchMAC(session.networkKey, session.deviceID, peer.handshake.nonce, peer.handshake.public),
				Signature: mustSignPunchHello(c.identityPrivate, session.deviceID, peer.handshake.nonce, peer.handshake.public),
			},
		}
		targets = append(targets, punchTarget{
			addrs: cloneUDPAddrs(peer.candidates),
			env:   env,
		})
	}
	return targets
}

func (c *Client) sendRegister() error {
	session := c.networkSnapshot()
	if session.deviceID == "" || session.sessionToken == "" || session.serverUDPAddr == nil {
		return errors.New("network session is not ready")
	}

	services := make([]proto.ServiceInfo, 0, len(c.cfg.Publish))
	for name, publish := range c.cfg.Publish {
		services = append(services, proto.ServiceInfo{Name: name, Protocol: publish.Protocol})
	}
	slices.SortFunc(services, func(a, b proto.ServiceInfo) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		case a.Protocol < b.Protocol:
			return -1
		case a.Protocol > b.Protocol:
			return 1
		default:
			return 0
		}
	})

	localAddr := c.udpConn.LocalAddr().(*net.UDPAddr)
	env := proto.Envelope{
		Type: proto.TypeRegister,
		Register: &proto.RegisterMessage{
			DeviceID:   session.deviceID,
			DeviceName: c.cfg.DeviceName,
			Token:      session.sessionToken,
			Candidates: gatherLocalCandidates(localAddr.Port),
			Services:   services,
		},
	}
	err := c.sendEnvelope(session.serverUDPAddr, env)
	c.markRegisterResult(err)
	return err
}

func (c *Client) handleDatagram(addr *net.UDPAddr, raw []byte) {
	env, err := proto.UnmarshalEnvelope(raw)
	if err != nil {
		logx.Debugf("invalid udp envelope from %s: %v", addr, err)
		return
	}

	switch env.Type {
	case proto.TypeRegisterAck:
		if env.RegisterAck != nil {
			c.handleRegisterAck(env.RegisterAck)
		}
	case proto.TypePeerSync:
		if env.PeerSync != nil {
			c.handlePeerSync(env.PeerSync)
		}
	case proto.TypePunchHello:
		if env.PunchHello != nil {
			c.handlePunchHello(addr, env.PunchHello)
		}
	case proto.TypeData:
		if env.Data != nil {
			c.handleData(addr, env.Data)
		}
	case proto.TypeError:
		if env.Error != nil {
			c.handleError(addr, env.Error)
		}
	default:
		logx.Warnf("unknown udp envelope type=%s from %s", env.Type, addr)
	}
}

func (c *Client) handleRegisterAck(msg *proto.RegisterAckMessage) {
	c.mu.Lock()
	previous := c.observedAddr
	c.observedAddr = msg.ObservedAddr
	c.mu.Unlock()
	if previous != msg.ObservedAddr {
		logx.Infof("server observed public udp addr: %s", msg.ObservedAddr)
		c.recordEvent("client", "", "", "observed_addr_changed", msg.ObservedAddr)
	}
}

func (c *Client) handleError(addr *net.UDPAddr, msg *proto.ErrorMessage) {
	logx.Warnf("server/client error from %s: %s", addr, msg.Message)
	c.recordEvent("client", "", "", "remote_error", fmt.Sprintf("addr=%s message=%s", addr, msg.Message))
	if strings.Contains(strings.ToLower(msg.Message), "invalid device session") {
		c.requestRejoin("invalid_device_session")
	}
}

func (c *Client) handlePeerSync(msg *proto.PeerSyncMessage) {
	session := c.networkSnapshot()

	var proxiesToClose []*serviceProxy
	var streamsToClose []*tcpBindStream
	c.mu.Lock()
	now := time.Now()
	seen := map[string]struct{}{}
	for _, info := range msg.Peers {
		if info.DeviceID == session.deviceID {
			continue
		}
		peer := c.peers[info.DeviceID]
		oldCandidates := []string(nil)
		oldServices := []string(nil)
		oldName := ""
		if peer == nil {
			peer = &peerState{}
			c.peers[info.DeviceID] = peer
		} else {
			oldCandidates = udpAddrsToStrings(peer.candidates)
			oldServices = serviceNames(peer.info.Services)
			oldName = peer.info.DeviceName
		}
		peer.info = info
		peer.candidates = resolveCandidateAddrs(info.Candidates)
		syncCandidateStatsLocked(peer, peer.candidates)
		peer.lastSeen = now
		if peer.handshake == nil {
			peer.handshake = c.newHandshakeLocked()
		}
		if oldName == "" {
			logx.Debugf("peer announced: name=%s id=%s candidates=%v services=%v", firstNonEmpty(info.DeviceName, info.DeviceID), info.DeviceID, info.Candidates, serviceNames(info.Services))
			c.recordEventLocked("peer", info.DeviceID, info.DeviceName, "peer_announced", fmt.Sprintf("candidates=%s services=%s", strings.Join(info.Candidates, ","), strings.Join(serviceNames(info.Services), ",")))
		} else if !slices.Equal(oldCandidates, info.Candidates) || !slices.Equal(oldServices, serviceNames(info.Services)) || oldName != info.DeviceName {
			logx.Debugf("peer updated: name=%s id=%s candidates=%v services=%v", firstNonEmpty(info.DeviceName, info.DeviceID), info.DeviceID, info.Candidates, serviceNames(info.Services))
			c.recordEventLocked("peer", info.DeviceID, info.DeviceName, "peer_updated", fmt.Sprintf("candidates=%s services=%s", strings.Join(info.Candidates, ","), strings.Join(serviceNames(info.Services), ",")))
		}
		seen[info.DeviceID] = struct{}{}
	}
	for deviceID := range c.peers {
		if _, ok := seen[deviceID]; ok {
			continue
		}
		logx.Infof("peer offline: name=%s id=%s", firstNonEmpty(c.peers[deviceID].info.DeviceName, deviceID), deviceID)
		c.peers[deviceID].lastOfflineReason = "removed_from_server_peer_list"
		c.recordEventLocked("peer", deviceID, c.peers[deviceID].info.DeviceName, "peer_offline", "removed_from_server_peer_list")
		proxies, streams := c.dropPeerLocked(deviceID)
		proxiesToClose = append(proxiesToClose, proxies...)
		streamsToClose = append(streamsToClose, streams...)
		delete(c.peers, deviceID)
	}
	c.mu.Unlock()

	for _, proxy := range proxiesToClose {
		proxy.close()
	}
	for _, stream := range streamsToClose {
		stream.close()
	}
}

func (c *Client) handlePunchHello(addr *net.UDPAddr, msg *proto.PunchHelloMessage) {
	session := c.networkSnapshot()
	if !secure.VerifyPunchMAC(session.networkKey, msg.FromID, msg.Nonce, msg.Public, msg.MAC) {
		logx.Debugf("drop invalid punch hello from %s", addr)
		c.recordEvent("peer", msg.FromID, msg.FromName, "punch_rejected", fmt.Sprintf("invalid_mac addr=%s", addr))
		return
	}

	c.mu.Lock()
	peer := c.peers[msg.FromID]
	if peer == nil {
		c.mu.Unlock()
		logx.Debugf("ignore punch hello from unknown peer id=%s name=%s addr=%s", msg.FromID, msg.FromName, addr)
		c.recordEvent("peer", msg.FromID, msg.FromName, "punch_ignored", fmt.Sprintf("unknown_peer addr=%s", addr))
		return
	}
	identityPublic := slices.Clone(peer.info.IdentityPublic)
	if !secure.VerifyPunchHelloSignature(identityPublic, msg.FromID, msg.Nonce, msg.Public, msg.Signature) {
		c.mu.Unlock()
		logx.Debugf("drop invalid punch signature from %s for peer=%s", addr, msg.FromID)
		c.recordEvent("peer", msg.FromID, msg.FromName, "punch_rejected", fmt.Sprintf("invalid_signature addr=%s", addr))
		return
	}
	noteCandidateInboundLocked(peer, addr)
	if peer.handshake == nil {
		peer.handshake = c.newHandshakeLocked()
		if peer.handshake == nil {
			c.mu.Unlock()
			return
		}
	}
	peerPub, err := secure.ParsePeerPublicKey(msg.Public)
	if err != nil {
		c.mu.Unlock()
		logx.Warnf("parse peer public key from %s failed: %v", addr, err)
		return
	}
	sharedSecret, err := peer.handshake.private.ECDH(peerPub)
	if err != nil {
		c.mu.Unlock()
		logx.Warnf("derive shared secret with %s failed: %v", msg.FromID, err)
		return
	}
	key, err := secure.DeriveSessionKey(session.networkKey, session.deviceID, msg.FromID, peer.handshake.nonce, msg.Nonce, peer.handshake.public, msg.Public, sharedSecret)
	if err != nil {
		c.mu.Unlock()
		logx.Warnf("derive session key with %s failed: %v", msg.FromID, err)
		return
	}
	reply := proto.Envelope{
		Type: proto.TypePunchHello,
		PunchHello: &proto.PunchHelloMessage{
			FromID:    session.deviceID,
			FromName:  c.cfg.DeviceName,
			Nonce:     slices.Clone(peer.handshake.nonce),
			Public:    slices.Clone(peer.handshake.public),
			MAC:       secure.ComputePunchMAC(session.networkKey, session.deviceID, peer.handshake.nonce, peer.handshake.public),
			Signature: mustSignPunchHello(c.identityPrivate, session.deviceID, peer.handshake.nonce, peer.handshake.public),
		},
	}
	if peer.session == nil {
		peer.session = &sessionState{
			key:           key,
			seen:          map[uint64]struct{}{},
			lastSeen:      time.Now(),
			establishedAt: time.Now(),
		}
		logx.Infof("p2p established: peer=%s id=%s addr=%s", firstNonEmpty(peer.info.DeviceName, msg.FromID), msg.FromID, addr)
	} else {
		peer.session.key = key
		peer.session.lastSeen = time.Now()
		if peer.chosenAddr == nil || peer.chosenAddr.String() != addr.String() {
			logx.Infof("p2p route updated: peer=%s id=%s addr=%s", firstNonEmpty(peer.info.DeviceName, msg.FromID), msg.FromID, addr)
		}
	}
	setPeerRouteLocked(c, peer, addr, "received_punch_hello")
	peer.lastSeen = time.Now()
	peer.info.DeviceName = firstNonEmpty(peer.info.DeviceName, msg.FromName)
	peer.lastError = ""
	if !containsUDPAddr(peer.candidates, addr) {
		peer.candidates = append(peer.candidates, cloneUDPAddr(addr))
	}
	c.mu.Unlock()

	c.sendEnvelope(addr, reply)
}

func (c *Client) requestRejoin(reason string) {
	if c.rejoinCh == nil {
		return
	}
	c.markRejoinRequested(reason)
	c.recordEvent("client", "", "", "rejoin_requested", reason)
	select {
	case c.rejoinCh <- reason:
	default:
	}
}

func (c *Client) rejoinLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case reason := <-c.rejoinCh:
			c.rejoinUntilSuccess(ctx, reason)
		}
	}
}

func (c *Client) rejoinUntilSuccess(ctx context.Context, reason string) {
	for attempt := 1; ; attempt++ {
		c.markRejoinAttempt(reason)
		c.recordEvent("client", "", "", "rejoin_started", fmt.Sprintf("reason=%s attempt=%d", reason, attempt))
		if err := c.rejoinNetwork(ctx, reason); err == nil {
			c.markRejoinSuccess(reason)
			return
		} else {
			logx.Warnf("rejoin failed: reason=%s attempt=%d err=%v", reason, attempt, err)
			c.markRejoinFailure(reason, err)
			c.recordEvent("client", "", "", "rejoin_failed", fmt.Sprintf("reason=%s attempt=%d err=%v", reason, attempt, err))
		}

		delay := time.Duration(attempt) * 2 * time.Second
		if delay > 15*time.Second {
			delay = 15 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Client) rejoinNetwork(ctx context.Context, reason string) error {
	previous := c.networkSnapshot()
	if err := c.joinNetwork(ctx); err != nil {
		return err
	}
	current := c.networkSnapshot()

	c.resetTransportState(reason)
	if err := c.sendRegister(); err != nil {
		return fmt.Errorf("send register after rejoin: %w", err)
	}

	logx.Infof("client %s rejoined network reason=%s old_device_id=%s new_device_id=%s server_udp=%s", c.cfg.DeviceName, reason, previous.deviceID, current.deviceID, current.serverUDPAddr)
	c.recordEvent("client", "", "", "rejoined_network", fmt.Sprintf("reason=%s old_device_id=%s new_device_id=%s server_udp=%s", reason, previous.deviceID, current.deviceID, current.serverUDPAddr))
	return nil
}

func (c *Client) handleData(addr *net.UDPAddr, msg *proto.DataMessage) {
	c.mu.Lock()
	peer := c.peers[msg.FromID]
	if peer == nil || peer.session == nil {
		c.mu.Unlock()
		return
	}
	if _, ok := peer.session.seen[msg.Seq]; ok {
		c.mu.Unlock()
		return
	}
	plaintext, err := secure.DecryptPacket(peer.session.key, msg.Seq, msg.Ciphertext)
	if err != nil {
		c.mu.Unlock()
		logx.Warnf("decrypt packet from %s failed: %v", msg.FromID, err)
		return
	}
	previousRoute := udpAddrString(peer.chosenAddr)
	peer.lastSeen = time.Now()
	peer.session.lastSeen = time.Now()
	noteCandidateSuccessLocked(peer, addr, "received_encrypted_data")
	recordReplayLocked(peer.session, msg.Seq)
	session := peer.session
	if peer.routeReason == "" || previousRoute != addr.String() {
		setPeerRouteLocked(c, peer, addr, "received_encrypted_data")
	} else {
		peer.chosenAddr = cloneUDPAddr(addr)
	}
	c.mu.Unlock()

	payload, err := proto.UnmarshalServicePayload(plaintext)
	if err != nil {
		logx.Warnf("decode service payload from %s failed: %v", msg.FromID, err)
		return
	}
	session.recvPackets.Add(1)
	session.recvBytes.Add(uint64(len(payload.Payload)))

	switch payload.Kind {
	case proto.DataKindKeepalive:
		return
	case proto.DataKindRequest:
		c.handleServiceRequest(msg.FromID, payload)
	case proto.DataKindResponse:
		c.handleServiceResponse(msg.FromID, payload)
	case proto.DataKindTCPOpen:
		c.handleTCPOpen(msg.FromID, payload)
	case proto.DataKindTCPOk:
		c.handleTCPOpenResult(msg.FromID, payload)
	case proto.DataKindTCPData:
		c.handleTCPData(msg.FromID, payload)
	case proto.DataKindTCPAck:
		c.handleTCPAck(msg.FromID, payload)
	case proto.DataKindTCPClose:
		c.handleTCPClose(msg.FromID, payload)
	default:
		logx.Warnf("unknown service payload kind=%s from %s", payload.Kind, msg.FromID)
	}
}

func (c *Client) handleServiceRequest(peerID string, payload proto.ServicePayload) {
	if payload.Protocol != "" && payload.Protocol != config.ServiceProtocolUDP {
		return
	}
	publish, ok := c.cfg.Publish[payload.Service]
	if !ok {
		logx.Debugf("service request for unknown publish=%s from %s", payload.Service, peerID)
		return
	}
	if publish.Protocol != config.ServiceProtocolUDP {
		logx.Debugf("service request for non-udp publish=%s from %s", payload.Service, peerID)
		return
	}

	proxy, err := c.getOrCreateServiceProxy(peerID, payload.BindName, payload.Service, payload.SessionID, publish.Local)
	if err != nil {
		logx.Warnf("service proxy %s/%s create failed: %v", peerID, payload.Service, err)
		return
	}
	proxy.touch()
	if _, err := proxy.udpConn.Write(payload.Payload); err != nil {
		logx.Warnf("write to local publish service %s failed: %v", payload.Service, err)
	}
}

func (c *Client) handleServiceResponse(peerID string, payload proto.ServicePayload) {
	c.mu.RLock()
	bind := c.binds[payload.BindName]
	peer := c.peers[peerID]
	c.mu.RUnlock()
	if bind == nil || peer == nil {
		return
	}
	if bind.cfg.Protocol != config.ServiceProtocolUDP || peer.info.DeviceName != bind.cfg.Peer {
		return
	}

	bind.mu.Lock()
	session := bind.sessions[payload.SessionID]
	if session != nil {
		session.lastSeen = time.Now()
	}
	bind.mu.Unlock()
	if session == nil {
		return
	}
	if _, err := bind.udp.WriteToUDP(payload.Payload, session.appAddr); err != nil {
		logx.Warnf("write bind response %s failed: %v", bind.name, err)
	}
}

func (c *Client) getOrCreateServiceProxy(peerID, bindName, service, sessionID, target string) (*serviceProxy, error) {
	key := strings.Join([]string{peerID, bindName, service, sessionID}, "|")

	c.mu.RLock()
	existing := c.serviceProxies[key]
	c.mu.RUnlock()
	if existing != nil {
		existing.touch()
		return existing, nil
	}

	targetAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		return nil, fmt.Errorf("resolve publish target: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("dial publish target: %w", err)
	}

	proxy := &serviceProxy{
		key:       key,
		peerID:    peerID,
		bindName:  bindName,
		service:   service,
		sessionID: sessionID,
		protocol:  config.ServiceProtocolUDP,
		udpConn:   conn,
		done:      make(chan struct{}),
	}
	proxy.touch()

	c.mu.Lock()
	if existing = c.serviceProxies[key]; existing != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return existing, nil
	}
	c.serviceProxies[key] = proxy
	c.mu.Unlock()

	go c.runServiceProxy(proxy)
	return proxy, nil
}

func (c *Client) runServiceProxy(proxy *serviceProxy) {
	buf := make([]byte, maxDatagramSize)
	for {
		n, err := proxy.udpConn.Read(buf)
		if err != nil {
			return
		}
		proxy.touch()
		payload := proto.ServicePayload{
			Kind:      proto.DataKindResponse,
			BindName:  proxy.bindName,
			Service:   proxy.service,
			SessionID: proxy.sessionID,
			Payload:   slices.Clone(buf[:n]),
		}
		if err := c.sendServicePayload(proxy.peerID, payload); err != nil {
			logx.Warnf("service proxy response send failed: %v", err)
		}
	}
}

func (c *Client) runBind(bind *bindProxy) {
	buf := make([]byte, maxDatagramSize)
	for {
		n, appAddr, err := bind.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}

		peerID, _, err := c.peerIDForBind(bind.cfg.Peer, bind.cfg.Service, bind.cfg.Protocol)
		if err != nil {
			bind.logDrop(bind.name, err)
			continue
		}

		sessionID := appAddr.String()
		bind.mu.Lock()
		bind.sessions[sessionID] = &bindSession{
			appAddr:  cloneUDPAddr(appAddr),
			lastSeen: time.Now(),
		}
		bind.mu.Unlock()

		payload := proto.ServicePayload{
			Kind:      proto.DataKindRequest,
			BindName:  bind.name,
			Service:   bind.cfg.Service,
			SessionID: sessionID,
			Payload:   slices.Clone(buf[:n]),
		}
		if err := c.sendServicePayload(peerID, payload); err != nil {
			logx.Warnf("bind %s send failed: %v", bind.name, err)
		}
	}
}

func (c *Client) peerIDForBind(peerName, service, protocol string) (string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, peer := range c.peers {
		if peer.info.DeviceName != peerName {
			continue
		}
		if peer.session == nil || peer.chosenAddr == nil {
			return "", "", fmt.Errorf("peer %s has no established p2p session", peerName)
		}
		if !peerAdvertisesService(peer.info, service, protocol) {
			return "", "", fmt.Errorf("peer %s does not publish service %s/%s", peerName, service, protocol)
		}
		return peer.info.DeviceID, peer.info.DeviceName, nil
	}
	return "", "", fmt.Errorf("peer %s is not online", peerName)
}

func (c *Client) sendServicePayload(peerID string, payload proto.ServicePayload) error {
	network := c.networkSnapshot()
	plaintext, err := proto.MarshalServicePayload(payload)
	if err != nil {
		return fmt.Errorf("encode service payload: %w", err)
	}

	c.mu.RLock()
	peer := c.peers[peerID]
	if peer == nil || peer.session == nil || peer.chosenAddr == nil {
		c.mu.RUnlock()
		return errors.New("peer session is not ready")
	}
	sessionKey := slices.Clone(peer.session.key)
	addr := cloneUDPAddr(peer.chosenAddr)
	seq := peer.session.sendSeq.Add(1)
	peerSession := peer.session
	c.mu.RUnlock()

	ciphertext, err := secure.EncryptPacket(sessionKey, seq, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt service payload: %w", err)
	}
	if err := c.sendEnvelope(addr, proto.Envelope{
		Type: proto.TypeData,
		Data: &proto.DataMessage{
			FromID:     network.deviceID,
			Seq:        seq,
			Ciphertext: ciphertext,
		},
	}); err != nil {
		c.recordPeerError(peerID, err)
		return err
	}
	peerSession.sentPackets.Add(1)
	peerSession.sentBytes.Add(uint64(len(payload.Payload)))
	c.clearPeerError(peerID)
	return nil
}

func (c *Client) sendEnvelope(addr *net.UDPAddr, env proto.Envelope) error {
	raw, err := proto.MarshalEnvelope(env)
	if err != nil {
		return err
	}
	_, err = c.udpConn.WriteToUDP(raw, addr)
	return err
}

func (c *Client) establishedPeerIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peerIDs := make([]string, 0, len(c.peers))
	for deviceID, peer := range c.peers {
		if peer.session == nil || peer.chosenAddr == nil {
			continue
		}
		peerIDs = append(peerIDs, deviceID)
	}
	return peerIDs
}

func (c *Client) newHandshakeLocked() *handshakeState {
	priv, pub, nonce, err := secure.NewEphemeralKey()
	if err != nil {
		logx.Warnf("create handshake state failed: %v", err)
		return nil
	}
	return &handshakeState{
		private: priv,
		public:  pub,
		nonce:   nonce,
	}
}

func (c *Client) ensureIdentityKey() error {
	if len(c.identityPublic) == ed25519.PublicKeySize && len(c.identityPrivate) == ed25519.PrivateKeySize {
		return nil
	}

	if strings.TrimSpace(c.cfg.IdentityPrivate) != "" {
		publicKey, privateKey, err := secure.ParseIdentityPrivate(c.cfg.IdentityPrivate)
		if err != nil {
			return fmt.Errorf("parse identity_private: %w", err)
		}
		c.identityPublic = publicKey
		c.identityPrivate = privateKey
		return nil
	}

	publicKey, privateKey, err := secure.NewIdentityKey()
	if err != nil {
		return fmt.Errorf("create identity key: %w", err)
	}
	encoded, err := secure.EncodeIdentityPrivate(privateKey)
	if err != nil {
		return fmt.Errorf("encode identity key: %w", err)
	}
	c.cfg.IdentityPrivate = encoded
	c.identityPublic = publicKey
	c.identityPrivate = privateKey
	return nil
}

func mustSignPunchHello(privateKey ed25519.PrivateKey, fromID string, nonce, public []byte) []byte {
	signature, err := secure.SignPunchHello(privateKey, fromID, nonce, public)
	if err != nil {
		logx.Warnf("sign punch hello failed for %s: %v", fromID, err)
		return nil
	}
	return signature
}

func recordReplayLocked(session *sessionState, seq uint64) {
	session.seen[seq] = struct{}{}
	session.seenOrder = append(session.seenOrder, seq)
	if len(session.seenOrder) <= replayWindowSize {
		return
	}
	oldest := session.seenOrder[0]
	session.seenOrder = session.seenOrder[1:]
	delete(session.seen, oldest)
}

func gatherLocalCandidates(port int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addrIP(addr)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				candidate := net.JoinHostPort(v4.String(), strconv.Itoa(port))
				if _, ok := seen[candidate]; ok {
					continue
				}
				seen[candidate] = struct{}{}
				out = append(out, candidate)
				continue
			}
			if !ip.IsGlobalUnicast() {
				continue
			}
			candidate := net.JoinHostPort(ip.String(), strconv.Itoa(port))
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	slices.Sort(out)
	return out
}

func resolveCandidateAddrs(candidates []string) []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		addr, err := net.ResolveUDPAddr("udp", candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[addr.String()]; ok {
			continue
		}
		seen[addr.String()] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func addrIP(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

func peerAdvertisesService(peer proto.PeerInfo, service, protocol string) bool {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		protocol = config.ServiceProtocolUDP
	}
	for _, candidate := range peer.Services {
		if candidate.Name != service {
			continue
		}
		advertisedProtocol := strings.TrimSpace(candidate.Protocol)
		if advertisedProtocol == "" {
			advertisedProtocol = config.ServiceProtocolUDP
		}
		if advertisedProtocol == protocol {
			return true
		}
	}
	return false
}

func cloneUDPAddrs(in []*net.UDPAddr) []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, len(in))
	for _, addr := range in {
		out = append(out, cloneUDPAddr(addr))
	}
	return out
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	copyAddr := *addr
	if addr.IP != nil {
		copyAddr.IP = slices.Clone(addr.IP)
	}
	return &copyAddr
}

func containsUDPAddr(in []*net.UDPAddr, target *net.UDPAddr) bool {
	for _, addr := range in {
		if addr.String() == target.String() {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
