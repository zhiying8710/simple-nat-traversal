package client

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

func (c *Client) recordEvent(scope, peerID, peerName, event, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordEventLocked(scope, peerID, peerName, event, detail)
}

func (c *Client) recordEventLocked(scope, peerID, peerName, event, detail string) {
	c.traceEvents = append(c.traceEvents, TraceEvent{
		At:       time.Now(),
		Scope:    scope,
		PeerID:   peerID,
		PeerName: peerName,
		Event:    event,
		Detail:   detail,
	})
	if len(c.traceEvents) <= traceEventLimit {
		return
	}
	c.traceEvents = slices.Clone(c.traceEvents[len(c.traceEvents)-traceEventLimit:])
}

func (c *Client) markInitialJoinSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.networkState = "joined"
}

func (c *Client) markRegisterResult(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err == nil {
		c.lastRegisterAt = time.Now()
		c.lastRegisterError = ""
		return
	}
	c.lastRegisterError = err.Error()
	c.recordEventLocked("client", "", "", "register_failed", err.Error())
}

func (c *Client) markRejoinRequested(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.networkState != "rejoining" {
		c.networkState = "rejoin_pending"
	}
	c.lastRejoinReason = reason
}

func (c *Client) markRejoinAttempt(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.networkState = "rejoining"
	c.lastRejoinReason = reason
	c.lastRejoinAttemptAt = time.Now()
}

func (c *Client) markRejoinFailure(reason string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.networkState = "rejoining"
	c.lastRejoinReason = reason
	c.lastRejoinError = err.Error()
	c.consecutiveRejoinFailures++
}

func (c *Client) markRejoinSuccess(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.networkState = "joined"
	c.rejoinCount++
	c.lastRejoinReason = reason
	c.lastRejoinAt = time.Now()
	c.lastRejoinError = ""
	c.consecutiveRejoinFailures = 0
}

func ensureCandidateStatsLocked(peer *peerState) map[string]*candidateState {
	if peer.candidateStats == nil {
		peer.candidateStats = map[string]*candidateState{}
	}
	return peer.candidateStats
}

func syncCandidateStatsLocked(peer *peerState, candidates []*net.UDPAddr) {
	stats := ensureCandidateStatsLocked(peer)
	keep := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		addr := candidate.String()
		keep[addr] = struct{}{}
		if _, ok := stats[addr]; ok {
			continue
		}
		stats[addr] = &candidateState{addr: addr}
	}
	for addr := range stats {
		if _, ok := keep[addr]; ok {
			continue
		}
		delete(stats, addr)
	}
}

func candidateStateForAddrLocked(peer *peerState, addr *net.UDPAddr) *candidateState {
	if addr == nil {
		return nil
	}
	stats := ensureCandidateStatsLocked(peer)
	key := addr.String()
	state := stats[key]
	if state == nil {
		state = &candidateState{addr: key}
		stats[key] = state
	}
	return state
}

func noteCandidateAttemptLocked(peer *peerState, addr *net.UDPAddr) {
	state := candidateStateForAddrLocked(peer, addr)
	if state == nil {
		return
	}
	if state.firstAttemptAt.IsZero() {
		state.firstAttemptAt = time.Now()
	}
	state.attempts++
	state.lastAttemptAt = time.Now()
}

func noteCandidateInboundLocked(peer *peerState, addr *net.UDPAddr) {
	state := candidateStateForAddrLocked(peer, addr)
	if state == nil {
		return
	}
	state.lastInboundAt = time.Now()
}

func noteCandidateSuccessLocked(peer *peerState, addr *net.UDPAddr, source string) {
	state := candidateStateForAddrLocked(peer, addr)
	if state == nil {
		return
	}
	now := time.Now()
	state.lastInboundAt = now
	state.lastSuccessAt = now
	state.lastSuccessSource = source
	if state.firstAttemptAt.IsZero() {
		state.firstAttemptAt = now
	}
	if state.firstSuccessLatency == 0 {
		state.firstSuccessLatency = now.Sub(state.firstAttemptAt)
	}
}

func setPeerRouteLocked(c *Client, peer *peerState, addr *net.UDPAddr, reason string) {
	if peer == nil || addr == nil {
		return
	}
	if peer.chosenAddr != nil && peer.chosenAddr.String() == addr.String() && peer.routeReason == reason {
		return
	}
	peer.chosenAddr = cloneUDPAddr(addr)
	peer.routeReason = reason
	peer.routeChangedAt = time.Now()
	noteCandidateSuccessLocked(peer, addr, reason)
	c.recordEventLocked("peer", peer.info.DeviceID, peer.info.DeviceName, "route_selected", fmt.Sprintf("reason=%s addr=%s", reason, addr.String()))
}

func peerDisplayName(peer *peerState) string {
	if peer == nil {
		return ""
	}
	return firstNonEmpty(peer.info.DeviceName, peer.info.DeviceID)
}

func (c *Client) peerDisplayNameByID(peerID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.peerDisplayNameByIDLocked(peerID)
}

func (c *Client) peerDisplayNameByIDLocked(peerID string) string {
	if c == nil {
		return ""
	}
	peer := c.peers[peerID]
	if peer == nil {
		return peerID
	}
	return peerDisplayName(peer)
}

func candidateListString(addrs []*net.UDPAddr) string {
	if len(addrs) == 0 {
		return "-"
	}
	values := udpAddrsToStrings(addrs)
	return strings.Join(values, ",")
}
