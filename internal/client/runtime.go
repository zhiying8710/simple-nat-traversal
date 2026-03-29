package client

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"simple-nat-traversal/internal/config"
	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
)

func (c *Client) cleanupStaleSessions() {
	now := time.Now()

	for _, bind := range c.binds {
		var staleStreams []*tcpBindStream
		var staleStreamEvents []struct {
			peerID   string
			peerName string
			detail   string
		}
		bind.mu.Lock()
		for sessionID, session := range bind.sessions {
			if now.Sub(session.lastSeen) <= bindSessionTTL {
				continue
			}
			delete(bind.sessions, sessionID)
		}
		for sessionID, stream := range bind.tcpStreams {
			if now.Sub(stream.lastSeenTime()) <= tcpIdleTTL {
				continue
			}
			delete(bind.tcpStreams, sessionID)
			staleStreams = append(staleStreams, stream)
			staleStreamEvents = append(staleStreamEvents, struct {
				peerID   string
				peerName string
				detail   string
			}{
				peerID:   stream.peerID,
				peerName: stream.peerName,
				detail:   "bind=" + stream.bindName + " service=" + stream.service + " session=" + sessionID + " reason=idle_timeout",
			})
		}
		bind.mu.Unlock()
		for _, event := range staleStreamEvents {
			c.recordEvent("tcp", event.peerID, event.peerName, "tcp_bind_idle_timeout", event.detail)
		}
		for _, stream := range staleStreams {
			stream.close()
		}
	}

	var staleProxies []*serviceProxy
	var staleProxyEvents []struct {
		peerID   string
		peerName string
		detail   string
	}
	c.mu.Lock()
	for key, proxy := range c.serviceProxies {
		ttl := serviceProxyTTL
		if proxy.protocol == config.ServiceProtocolTCP {
			ttl = tcpIdleTTL
		}
		if now.Sub(proxy.lastSeenTime()) <= ttl {
			continue
		}
		delete(c.serviceProxies, key)
		staleProxies = append(staleProxies, proxy)
		if proxy.protocol == config.ServiceProtocolTCP {
			staleProxyEvents = append(staleProxyEvents, struct {
				peerID   string
				peerName string
				detail   string
			}{
				peerID:   proxy.peerID,
				peerName: proxy.peerName,
				detail:   "bind=" + proxy.bindName + " service=" + proxy.service + " session=" + proxy.sessionID + " target=" + proxy.target + " reason=idle_timeout",
			})
		}
	}
	c.mu.Unlock()
	for _, event := range staleProxyEvents {
		c.recordEvent("tcp", event.peerID, event.peerName, "tcp_publish_idle_timeout", event.detail)
	}
	for _, proxy := range staleProxies {
		proxy.close()
	}
}

func (c *Client) resetTransportState(reason string) {
	c.mu.Lock()
	binds := make([]*bindProxy, 0, len(c.binds))
	for _, bind := range c.binds {
		binds = append(binds, bind)
	}
	proxies := make([]*serviceProxy, 0, len(c.serviceProxies))
	for key, proxy := range c.serviceProxies {
		delete(c.serviceProxies, key)
		proxies = append(proxies, proxy)
	}
	c.observedAddr = ""
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
	c.mu.Unlock()

	closedStreamCount := 0
	for _, bind := range binds {
		var streams []*tcpBindStream
		bind.mu.Lock()
		bind.sessions = map[string]*bindSession{}
		for sessionID, stream := range bind.tcpStreams {
			delete(bind.tcpStreams, sessionID)
			streams = append(streams, stream)
		}
		bind.mu.Unlock()
		closedStreamCount += len(streams)
		for _, stream := range streams {
			stream.close()
		}
	}
	for _, proxy := range proxies {
		proxy.close()
	}
	if closedStreamCount > 0 || len(proxies) > 0 {
		c.recordEvent("tcp", "", "", "tcp_transport_reset", fmt.Sprintf("reason=%s streams=%d proxies=%d", reason, closedStreamCount, len(proxies)))
	}
}

func (c *Client) dropPeerLocked(deviceID string) ([]*serviceProxy, []*tcpBindStream) {
	proxies := make([]*serviceProxy, 0, 1)
	for key, proxy := range c.serviceProxies {
		if proxy.peerID != deviceID {
			continue
		}
		delete(c.serviceProxies, key)
		proxies = append(proxies, proxy)
	}
	streams := make([]*tcpBindStream, 0, 1)
	for _, bind := range c.binds {
		bind.mu.Lock()
		for sessionID, stream := range bind.tcpStreams {
			if stream.peerID != deviceID {
				continue
			}
			delete(bind.tcpStreams, sessionID)
			streams = append(streams, stream)
		}
		bind.mu.Unlock()
	}
	return proxies, streams
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
	logx.Warnf("bind %s drop packet: %v", bindName, err)
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
		out = append(out, displayService(service))
	}
	slices.Sort(out)
	return out
}

func displayService(service proto.ServiceInfo) string {
	protocol := strings.TrimSpace(service.Protocol)
	if protocol == "" {
		protocol = config.ServiceProtocolUDP
	}
	return service.Name + "/" + protocol
}

func (b *bindProxy) listenAddrLocked() string {
	switch {
	case b.udp != nil:
		return b.udp.LocalAddr().String()
	case b.tcp != nil:
		return b.tcp.Addr().String()
	default:
		return ""
	}
}

func (b *bindProxy) activeSessionCountLocked() int {
	return len(b.sessions) + len(b.tcpStreams)
}

func udpAddrsToStrings(in []*net.UDPAddr) []string {
	out := make([]string, 0, len(in))
	for _, addr := range in {
		out = append(out, addr.String())
	}
	slices.Sort(out)
	return out
}
