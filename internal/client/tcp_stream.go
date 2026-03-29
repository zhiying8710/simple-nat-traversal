package client

import (
	"errors"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"simple-nat-traversal/internal/logx"
	"simple-nat-traversal/internal/proto"
)

type tcpFrameEvent struct {
	seq     uint64
	payload []byte
	close   bool
	errText string
}

type tcpPendingChunk struct {
	payload []byte
	sentAt  time.Time
}

type tcpReliableSender struct {
	basePayload proto.ServicePayload
	sendPayload func(proto.ServicePayload) error
	touch       func()

	done chan struct{}

	mu      sync.Mutex
	cond    *sync.Cond
	nextSeq uint64
	pending map[uint64]*tcpPendingChunk
	closed  bool
}

func newTCPReliableSender(base proto.ServicePayload, sendPayload func(proto.ServicePayload) error, touch func()) *tcpReliableSender {
	sender := &tcpReliableSender{
		basePayload: base,
		sendPayload: sendPayload,
		touch:       touch,
		done:        make(chan struct{}),
		pending:     map[uint64]*tcpPendingChunk{},
	}
	sender.cond = sync.NewCond(&sender.mu)
	return sender
}

func (s *tcpReliableSender) run() {
	ticker := time.NewTicker(tcpResendAfter)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.resendPending()
		}
	}
}

func (s *tcpReliableSender) sendChunk(chunk []byte) error {
	chunk = slices.Clone(chunk)

	s.mu.Lock()
	for !s.closed && len(s.pending) >= tcpSendWindow {
		s.cond.Wait()
	}
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	s.nextSeq++
	seq := s.nextSeq
	s.pending[seq] = &tcpPendingChunk{payload: chunk}
	s.mu.Unlock()

	if err := s.sendFrame(seq, chunk); err != nil {
		s.mu.Lock()
		delete(s.pending, seq)
		s.cond.Broadcast()
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *tcpReliableSender) ack(ack uint64) {
	if ack == 0 {
		return
	}

	s.mu.Lock()
	changed := false
	for seq := range s.pending {
		if seq <= ack {
			delete(s.pending, seq)
			changed = true
		}
	}
	if changed {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
	if changed && s.touch != nil {
		s.touch()
	}
}

func (s *tcpReliableSender) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *tcpReliableSender) resendPending() {
	now := time.Now()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	type frame struct {
		seq     uint64
		payload []byte
	}
	frames := make([]frame, 0, len(s.pending))
	for seq, pending := range s.pending {
		if now.Sub(pending.sentAt) < tcpResendAfter {
			continue
		}
		frames = append(frames, frame{
			seq:     seq,
			payload: slices.Clone(pending.payload),
		})
	}
	s.mu.Unlock()

	for _, frame := range frames {
		if err := s.sendFrame(frame.seq, frame.payload); err != nil && !errors.Is(err, net.ErrClosed) {
			logx.Warnf("tcp retransmit %s/%s seq=%d failed: %v", s.basePayload.BindName, s.basePayload.SessionID, frame.seq, err)
		}
	}
}

func (s *tcpReliableSender) sendFrame(seq uint64, chunk []byte) error {
	payload := s.basePayload
	payload.Kind = proto.DataKindTCPData
	payload.StreamSeq = seq
	payload.Payload = slices.Clone(chunk)
	if err := s.sendPayload(payload); err != nil {
		return err
	}

	s.mu.Lock()
	if pending := s.pending[seq]; pending != nil {
		pending.sentAt = time.Now()
	}
	s.mu.Unlock()
	if s.touch != nil {
		s.touch()
	}
	return nil
}

func processTCPInbound(nextSeq *uint64, pending map[uint64][]byte, seq uint64, payload []byte) ([][]byte, uint64) {
	if seq == 0 {
		return nil, *nextSeq - 1
	}
	if *nextSeq == 0 {
		*nextSeq = 1
	}
	if seq < *nextSeq {
		return nil, *nextSeq - 1
	}
	if _, ok := pending[seq]; !ok {
		pending[seq] = slices.Clone(payload)
	}
	ready := make([][]byte, 0, 1)
	for {
		chunk, ok := pending[*nextSeq]
		if !ok {
			break
		}
		delete(pending, *nextSeq)
		ready = append(ready, chunk)
		*nextSeq++
	}
	return ready, *nextSeq - 1
}

func writeAll(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func tcpReadChunks(conn net.Conn, handle func([]byte) error) error {
	buf := make([]byte, tcpChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if sendErr := handle(buf[:n]); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
