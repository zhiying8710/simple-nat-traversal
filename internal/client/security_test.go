package client

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/proto"
	"simple-nat-traversal/internal/secure"
)

func TestJoinNetworkUsesPerAttemptTimeout(t *testing.T) {
	oldTimeout := joinRequestTimeout
	oldClient := joinHTTPClient
	joinRequestTimeout = 50 * time.Millisecond
	defer func() {
		joinRequestTimeout = oldTimeout
		joinHTTPClient = oldClient
	}()

	joinHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/v1/network/join" {
				t.Fatalf("unexpected path: %s", req.URL.Path)
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}

	client := &Client{
		cfg: config.ClientConfig{
			ServerURL:  "http://snt.invalid",
			Password:   "timeout-password-1234",
			DeviceName: "mac-a",
		},
	}

	start := time.Now()
	err := client.joinNetwork(context.Background())
	if err == nil {
		t.Fatal("expected joinNetwork to fail")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected deadline exceeded error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("joinNetwork exceeded expected timeout window: %v", elapsed)
	}
}

func TestHandlePunchHelloRejectsInvalidIdentitySignature(t *testing.T) {
	t.Parallel()

	expectedPublic, _, err := secure.NewIdentityKey()
	if err != nil {
		t.Fatalf("NewIdentityKey: %v", err)
	}
	_, attackerPrivate, err := secure.NewIdentityKey()
	if err != nil {
		t.Fatalf("NewIdentityKey: %v", err)
	}
	_, handshakePublic, handshakeNonce, err := secure.NewEphemeralKey()
	if err != nil {
		t.Fatalf("NewEphemeralKey: %v", err)
	}
	signature, err := secure.SignPunchHello(attackerPrivate, "peer-1", handshakeNonce, handshakePublic)
	if err != nil {
		t.Fatalf("SignPunchHello: %v", err)
	}

	networkKey := make([]byte, 32)
	client := &Client{
		cfg:        config.ClientConfig{DeviceName: "local"},
		deviceID:   "local-1",
		networkKey: networkKey,
		peers: map[string]*peerState{
			"peer-1": {
				info: proto.PeerInfo{
					DeviceID:       "peer-1",
					DeviceName:     "peer",
					IdentityPublic: expectedPublic,
				},
			},
		},
	}

	client.handlePunchHello(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 32001}, &proto.PunchHelloMessage{
		FromID:    "peer-1",
		FromName:  "peer",
		Nonce:     handshakeNonce,
		Public:    handshakePublic,
		MAC:       secure.ComputePunchMAC(networkKey, "peer-1", handshakeNonce, handshakePublic),
		Signature: signature,
	})

	peer := client.peers["peer-1"]
	if peer.session != nil {
		t.Fatal("expected invalid identity signature to prevent session establishment")
	}
	if peer.chosenAddr != nil {
		t.Fatal("expected invalid identity signature to prevent route selection")
	}
}

func TestEnsureIdentityKeyReusesPersistedIdentity(t *testing.T) {
	t.Parallel()

	cfg := config.ClientDefaults()
	cfg.ServerURL = "http://127.0.0.1:8080"
	cfg.Password = "network-password-1234"
	cfg.DeviceName = "mac-a"

	if _, changed, err := config.EnsureClientIdentity(&cfg); err != nil {
		t.Fatalf("EnsureClientIdentity: %v", err)
	} else if !changed {
		t.Fatal("expected identity to be generated")
	}

	first := &Client{cfg: cfg}
	if err := first.ensureIdentityKey(); err != nil {
		t.Fatalf("ensureIdentityKey first: %v", err)
	}
	second := &Client{cfg: cfg}
	if err := second.ensureIdentityKey(); err != nil {
		t.Fatalf("ensureIdentityKey second: %v", err)
	}

	if string(first.identityPublic) != string(second.identityPublic) {
		t.Fatal("expected persisted config identity to be reused across client instances")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
