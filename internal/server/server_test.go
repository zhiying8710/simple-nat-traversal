package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

func TestJoinNetworkRequiresIdentityPublic(t *testing.T) {
	t.Parallel()

	srv, err := New(config.ServerConfig{Password: "server-password-1234"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, status, err := srv.joinNetwork(proto.JoinNetworkRequest{
		Password:   "server-password-1234",
		DeviceName: "mac-a",
	})
	if err == nil {
		t.Fatal("expected joinNetwork to fail")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d err=%v", status, err)
	}
}

func TestAuthorizeAdminUsesSeparateAdminPassword(t *testing.T) {
	t.Parallel()

	srv, err := New(config.ServerConfig{
		Password:      "network-password-1234",
		AdminPassword: "admin-password-5678",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	networkReq := httptest.NewRequest(http.MethodGet, "/v1/network/devices", nil)
	networkReq.Header.Set("X-SNT-Admin-Password", "network-password-1234")
	if srv.authorizeAdmin(networkReq) {
		t.Fatal("expected network password to be rejected for admin auth")
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/v1/network/devices", nil)
	adminReq.Header.Set("X-SNT-Admin-Password", "admin-password-5678")
	if !srv.authorizeAdmin(adminReq) {
		t.Fatal("expected admin password to be accepted")
	}
}

func TestJoinNetworkRejectsDifferentIdentityForKnownDeviceName(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "server.state.json")
	ownerPublic, _, err := secure.NewIdentityKey()
	if err != nil {
		t.Fatalf("NewIdentityKey owner: %v", err)
	}
	otherPublic, _, err := secure.NewIdentityKey()
	if err != nil {
		t.Fatalf("NewIdentityKey other: %v", err)
	}

	srv, err := New(config.ServerConfig{
		Password:  "server-password-1234",
		StatePath: statePath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	firstResp, status, err := srv.joinNetwork(proto.JoinNetworkRequest{
		Password:       "server-password-1234",
		DeviceName:     "win-b",
		IdentityPublic: ownerPublic,
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("first joinNetwork status=%d err=%v", status, err)
	}
	if _, status, err := srv.leaveNetwork(proto.LeaveNetworkRequest{
		DeviceID:     firstResp.DeviceID,
		SessionToken: firstResp.SessionToken,
	}); err != nil || status != http.StatusOK {
		t.Fatalf("leaveNetwork status=%d err=%v", status, err)
	}

	restarted, err := New(config.ServerConfig{
		Password:  "server-password-1234",
		StatePath: statePath,
	})
	if err != nil {
		t.Fatalf("New restarted: %v", err)
	}

	if _, status, err := restarted.joinNetwork(proto.JoinNetworkRequest{
		Password:       "server-password-1234",
		DeviceName:     "win-b",
		IdentityPublic: otherPublic,
	}); err == nil || status != http.StatusForbidden {
		t.Fatalf("expected identity mismatch to be rejected, status=%d err=%v", status, err)
	}
	if _, status, err := restarted.joinNetwork(proto.JoinNetworkRequest{
		Password:       "server-password-1234",
		DeviceName:     "win-b",
		IdentityPublic: ownerPublic,
	}); err != nil || status != http.StatusOK {
		t.Fatalf("expected original identity to reclaim name, status=%d err=%v", status, err)
	}
}

func TestPruneStaleDevicesRemovesExpiredPendingJoin(t *testing.T) {
	t.Parallel()

	identityPublic, _, err := secure.NewIdentityKey()
	if err != nil {
		t.Fatalf("NewIdentityKey: %v", err)
	}
	srv, err := New(config.ServerConfig{Password: "server-password-1234"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, status, err := srv.joinNetwork(proto.JoinNetworkRequest{
		Password:       "server-password-1234",
		DeviceName:     "mac-a",
		IdentityPublic: identityPublic,
	})
	if err != nil || status != http.StatusOK {
		t.Fatalf("joinNetwork status=%d err=%v", status, err)
	}

	srv.mu.Lock()
	device := srv.devices[resp.DeviceID]
	if device == nil {
		srv.mu.Unlock()
		t.Fatalf("expected pending device %s", resp.DeviceID)
	}
	device.JoinedAt = time.Now().Add(-pendingJoinTTL - time.Second)
	srv.mu.Unlock()

	srv.pruneStaleDevices()

	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.devices) != 0 {
		t.Fatalf("expected expired pending join to be pruned, got %d devices", len(srv.devices))
	}
	if len(srv.deviceOwners) != 1 {
		t.Fatalf("expected device ownership to remain registered, got %d", len(srv.deviceOwners))
	}
}

func TestHandleLogLevelUpdatesRuntimeLevel(t *testing.T) {
	previous := logx.CurrentLevel()
	defer func() {
		_, _ = logx.SetLevel(previous)
	}()
	_, _ = logx.SetLevel(config.LogLevelInfo)

	srv, err := New(config.ServerConfig{
		Password:      "network-password-1234",
		AdminPassword: "admin-password-5678",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body, err := json.Marshal(proto.LogLevelUpdateRequest{LogLevel: config.LogLevelDebug})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/log-level", bytes.NewReader(body))
	req.Header.Set("X-SNT-Admin-Password", "admin-password-5678")
	rec := httptest.NewRecorder()

	srv.handleLogLevel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp proto.LogLevelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if resp.LogLevel != config.LogLevelDebug {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if got := logx.CurrentLevel(); got != config.LogLevelDebug {
		t.Fatalf("unexpected runtime log level: %s", got)
	}
	if srv.cfg.LogLevel != config.LogLevelDebug {
		t.Fatalf("unexpected server config log level: %s", srv.cfg.LogLevel)
	}
}

func TestSnapshotDevicesIncludesProtocolInServiceNames(t *testing.T) {
	t.Parallel()

	srv, err := New(config.ServerConfig{Password: "server-password-1234"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv.devices["dev-win"] = &deviceState{
		ID:   "dev-win",
		Name: "win-b",
		Services: []proto.ServiceInfo{
			{Name: "rdp", Protocol: config.ServiceProtocolTCP},
			{Name: "dns"},
		},
	}

	snapshot := srv.snapshotDevices()
	if len(snapshot.Devices) != 1 {
		t.Fatalf("unexpected device count: %+v", snapshot.Devices)
	}
	if got := snapshot.Devices[0].Services; len(got) != 2 || got[0] != "dns/udp" || got[1] != "rdp/tcp" {
		t.Fatalf("unexpected service names: %+v", got)
	}
}
