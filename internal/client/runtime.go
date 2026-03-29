package client

import (
	"log"
	"net"
	"slices"
	"time"

	"simple-nat-traversal/internal/proto"
)

func (c *Client) cleanupStaleSessions() {
	now := time.Now()

	for _, bind := range c.binds {
		bind.mu.Lock()
		for sessionID, session := range bind.sessions {
			if now.Sub(session.lastSeen) <= bindSessionTTL {
				continue
			}
			delete(bind.sessions, sessionID)
		}
		bind.mu.Unlock()
	}

	c.mu.Lock()
	for key, proxy := range c.serviceProxies {
		if now.Sub(proxy.lastSeenTime()) <= serviceProxyTTL {
			continue
		}
		delete(c.serviceProxies, key)
		_ = proxy.conn.Close()
	}
	c.mu.Unlock()
}

func (c *Client) resetTransportState(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.observedAddr = ""
	for _, bind := range c.binds {
		bind.mu.Lock()
		bind.sessions = map[string]*bindSession{}
		bind.mu.Unlock()
	}
	for key, proxy := range c.serviceProxies {
		delete(c.serviceProxies, key)
		_ = proxy.conn.Close()
	}
	for _, peer := range c.peers {
		peer.handshake = nil
		peer.session = nil
		peer.chosenAddr = nil
		peer.lastError = ""
		peer.routeReason = ""
		peer.routeChangedAt = time.Time{}
		peer.punchingLogged = false
		peer.establishedLogged = false
		peer.lastOfflineReason = reason
	}
}

func (c *Client) dropPeerLocked(deviceID string) {
	for key, proxy := range c.serviceProxies {
		if proxy.peerID != deviceID {
			continue
		}
		delete(c.serviceProxies, key)
		_ = proxy.conn.Close()
	}
}

func (c *Client) recordPeerError(peerID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	peer := c.peers[peerID]
	if peer == nil {
		return
	}
	peer.lastError = err.Error()
	c.recordEventLocked("peer", peerID, peer.info.DeviceName, "send_error", err.Error())
}

func (c *Client) clearPeerError(peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	peer := c.peers[peerID]
	if peer == nil {
		return
	}
	peer.lastError = ""
}

func (b *bindProxy) logDrop(bindName string, err error) {
	now := time.Now()
	if now.Sub(b.lastDropLog) < 5*time.Second {
		return
	}
	b.lastDropLog = now
	log.Printf("bind %s drop packet: %v", bindName, err)
}

func (p *serviceProxy) touch() {
	p.lastSeen.Store(time.Now().UnixNano())
}

func (p *serviceProxy) lastSeenTime() time.Time {
	ns := p.lastSeen.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func serviceNames(in []proto.ServiceInfo) []string {
	out := make([]string, 0, len(in))
	for _, service := range in {
		out = append(out, service.Name)
	}
	slices.Sort(out)
	return out
}

func udpAddrsToStrings(in []*net.UDPAddr) []string {
	out := make([]string, 0, len(in))
	for _, addr := range in {
		out = append(out, addr.String())
	}
	slices.Sort(out)
	return out
}
