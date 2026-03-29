package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
)

type StatusSnapshot struct {
	GeneratedAt               time.Time             `json:"generated_at"`
	StartedAt                 time.Time             `json:"started_at"`
	DeviceID                  string                `json:"device_id"`
	DeviceName                string                `json:"device_name"`
	NetworkState              string                `json:"network_state"`
	LocalUDPAddr              string                `json:"local_udp_addr,omitempty"`
	ObservedAddr              string                `json:"observed_addr,omitempty"`
	ServerUDPAddr             string                `json:"server_udp_addr,omitempty"`
	AdminAddr                 string                `json:"admin_addr,omitempty"`
	LogLevel                  string                `json:"log_level"`
	HeartbeatSeconds          int                   `json:"heartbeat_seconds"`
	PunchIntervalMS           int                   `json:"punch_interval_ms"`
	LastRegisterAt            time.Time             `json:"last_register_at,omitempty"`
	LastRegisterError         string                `json:"last_register_error,omitempty"`
	RejoinCount               uint64                `json:"rejoin_count"`
	LastRejoinReason          string                `json:"last_rejoin_reason,omitempty"`
	LastRejoinAttemptAt       time.Time             `json:"last_rejoin_attempt_at,omitempty"`
	LastRejoinAt              time.Time             `json:"last_rejoin_at,omitempty"`
	LastRejoinError           string                `json:"last_rejoin_error,omitempty"`
	ConsecutiveRejoinFailures uint64                `json:"consecutive_rejoin_failures"`
	ActiveServiceProxies      int                   `json:"active_service_proxies"`
	Publish                   []PublishStatus       `json:"publish"`
	Binds                     []BindStatus          `json:"binds"`
	TCPBindStreams            []TCPBindStreamStatus `json:"tcp_bind_streams,omitempty"`
	TCPProxies                []TCPProxyStatus      `json:"tcp_proxies,omitempty"`
	Peers                     []PeerStatus          `json:"peers"`
	RecentEvents              []TraceEvent          `json:"recent_events,omitempty"`
}

type PublishStatus struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Local    string `json:"local"`
}

type BindStatus struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	ListenAddr     string `json:"listen_addr"`
	Peer           string `json:"peer"`
	Service        string `json:"service"`
	ActiveSessions int    `json:"active_sessions"`
}

type TCPBindStreamStatus struct {
	BindName        string    `json:"bind_name"`
	PeerID          string    `json:"peer_id,omitempty"`
	PeerName        string    `json:"peer_name,omitempty"`
	Service         string    `json:"service"`
	SessionID       string    `json:"session_id"`
	State           string    `json:"state"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	LastSeen        time.Time `json:"last_seen,omitempty"`
	BufferedInbound int       `json:"buffered_inbound"`
	UnackedOutbound int       `json:"unacked_outbound"`
}

type TCPProxyStatus struct {
	PeerID          string    `json:"peer_id,omitempty"`
	PeerName        string    `json:"peer_name,omitempty"`
	BindName        string    `json:"bind_name"`
	Service         string    `json:"service"`
	SessionID       string    `json:"session_id"`
	State           string    `json:"state"`
	Target          string    `json:"target,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	LastSeen        time.Time `json:"last_seen,omitempty"`
	BufferedInbound int       `json:"buffered_inbound"`
	UnackedOutbound int       `json:"unacked_outbound"`
}

type PeerStatus struct {
	DeviceID             string              `json:"device_id"`
	DeviceName           string              `json:"device_name"`
	State                string              `json:"state"`
	ObservedAddr         string              `json:"observed_addr,omitempty"`
	ChosenAddr           string              `json:"chosen_addr,omitempty"`
	Candidates           []string            `json:"candidates,omitempty"`
	Services             []string            `json:"services,omitempty"`
	ServiceDetails       []proto.ServiceInfo `json:"service_details,omitempty"`
	LastSeen             time.Time           `json:"last_seen,omitempty"`
	SessionEstablishedAt time.Time           `json:"session_established_at,omitempty"`
	SessionLastSeen      time.Time           `json:"session_last_seen,omitempty"`
	PunchAttempts        uint64              `json:"punch_attempts"`
	SentPackets          uint64              `json:"sent_packets"`
	RecvPackets          uint64              `json:"recv_packets"`
	SentBytes            uint64              `json:"sent_bytes"`
	RecvBytes            uint64              `json:"recv_bytes"`
	LastError            string              `json:"last_error,omitempty"`
	RouteReason          string              `json:"route_reason,omitempty"`
	RouteChangedAt       time.Time           `json:"route_changed_at,omitempty"`
	LastOfflineReason    string              `json:"last_offline_reason,omitempty"`
	CandidateStats       []CandidateStatus   `json:"candidate_stats,omitempty"`
}

type CandidateStatus struct {
	Addr                  string    `json:"addr"`
	CurrentRoute          bool      `json:"current_route"`
	Attempts              uint64    `json:"attempts"`
	FirstAttemptAt        time.Time `json:"first_attempt_at,omitempty"`
	LastAttemptAt         time.Time `json:"last_attempt_at,omitempty"`
	LastInboundAt         time.Time `json:"last_inbound_at,omitempty"`
	LastSuccessAt         time.Time `json:"last_success_at,omitempty"`
	FirstSuccessLatencyMS int64     `json:"first_success_latency_ms,omitempty"`
	LastSuccessSource     string    `json:"last_success_source,omitempty"`
}

type TraceEvent struct {
	At       time.Time `json:"at"`
	Scope    string    `json:"scope"`
	PeerID   string    `json:"peer_id,omitempty"`
	PeerName string    `json:"peer_name,omitempty"`
	Event    string    `json:"event"`
	Detail   string    `json:"detail,omitempty"`
}

func FetchStatus(ctx context.Context, cfg config.ClientConfig) (StatusSnapshot, error) {
	if cfg.AdminListen == "" {
		return StatusSnapshot{}, errors.New("client config missing admin_listen")
	}

	u, err := statusURLFromListen(cfg.AdminListen)
	if err != nil {
		return StatusSnapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return StatusSnapshot{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return StatusSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusSnapshot{}, fmt.Errorf("status endpoint returned %s", resp.Status)
	}

	var snapshot StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return StatusSnapshot{}, err
	}
	return snapshot, nil
}

func (c *Client) startAdminServer() error {
	if c.cfg.AdminListen == "" {
		return nil
	}
	if err := config.ValidateAdminListen(c.cfg.AdminListen); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", c.cfg.AdminListen)
	if err != nil {
		return fmt.Errorf("listen admin http: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/status", c.handleStatus)
	mux.HandleFunc("/log-level", c.handleLogLevel)

	srv := &http.Server{
		Handler: mux,
	}
	c.adminServer = srv
	c.adminAddr = ln.Addr().String()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Warnf("admin http server exited: %v", err)
		}
	}()
	return nil
}

func (c *Client) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(c.snapshotStatus()); err != nil {
		http.Error(w, "encode status failed", http.StatusInternalServerError)
	}
}

func (c *Client) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c.writeLogLevelResponse(w, logx.CurrentLevel())
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read request body failed", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req proto.LogLevelUpdateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		level, err := logx.SetLevel(req.LogLevel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		c.mu.Lock()
		c.cfg.LogLevel = level
		c.mu.Unlock()
		c.writeLogLevelResponse(w, level)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *Client) writeLogLevelResponse(w http.ResponseWriter, level string) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(proto.LogLevelResponse{LogLevel: level}); err != nil {
		http.Error(w, "encode log level failed", http.StatusInternalServerError)
	}
}

func (c *Client) snapshotStatus() StatusSnapshot {
	session := c.networkSnapshot()
	snapshot := StatusSnapshot{
		GeneratedAt:      time.Now(),
		StartedAt:        c.startedAt,
		DeviceID:         session.deviceID,
		DeviceName:       c.cfg.DeviceName,
		AdminAddr:        c.adminAddr,
		LogLevel:         logx.CurrentLevel(),
		HeartbeatSeconds: int(session.heartbeat / time.Second),
		PunchIntervalMS:  int(session.punchInterval / time.Millisecond),
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot.ObservedAddr = c.observedAddr
	snapshot.NetworkState = firstNonEmpty(c.networkState, "joined")
	if c.udpConn != nil {
		snapshot.LocalUDPAddr = c.udpConn.LocalAddr().String()
	}
	if session.serverUDPAddr != nil {
		snapshot.ServerUDPAddr = session.serverUDPAddr.String()
	}
	snapshot.LastRegisterAt = c.lastRegisterAt
	snapshot.LastRegisterError = c.lastRegisterError
	snapshot.RejoinCount = c.rejoinCount
	snapshot.LastRejoinReason = c.lastRejoinReason
	snapshot.LastRejoinAttemptAt = c.lastRejoinAttemptAt
	snapshot.LastRejoinAt = c.lastRejoinAt
	snapshot.LastRejoinError = c.lastRejoinError
	snapshot.ConsecutiveRejoinFailures = c.consecutiveRejoinFailures
	snapshot.ActiveServiceProxies = len(c.serviceProxies)
	snapshot.RecentEvents = slices.Clone(c.traceEvents)

	for name, publish := range c.cfg.Publish {
		snapshot.Publish = append(snapshot.Publish, PublishStatus{
			Name:     name,
			Protocol: publish.Protocol,
			Local:    publish.Local,
		})
	}
	for name, bind := range c.binds {
		bind.mu.Lock()
		activeSessions := bind.activeSessionCountLocked()
		listenAddr := bind.listenAddrLocked()
		for _, stream := range bind.tcpStreams {
			state, startedAt, lastSeen, bufferedInbound, unackedOutbound := stream.snapshotStatus()
			snapshot.TCPBindStreams = append(snapshot.TCPBindStreams, TCPBindStreamStatus{
				BindName:        name,
				PeerID:          stream.peerID,
				PeerName:        firstNonEmpty(stream.peerName, c.peerDisplayNameByIDLocked(stream.peerID)),
				Service:         stream.service,
				SessionID:       stream.sessionID,
				State:           state,
				StartedAt:       startedAt,
				LastSeen:        lastSeen,
				BufferedInbound: bufferedInbound,
				UnackedOutbound: unackedOutbound,
			})
		}
		bind.mu.Unlock()

		snapshot.Binds = append(snapshot.Binds, BindStatus{
			Name:           name,
			Protocol:       bind.cfg.Protocol,
			ListenAddr:     listenAddr,
			Peer:           bind.cfg.Peer,
			Service:        bind.cfg.Service,
			ActiveSessions: activeSessions,
		})
	}
	for _, proxy := range c.serviceProxies {
		if proxy.protocol != config.ServiceProtocolTCP {
			continue
		}
		state, startedAt, lastSeen, bufferedInbound, unackedOutbound := proxy.snapshotStatus()
		snapshot.TCPProxies = append(snapshot.TCPProxies, TCPProxyStatus{
			PeerID:          proxy.peerID,
			PeerName:        firstNonEmpty(proxy.peerName, c.peerDisplayNameByIDLocked(proxy.peerID)),
			BindName:        proxy.bindName,
			Service:         proxy.service,
			SessionID:       proxy.sessionID,
			State:           state,
			Target:          proxy.target,
			StartedAt:       startedAt,
			LastSeen:        lastSeen,
			BufferedInbound: bufferedInbound,
			UnackedOutbound: unackedOutbound,
		})
	}
	for _, peer := range c.peers {
		state := peerStateLabel(peer)
		status := PeerStatus{
			DeviceID:          peer.info.DeviceID,
			DeviceName:        peer.info.DeviceName,
			State:             state,
			ObservedAddr:      peer.info.ObservedAddr,
			ChosenAddr:        udpAddrString(peer.chosenAddr),
			Candidates:        udpAddrsToStrings(peer.candidates),
			Services:          serviceNames(peer.info.Services),
			ServiceDetails:    slices.Clone(peer.info.Services),
			LastSeen:          peer.lastSeen,
			PunchAttempts:     peer.punchAttempts,
			LastError:         peer.lastError,
			RouteReason:       peer.routeReason,
			RouteChangedAt:    peer.routeChangedAt,
			LastOfflineReason: peer.lastOfflineReason,
		}
		status.CandidateStats = snapshotCandidateStats(peer.candidateStats, peer.chosenAddr)
		if peer.session != nil {
			status.SessionEstablishedAt = peer.session.establishedAt
			status.SessionLastSeen = peer.session.lastSeen
			status.SentPackets = peer.session.sentPackets.Load()
			status.RecvPackets = peer.session.recvPackets.Load()
			status.SentBytes = peer.session.sentBytes.Load()
			status.RecvBytes = peer.session.recvBytes.Load()
		}
		snapshot.Peers = append(snapshot.Peers, status)
	}

	slices.SortFunc(snapshot.Publish, func(a, b PublishStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(snapshot.Binds, func(a, b BindStatus) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(snapshot.TCPBindStreams, func(a, b TCPBindStreamStatus) int {
		return strings.Compare(a.BindName+a.SessionID, b.BindName+b.SessionID)
	})
	slices.SortFunc(snapshot.TCPProxies, func(a, b TCPProxyStatus) int {
		return strings.Compare(a.PeerName+a.BindName+a.SessionID, b.PeerName+b.BindName+b.SessionID)
	})
	slices.SortFunc(snapshot.Peers, func(a, b PeerStatus) int {
		return strings.Compare(a.DeviceName+a.DeviceID, b.DeviceName+b.DeviceID)
	})
	return snapshot
}

func adminURLFromListen(addr, path string) (string, error) {
	if err := config.ValidateAdminListen(addr); err != nil {
		return "", err
	}
	u := url.URL{
		Scheme: "http",
		Host:   addr,
		Path:   path,
	}
	return u.String(), nil
}

func statusURLFromListen(addr string) (string, error) {
	return adminURLFromListen(addr, "/status")
}

func peerStateLabel(peer *peerState) string {
	switch {
	case peer == nil:
		return "unknown"
	case peer.session != nil && peer.chosenAddr != nil:
		return "connected"
	case len(peer.candidates) > 0:
		return "punching"
	default:
		return "discovered"
	}
}

func udpAddrString(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func snapshotCandidateStats(in map[string]*candidateState, chosen *net.UDPAddr) []CandidateStatus {
	if len(in) == 0 {
		return nil
	}
	currentRoute := udpAddrString(chosen)
	out := make([]CandidateStatus, 0, len(in))
	for _, candidate := range in {
		if candidate == nil {
			continue
		}
		out = append(out, CandidateStatus{
			Addr:                  candidate.addr,
			CurrentRoute:          candidate.addr == currentRoute,
			Attempts:              candidate.attempts,
			FirstAttemptAt:        candidate.firstAttemptAt,
			LastAttemptAt:         candidate.lastAttemptAt,
			LastInboundAt:         candidate.lastInboundAt,
			LastSuccessAt:         candidate.lastSuccessAt,
			FirstSuccessLatencyMS: durationMillis(candidate.firstSuccessLatency),
			LastSuccessSource:     candidate.lastSuccessSource,
		})
	}
	slices.SortFunc(out, func(a, b CandidateStatus) int {
		return strings.Compare(a.Addr, b.Addr)
	})
	return out
}

func durationMillis(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	return value.Milliseconds()
}
