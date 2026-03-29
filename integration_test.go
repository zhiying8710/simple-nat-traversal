package simple_nat_traversal_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"simple-nat-traversal/internal/client"
	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/server"
)

func TestUDPPortMappingEndToEnd(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	httpAddr := reserveTCPAddr(t)
	udpAddr := reserveUDPAddr(t)
	adminA := reserveTCPAddr(t)
	adminB := reserveTCPAddr(t)
	echoAddr := reserveUDPAddr(t)
	bindAddr := reserveUDPAddr(t)

	srv, err := server.New(config.ServerConfig{
		HTTPListen:    httpAddr,
		UDPListen:     udpAddr,
		PublicUDPAddr: udpAddr,
		Password:      "smoke-network-password-1234",
		AdminPassword: "smoke-admin-password-1234",
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(ctx)
	}()
	waitForHTTPHealth(t, ctx, "http://"+httpAddr+"/healthz")

	echoConn, err := net.ListenUDP("udp", mustResolveUDPAddr(t, echoAddr))
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	defer echoConn.Close()
	go runEchoServer(echoConn)

	clientErrCh := make(chan error, 2)
	go func() {
		clientErrCh <- client.Run(ctx, config.ClientConfig{
			ServerURL:     "http://" + httpAddr,
			Password:      "smoke-network-password-1234",
			AdminPassword: "smoke-admin-password-1234",
			DeviceName:    "win-b",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminB,
			Publish: map[string]config.PublishConfig{
				"echo": {Local: echoAddr},
			},
			Binds: map[string]config.BindConfig{},
		})
	}()
	go func() {
		clientErrCh <- client.Run(ctx, config.ClientConfig{
			ServerURL:     "http://" + httpAddr,
			Password:      "smoke-network-password-1234",
			AdminPassword: "smoke-admin-password-1234",
			DeviceName:    "mac-a",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminA,
			Publish:       map[string]config.PublishConfig{},
			Binds: map[string]config.BindConfig{
				"echo-b": {
					Peer:    "win-b",
					Service: "echo",
					Local:   bindAddr,
				},
			},
		})
	}()

	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminA}, "win-b")

	appConn, err := net.ListenUDP("udp", mustResolveUDPAddr(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("listen app udp: %v", err)
	}
	defer appConn.Close()

	payload := []byte("ping-from-integration-test")
	if _, err := appConn.WriteToUDP(payload, mustResolveUDPAddr(t, bindAddr)); err != nil {
		t.Fatalf("send test packet: %v", err)
	}
	if err := appConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	buf := make([]byte, 4096)
	n, _, err := appConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read echoed packet: %v", err)
	}
	if got := string(buf[:n]); got != string(payload) {
		t.Fatalf("unexpected echo payload: got=%q want=%q", got, payload)
	}

	status, err := client.FetchStatus(ctx, config.ClientConfig{AdminListen: adminA})
	if err != nil {
		t.Fatalf("fetch final status: %v", err)
	}
	if len(status.Peers) == 0 || status.Peers[0].State != "connected" {
		t.Fatalf("unexpected peer status: %+v", status.Peers)
	}
	oldPeerID := status.Peers[0].DeviceID

	statusB, err := client.FetchStatus(ctx, config.ClientConfig{AdminListen: adminB})
	if err != nil {
		t.Fatalf("fetch final status for win-b: %v", err)
	}
	oldSelfID := statusB.DeviceID

	adminCfg := config.ClientConfig{
		ServerURL:     "http://" + httpAddr,
		Password:      "smoke-network-password-1234",
		AdminPassword: "smoke-admin-password-1234",
	}

	devices, err := client.FetchNetworkDevices(ctx, adminCfg)
	if err != nil {
		t.Fatalf("list network devices: %v", err)
	}
	if len(devices.Devices) != 2 {
		t.Fatalf("unexpected number of online devices: %+v", devices.Devices)
	}
	if devices.Devices[0].State != "online" || devices.Devices[1].State != "online" {
		t.Fatalf("expected online devices, got: %+v", devices.Devices)
	}

	kickResp, err := client.KickNetworkDevice(ctx, adminCfg, proto.KickDeviceRequest{
		DeviceName: "win-b",
	})
	if err != nil {
		t.Fatalf("kick network device: %v", err)
	}
	if !kickResp.Removed || kickResp.DeviceName != "win-b" {
		t.Fatalf("unexpected kick response: %+v", kickResp)
	}

	newSelfID := waitForDeviceIDChange(t, ctx, config.ClientConfig{AdminListen: adminB}, oldSelfID)
	waitForPeerConnectedWithID(t, ctx, config.ClientConfig{AdminListen: adminA}, "win-b", newSelfID)
	if newSelfID == oldPeerID {
		t.Fatalf("expected win-b to rejoin with a new device_id, got old=%s new=%s", oldPeerID, newSelfID)
	}

	devices, err = client.FetchNetworkDevices(ctx, adminCfg)
	if err != nil {
		t.Fatalf("list devices after kick: %v", err)
	}
	if len(devices.Devices) != 2 {
		t.Fatalf("unexpected device list after rejoin: %+v", devices.Devices)
	}
	if !deviceListContains(devices, "win-b", newSelfID) {
		t.Fatalf("expected rejoined win-b in device list, got: %+v", devices.Devices)
	}

	payload = []byte("ping-after-rejoin")
	if _, err := appConn.WriteToUDP(payload, mustResolveUDPAddr(t, bindAddr)); err != nil {
		t.Fatalf("send test packet after rejoin: %v", err)
	}
	if err := appConn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline after rejoin: %v", err)
	}
	n, _, err = appConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read echoed packet after rejoin: %v", err)
	}
	if got := string(buf[:n]); got != string(payload) {
		t.Fatalf("unexpected echo payload after rejoin: got=%q want=%q", got, payload)
	}

	cancel()
	assertContextExit(t, <-serverErrCh)
	assertContextExit(t, <-clientErrCh)
	assertContextExit(t, <-clientErrCh)
}

func TestGracefulLeaveAllowsImmediateReconnect(t *testing.T) {
	t.Parallel()

	rootCtx, rootCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer rootCancel()

	httpAddr := reserveTCPAddr(t)
	udpAddr := reserveUDPAddr(t)
	adminA := reserveTCPAddr(t)
	adminB1 := reserveTCPAddr(t)
	adminB2 := reserveTCPAddr(t)

	srv, err := server.New(config.ServerConfig{
		HTTPListen:    httpAddr,
		UDPListen:     udpAddr,
		PublicUDPAddr: udpAddr,
		Password:      "graceful-leave-password-1234",
		AdminPassword: "graceful-admin-password-1234",
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(rootCtx)
	}()
	waitForHTTPHealth(t, rootCtx, "http://"+httpAddr+"/healthz")

	clientErrCh := make(chan error, 3)
	go func() {
		clientErrCh <- client.Run(rootCtx, config.ClientConfig{
			ServerURL:     "http://" + httpAddr,
			Password:      "graceful-leave-password-1234",
			AdminPassword: "graceful-admin-password-1234",
			DeviceName:    "mac-a",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminA,
			Publish:       map[string]config.PublishConfig{},
			Binds:         map[string]config.BindConfig{},
		})
	}()

	peerCfg1 := config.ClientConfig{
		ServerURL:     "http://" + httpAddr,
		Password:      "graceful-leave-password-1234",
		AdminPassword: "graceful-admin-password-1234",
		DeviceName:    "win-b",
		UDPListen:     "127.0.0.1:0",
		AdminListen:   adminB1,
		Publish:       map[string]config.PublishConfig{},
		Binds:         map[string]config.BindConfig{},
	}
	if _, _, err := config.EnsureClientIdentity(&peerCfg1); err != nil {
		t.Fatalf("ensure peer identity: %v", err)
	}

	peerCtx1, peerCancel1 := context.WithCancel(rootCtx)
	go func() {
		clientErrCh <- client.Run(peerCtx1, peerCfg1)
	}()

	waitForPeerConnected(t, rootCtx, config.ClientConfig{AdminListen: adminA}, "win-b")
	firstPeerID := waitForPeerID(t, rootCtx, config.ClientConfig{AdminListen: adminA}, "win-b")

	peerCancel1()
	assertContextExit(t, <-clientErrCh)

	waitForPeerAbsent(t, rootCtx, config.ClientConfig{AdminListen: adminA}, "win-b")

	peerCfg2 := peerCfg1
	peerCfg2.AdminListen = adminB2
	peerCtx2, peerCancel2 := context.WithCancel(rootCtx)
	defer peerCancel2()
	go func() {
		clientErrCh <- client.Run(peerCtx2, peerCfg2)
	}()

	waitForPeerConnected(t, rootCtx, config.ClientConfig{AdminListen: adminA}, "win-b")
	secondPeerID := waitForPeerID(t, rootCtx, config.ClientConfig{AdminListen: adminA}, "win-b")
	if secondPeerID == firstPeerID {
		t.Fatalf("expected restarted peer to get a new device_id, got old=%s new=%s", firstPeerID, secondPeerID)
	}

	adminCfg := config.ClientConfig{
		ServerURL:     "http://" + httpAddr,
		Password:      "graceful-leave-password-1234",
		AdminPassword: "graceful-admin-password-1234",
	}
	devices, err := client.FetchNetworkDevices(rootCtx, adminCfg)
	if err != nil {
		t.Fatalf("list devices after reconnect: %v", err)
	}
	if !deviceListContains(devices, "win-b", secondPeerID) {
		t.Fatalf("expected restarted peer in device list, got: %+v", devices.Devices)
	}

	rootCancel()
	assertContextExit(t, <-serverErrCh)
	assertContextExit(t, <-clientErrCh)
	assertContextExit(t, <-clientErrCh)
}

func TestThreePeerMeshStatus(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	httpAddr := reserveTCPAddr(t)
	udpAddr := reserveUDPAddr(t)
	adminA := reserveTCPAddr(t)
	adminB := reserveTCPAddr(t)
	adminC := reserveTCPAddr(t)

	srv, err := server.New(config.ServerConfig{
		HTTPListen:    httpAddr,
		UDPListen:     udpAddr,
		PublicUDPAddr: udpAddr,
		Password:      "three-peer-password-1234",
		AdminPassword: "three-peer-admin-password-1234",
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Run(ctx)
	}()
	waitForHTTPHealth(t, ctx, "http://"+httpAddr+"/healthz")

	clientErrCh := make(chan error, 3)
	for _, cfg := range []config.ClientConfig{
		{
			ServerURL:     "http://" + httpAddr,
			Password:      "three-peer-password-1234",
			AdminPassword: "three-peer-admin-password-1234",
			DeviceName:    "mac-a",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminA,
		},
		{
			ServerURL:     "http://" + httpAddr,
			Password:      "three-peer-password-1234",
			AdminPassword: "three-peer-admin-password-1234",
			DeviceName:    "win-b",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminB,
		},
		{
			ServerURL:     "http://" + httpAddr,
			Password:      "three-peer-password-1234",
			AdminPassword: "three-peer-admin-password-1234",
			DeviceName:    "mini-c",
			UDPListen:     "127.0.0.1:0",
			AdminListen:   adminC,
		},
	} {
		cfg := cfg
		go func() {
			clientErrCh <- client.Run(ctx, cfg)
		}()
	}

	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminA}, "win-b")
	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminA}, "mini-c")
	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminB}, "mac-a")
	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminB}, "mini-c")
	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminC}, "mac-a")
	waitForPeerConnected(t, ctx, config.ClientConfig{AdminListen: adminC}, "win-b")

	adminCfg := config.ClientConfig{
		ServerURL:     "http://" + httpAddr,
		Password:      "three-peer-password-1234",
		AdminPassword: "three-peer-admin-password-1234",
	}
	devices, err := client.FetchNetworkDevices(ctx, adminCfg)
	if err != nil {
		t.Fatalf("fetch network devices: %v", err)
	}
	if len(devices.Devices) != 3 {
		t.Fatalf("expected 3 online devices, got: %+v", devices.Devices)
	}

	cancel()
	assertContextExit(t, <-serverErrCh)
	assertContextExit(t, <-clientErrCh)
	assertContextExit(t, <-clientErrCh)
	assertContextExit(t, <-clientErrCh)
}

func waitForHTTPHealth(t *testing.T, ctx context.Context, healthURL string) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			t.Fatalf("build health request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server health endpoint %s was not ready before timeout", healthURL)
}

func waitForPeerConnected(t *testing.T, ctx context.Context, cfg config.ClientConfig, peerName string) {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.FetchStatus(ctx, cfg)
		if err == nil {
			for _, peer := range snapshot.Peers {
				if peer.DeviceName == peerName && peer.State == "connected" {
					return
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("peer %s did not become connected before timeout", peerName)
}

func waitForPeerID(t *testing.T, ctx context.Context, cfg config.ClientConfig, peerName string) string {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.FetchStatus(ctx, cfg)
		if err == nil {
			for _, peer := range snapshot.Peers {
				if peer.DeviceName == peerName && peer.DeviceID != "" {
					return peer.DeviceID
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("peer %s did not expose a device_id before timeout", peerName)
	return ""
}

func waitForDeviceIDChange(t *testing.T, ctx context.Context, cfg config.ClientConfig, oldID string) string {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.FetchStatus(ctx, cfg)
		if err == nil {
			if snapshot.DeviceID != "" && snapshot.DeviceID != oldID {
				return snapshot.DeviceID
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("device_id did not change before timeout, old=%s", oldID)
	return ""
}

func waitForPeerAbsent(t *testing.T, ctx context.Context, cfg config.ClientConfig, peerName string) {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.FetchStatus(ctx, cfg)
		if err == nil {
			found := false
			for _, peer := range snapshot.Peers {
				if peer.DeviceName == peerName {
					found = true
					break
				}
			}
			if !found {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("peer %s was still present after timeout", peerName)
}

func waitForPeerConnectedWithID(t *testing.T, ctx context.Context, cfg config.ClientConfig, peerName, peerID string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := client.FetchStatus(ctx, cfg)
		if err == nil {
			for _, peer := range snapshot.Peers {
				if peer.DeviceName == peerName && peer.DeviceID == peerID && peer.State == "connected" {
					return
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("peer %s with device_id %s did not become connected before timeout", peerName, peerID)
}

func deviceListContains(snapshot proto.NetworkDevicesResponse, deviceName, deviceID string) bool {
	for _, device := range snapshot.Devices {
		if device.DeviceName == deviceName && device.DeviceID == deviceID {
			return true
		}
	}
	return false
}

func runEchoServer(conn *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = conn.WriteToUDP(buf[:n], addr)
	}
}

func reserveTCPAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp addr: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func reserveUDPAddr(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenUDP("udp", mustResolveUDPAddr(t, "127.0.0.1:0"))
	if err != nil {
		t.Fatalf("reserve udp addr: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}

func mustResolveUDPAddr(t *testing.T, raw string) *net.UDPAddr {
	t.Helper()

	addr, err := net.ResolveUDPAddr("udp", raw)
	if err != nil {
		t.Fatalf("resolve udp addr %q: %v", raw, err)
	}
	return addr
}

func assertContextExit(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	t.Fatalf("unexpected goroutine exit error: %v", err)
}
