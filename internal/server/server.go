package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

const (
	heartbeatSeconds = 15
	punchIntervalMS  = 700
	peerTTL          = 45 * time.Second
	pendingJoinTTL   = 30 * time.Second
	maxDatagramSize  = 64 * 1024
	httpReadTimeout  = 5 * time.Second
	httpWriteTimeout = 10 * time.Second
	httpIdleTimeout  = 30 * time.Second
	udpWorkerCount   = 4
	udpQueueSize     = 256
)

type Server struct {
	cfg config.ServerConfig

	mu           sync.RWMutex
	devices      map[string]*deviceState
	deviceOwners map[string][]byte

	udpConn *net.UDPConn
}

type deviceState struct {
	ID             string
	Name           string
	Token          string
	IdentityPublic []byte
	JoinedAt       time.Time
	LastSeen       time.Time

	ObservedAddr string
	Candidates   []string
	Services     []proto.ServiceInfo
}

type udpPacket struct {
	addr *net.UDPAddr
	data []byte
}

func New(cfg config.ServerConfig) (*Server, error) {
	if cfg.Password == "" {
		return nil, errors.New("server password is required")
	}
	srv := &Server{
		cfg:          cfg,
		devices:      map[string]*deviceState{},
		deviceOwners: map[string][]byte{},
	}
	if err := srv.loadState(); err != nil {
		return nil, err
	}
	return srv, nil
}

func (s *Server) Run(ctx context.Context) error {
	if _, err := logx.SetLevel(s.cfg.LogLevel); err != nil {
		return err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.UDPListen)
	if err != nil {
		return fmt.Errorf("resolve udp listen addr: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp: %w", err)
	}
	defer conn.Close()
	s.udpConn = conn

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/network/join", s.handleJoinNetwork)
	mux.HandleFunc("/v1/network/leave", s.handleLeaveNetwork)
	mux.HandleFunc("/v1/network/devices", s.handleListDevices)
	mux.HandleFunc("/v1/network/devices/kick", s.handleKickDevice)
	mux.HandleFunc("/v1/admin/log-level", s.handleLogLevel)

	httpServer := &http.Server{
		Addr:              s.cfg.HTTPListen,
		Handler:           mux,
		ReadHeaderTimeout: httpReadTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    8 << 10,
	}

	errCh := make(chan error, 2)
	go func() {
		if err := s.serveUDP(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
			errCh <- err
		}
	}()
	go func() {
		if err := s.janitor(ctx); err != nil {
			errCh <- err
		}
	}()
	go func() {
		var err error
		if s.cfg.TLSCertFile != "" {
			err = httpServer.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		} else {
			err = httpServer.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	logx.Infof("server listening: http=%s udp=%s public_udp=%s mode=single-network", s.cfg.HTTPListen, s.cfg.UDPListen, s.cfg.PublicUDPAddr)

	select {
	case err := <-errCh:
		_ = httpServer.Shutdown(context.Background())
		return err
	case <-ctx.Done():
		_ = conn.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return nil
	}
}

func (s *Server) serveUDP(ctx context.Context) error {
	packets := make(chan udpPacket, udpQueueSize)
	for range udpWorkerCount {
		go s.udpWorker(ctx, packets)
	}
	defer close(packets)

	lastDropLog := time.Time{}
	buf := make([]byte, maxDatagramSize)
	for {
		n, addr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return err
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		packet := udpPacket{
			addr: cloneUDPAddr(addr),
			data: data,
		}
		select {
		case packets <- packet:
		default:
			if time.Since(lastDropLog) >= 5*time.Second {
				lastDropLog = time.Now()
				logx.Warnf("udp receive queue full; dropping packet from %s", addr)
			}
		}
	}
}

func (s *Server) udpWorker(ctx context.Context, packets <-chan udpPacket) {
	for {
		select {
		case <-ctx.Done():
			return
		case packet, ok := <-packets:
			if !ok {
				return
			}
			s.handleDatagram(packet.addr, packet.data)
		}
	}
}

func (s *Server) janitor(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.pruneStaleDevices()
		}
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleJoinNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "read_failed", "read request body failed", err.Error())
		return
	}
	defer r.Body.Close()

	var req proto.JoinNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body", err.Error())
		return
	}
	if req.Password == "" || req.DeviceName == "" || len(req.IdentityPublic) == 0 {
		s.writeAPIError(w, http.StatusBadRequest, "missing_fields", "password, device_name and identity_public are required", "")
		return
	}

	resp, status, err := s.joinNetwork(req)
	if err != nil {
		s.writeAPIError(w, status, joinErrorCode(status), err.Error(), "")
		return
	}
	if err := s.writeJSON(w, http.StatusOK, resp); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
	}
}

func (s *Server) handleLeaveNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "read_failed", "read request body failed", err.Error())
		return
	}
	defer r.Body.Close()

	var req proto.LeaveNetworkRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body", err.Error())
		return
	}
	if req.DeviceID == "" || req.SessionToken == "" {
		s.writeAPIError(w, http.StatusBadRequest, "missing_fields", "device_id and session_token are required", "")
		return
	}

	resp, status, err := s.leaveNetwork(req)
	if err != nil {
		s.writeAPIError(w, status, leaveErrorCode(status), err.Error(), "")
		return
	}
	if err := s.writeJSON(w, http.StatusOK, resp); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
	}
}

func (s *Server) joinNetwork(req proto.JoinNetworkRequest) (proto.JoinNetworkResponse, int, error) {
	if !s.matchesServerPassword(req.Password) {
		return proto.JoinNetworkResponse{}, http.StatusUnauthorized, errors.New("invalid network password")
	}
	identityPublic, err := secure.ParseIdentityPublicKey(req.IdentityPublic)
	if err != nil {
		return proto.JoinNetworkResponse{}, http.StatusBadRequest, fmt.Errorf("invalid identity_public: %w", err)
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.canClaimDeviceNameLocked(req.DeviceName, identityPublic) {
		return proto.JoinNetworkResponse{}, http.StatusForbidden, fmt.Errorf("device_name %q is owned by a different identity", req.DeviceName)
	}
	for deviceID, device := range s.devices {
		if device.Name != req.DeviceName {
			continue
		}
		if isOnlineDevice(now, device) {
			return proto.JoinNetworkResponse{}, http.StatusConflict, fmt.Errorf("device_name %q is already online", req.DeviceName)
		}
		delete(s.devices, deviceID)
	}
	if err := s.claimDeviceNameLocked(req.DeviceName, identityPublic); err != nil {
		return proto.JoinNetworkResponse{}, http.StatusInternalServerError, fmt.Errorf("persist device identity: %w", err)
	}

	deviceID, err := secure.RandomID("dev")
	if err != nil {
		return proto.JoinNetworkResponse{}, http.StatusInternalServerError, fmt.Errorf("generate device id: %w", err)
	}
	token, err := secure.RandomToken()
	if err != nil {
		return proto.JoinNetworkResponse{}, http.StatusInternalServerError, fmt.Errorf("generate session token: %w", err)
	}
	s.devices[deviceID] = &deviceState{
		ID:             deviceID,
		Name:           req.DeviceName,
		Token:          token,
		IdentityPublic: slices.Clone(identityPublic),
		JoinedAt:       now,
	}

	return proto.JoinNetworkResponse{
		DeviceID:         deviceID,
		SessionToken:     token,
		UDPAddr:          s.cfg.PublicUDPAddr,
		HeartbeatSeconds: heartbeatSeconds,
		PunchIntervalMS:  punchIntervalMS,
	}, http.StatusOK, nil
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	if !s.adminEnabled() {
		s.writeAPIError(w, http.StatusForbidden, "admin_disabled", "server admin is disabled", "")
		return
	}
	if !s.authorizeAdmin(r) {
		s.writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", "")
		return
	}

	if err := s.writeJSON(w, http.StatusOK, s.snapshotDevices()); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
	}
}

func (s *Server) handleKickDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	if !s.adminEnabled() {
		s.writeAPIError(w, http.StatusForbidden, "admin_disabled", "server admin is disabled", "")
		return
	}
	if !s.authorizeAdmin(r) {
		s.writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", "")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "read_failed", "read request body failed", err.Error())
		return
	}
	defer r.Body.Close()

	var req proto.KickDeviceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body", err.Error())
		return
	}
	if req.DeviceID == "" && req.DeviceName == "" {
		s.writeAPIError(w, http.StatusBadRequest, "missing_fields", "device_id or device_name is required", "")
		return
	}

	resp, status, err := s.kickDevice(req)
	if err != nil {
		s.writeAPIError(w, status, kickErrorCode(status), err.Error(), "")
		return
	}
	if err := s.writeJSON(w, http.StatusOK, resp); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
	}
}

func (s *Server) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		s.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}
	if !s.authorizeLogLevel(r) {
		s.writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", "")
		return
	}

	if r.Method == http.MethodGet {
		if err := s.writeJSON(w, http.StatusOK, proto.LogLevelResponse{LogLevel: logx.CurrentLevel()}); err != nil {
			s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
		}
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "read_failed", "read request body failed", err.Error())
		return
	}
	defer r.Body.Close()

	var req proto.LogLevelUpdateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid json body", err.Error())
		return
	}
	level, err := logx.SetLevel(req.LogLevel)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, "invalid_log_level", err.Error(), "")
		return
	}

	s.mu.Lock()
	s.cfg.LogLevel = level
	s.mu.Unlock()

	if err := s.writeJSON(w, http.StatusOK, proto.LogLevelResponse{LogLevel: level}); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "encode_failed", "encode response failed", err.Error())
	}
}

func (s *Server) handleDatagram(addr *net.UDPAddr, raw []byte) {
	env, err := proto.UnmarshalEnvelope(raw)
	if err != nil {
		logx.Debugf("udp invalid envelope from %s: %v", addr, err)
		return
	}

	switch env.Type {
	case proto.TypeRegister:
		if env.Register == nil {
			s.sendError(addr, "missing register payload")
			return
		}
		s.handleRegister(addr, env.Register)
	default:
		s.sendError(addr, "unsupported server udp message")
	}
}

func (s *Server) handleRegister(addr *net.UDPAddr, msg *proto.RegisterMessage) {
	s.mu.Lock()
	device, ok := s.devices[msg.DeviceID]
	if !ok || device.Token != msg.Token || device.Name != msg.DeviceName {
		s.mu.Unlock()
		s.sendError(addr, "invalid device session")
		return
	}

	device.LastSeen = time.Now()
	device.ObservedAddr = addr.String()
	device.Candidates = normalizeCandidates(msg.Candidates)
	device.Services = slices.Clone(msg.Services)

	ack := proto.Envelope{
		Type: proto.TypeRegisterAck,
		RegisterAck: &proto.RegisterAckMessage{
			ObservedAddr: addr.String(),
			ServerTime:   time.Now().Unix(),
		},
	}
	targets := s.peerSyncTargetsLocked()
	s.mu.Unlock()

	s.sendEnvelope(addr, ack)
	s.broadcastPeerSync(targets)
}

func (s *Server) peerSyncTargetsLocked() []peerSyncTarget {
	peers := make([]proto.PeerInfo, 0, len(s.devices))
	now := time.Now()
	for _, device := range s.devices {
		if device.ObservedAddr == "" || now.Sub(device.LastSeen) > peerTTL {
			continue
		}
		candidates := normalizeCandidates(append([]string{device.ObservedAddr}, device.Candidates...))
		peers = append(peers, proto.PeerInfo{
			DeviceID:       device.ID,
			DeviceName:     device.Name,
			ObservedAddr:   device.ObservedAddr,
			Candidates:     candidates,
			Services:       slices.Clone(device.Services),
			IdentityPublic: slices.Clone(device.IdentityPublic),
		})
	}

	targets := make([]peerSyncTarget, 0, len(peers))
	for _, peer := range peers {
		targets = append(targets, peerSyncTarget{
			addr: peer.ObservedAddr,
			env: proto.Envelope{
				Type: proto.TypePeerSync,
				PeerSync: &proto.PeerSyncMessage{
					Peers: slices.Clone(peers),
				},
			},
		})
	}
	return targets
}

type peerSyncTarget struct {
	addr string
	env  proto.Envelope
}

func (s *Server) broadcastPeerSync(targets []peerSyncTarget) {
	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp", target.addr)
		if err != nil {
			logx.Warnf("resolve peer target %q failed: %v", target.addr, err)
			continue
		}
		s.sendEnvelope(addr, target.env)
	}
}

func (s *Server) sendError(addr *net.UDPAddr, message string) {
	s.sendEnvelope(addr, proto.Envelope{
		Type: proto.TypeError,
		Error: &proto.ErrorMessage{
			Message: message,
		},
	})
}

func (s *Server) sendEnvelope(addr *net.UDPAddr, env proto.Envelope) {
	raw, err := proto.MarshalEnvelope(env)
	if err != nil {
		logx.Errorf("marshal envelope failed: %v", err)
		return
	}
	if _, err := s.udpConn.WriteToUDP(raw, addr); err != nil {
		logx.Warnf("udp send to %s failed: %v", addr, err)
	}
}

func (s *Server) pruneStaleDevices() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	changed := false
	for deviceID, device := range s.devices {
		if !shouldPruneDevice(now, device) {
			continue
		}
		delete(s.devices, deviceID)
		changed = true
	}
	if !changed {
		return
	}

	targets := s.peerSyncTargetsLocked()
	go s.broadcastPeerSync(targets)
}

func (s *Server) kickDevice(req proto.KickDeviceRequest) (proto.KickDeviceResponse, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed *deviceState
	var removedID string
	for deviceID, device := range s.devices {
		if req.DeviceID != "" && deviceID != req.DeviceID {
			continue
		}
		if req.DeviceName != "" && device.Name != req.DeviceName {
			continue
		}
		removedID = deviceID
		copyDevice := *device
		removed = &copyDevice
		delete(s.devices, deviceID)
		break
	}
	if removed == nil {
		return proto.KickDeviceResponse{}, http.StatusNotFound, errors.New("device not found")
	}

	targets := s.peerSyncTargetsLocked()
	go s.broadcastPeerSync(targets)
	return proto.KickDeviceResponse{
		Removed:    true,
		DeviceID:   removedID,
		DeviceName: removed.Name,
	}, http.StatusOK, nil
}

func (s *Server) leaveNetwork(req proto.LeaveNetworkRequest) (proto.LeaveNetworkResponse, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	device, ok := s.devices[req.DeviceID]
	if !ok {
		return proto.LeaveNetworkResponse{}, http.StatusNotFound, errors.New("device not found")
	}
	if device.Token != req.SessionToken {
		return proto.LeaveNetworkResponse{}, http.StatusUnauthorized, errors.New("invalid device session")
	}

	delete(s.devices, req.DeviceID)
	targets := s.peerSyncTargetsLocked()
	go s.broadcastPeerSync(targets)

	return proto.LeaveNetworkResponse{
		Removed:    true,
		DeviceID:   req.DeviceID,
		DeviceName: device.Name,
	}, http.StatusOK, nil
}

func (s *Server) snapshotDevices() proto.NetworkDevicesResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	devices := make([]proto.NetworkDeviceStatus, 0, len(s.devices))
	for _, device := range s.devices {
		state := "pending_registration"
		lastSeen := device.JoinedAt
		if isOnlineDevice(now, device) {
			state = "online"
			lastSeen = device.LastSeen
		}
		devices = append(devices, proto.NetworkDeviceStatus{
			DeviceID:       device.ID,
			DeviceName:     device.Name,
			State:          state,
			ObservedAddr:   device.ObservedAddr,
			Candidates:     slices.Clone(device.Candidates),
			Services:       serviceNames(device.Services),
			ServiceDetails: slices.Clone(device.Services),
			LastSeen:       lastSeen,
		})
	}
	slices.SortFunc(devices, func(a, b proto.NetworkDeviceStatus) int {
		switch {
		case a.DeviceName < b.DeviceName:
			return -1
		case a.DeviceName > b.DeviceName:
			return 1
		default:
			return strings.Compare(a.DeviceID, b.DeviceID)
		}
	})
	return proto.NetworkDevicesResponse{
		GeneratedAt: time.Now(),
		Devices:     devices,
	}
}

func (s *Server) authorizeAdmin(r *http.Request) bool {
	password := strings.TrimSpace(r.Header.Get("X-SNT-Admin-Password"))
	if password == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			password = strings.TrimSpace(auth[len("Bearer "):])
		}
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.AdminPassword)) == 1
}

func (s *Server) adminEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return strings.TrimSpace(s.cfg.AdminPassword) != ""
}

func (s *Server) authorizeLogLevel(r *http.Request) bool {
	if s.adminEnabled() {
		return s.authorizeAdmin(r)
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) matchesServerPassword(password string) bool {
	return subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.Password)) == 1
}

func normalizeCandidates(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, candidate := range in {
		if candidate == "" {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", candidate)
		if err != nil {
			continue
		}
		normalized := addr.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	slices.Sort(out)
	return out
}

func serviceNames(in []proto.ServiceInfo) []string {
	out := make([]string, 0, len(in))
	for _, service := range in {
		protocol := strings.TrimSpace(service.Protocol)
		if protocol == "" {
			protocol = config.ServiceProtocolUDP
		}
		out = append(out, service.Name+"/"+protocol)
	}
	slices.Sort(out)
	return out
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}

func (s *Server) writeAPIError(w http.ResponseWriter, status int, code, message, detail string) {
	if code == "" {
		code = "request_failed"
	}
	if message == "" {
		message = http.StatusText(status)
	}
	_ = s.writeJSON(w, status, proto.APIErrorResponse{
		Code:    code,
		Message: message,
		Detail:  detail,
	})
}

func joinErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_network_password"
	case http.StatusForbidden:
		return "device_name_identity_mismatch"
	case http.StatusConflict:
		return "device_name_online"
	default:
		return "join_failed"
	}
}

func kickErrorCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return "device_not_found"
	default:
		return "kick_failed"
	}
}

func leaveErrorCode(status int) string {
	switch status {
	case http.StatusNotFound:
		return "device_not_found"
	case http.StatusUnauthorized:
		return "invalid_device_session"
	default:
		return "leave_failed"
	}
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

func isOnlineDevice(now time.Time, device *deviceState) bool {
	return device != nil && device.ObservedAddr != "" && now.Sub(device.LastSeen) <= peerTTL
}

func shouldPruneDevice(now time.Time, device *deviceState) bool {
	if device == nil {
		return false
	}
	if device.ObservedAddr == "" {
		return now.Sub(device.JoinedAt) > pendingJoinTTL
	}
	return now.Sub(device.LastSeen) > peerTTL
}
