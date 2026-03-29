package client

import "testing"

func TestProcessTCPInboundRejectsZeroSeqWithoutAckUnderflow(t *testing.T) {
	t.Parallel()

	var nextSeq uint64
	pending := map[uint64][]byte{}

	ready, ack := processTCPInbound(&nextSeq, pending, 0, []byte("bad"))

	if len(ready) != 0 {
		t.Fatalf("expected no ready payloads, got %d", len(ready))
	}
	if ack != 0 {
		t.Fatalf("expected ack 0 for invalid seq 0, got %d", ack)
	}
	if nextSeq != 0 {
		t.Fatalf("expected nextSeq to remain unset, got %d", nextSeq)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending payloads, got %d", len(pending))
	}
}

func TestProcessTCPInboundRejectsFramesOutsideReceiveWindow(t *testing.T) {
	t.Parallel()

	nextSeq := uint64(5)
	pending := map[uint64][]byte{}
	farFutureSeq := nextSeq + uint64(tcpSendWindow)

	ready, ack := processTCPInbound(&nextSeq, pending, farFutureSeq, []byte("far"))

	if len(ready) != 0 {
		t.Fatalf("expected no ready payloads, got %d", len(ready))
	}
	if ack != 4 {
		t.Fatalf("expected ack 4, got %d", ack)
	}
	if nextSeq != 5 {
		t.Fatalf("expected nextSeq to remain 5, got %d", nextSeq)
	}
	if len(pending) != 0 {
		t.Fatalf("expected far-future payload to be dropped, got pending=%d", len(pending))
	}
}

func TestProcessTCPInboundBuffersWithinReceiveWindow(t *testing.T) {
	t.Parallel()

	nextSeq := uint64(1)
	pending := map[uint64][]byte{}
	withinWindowSeq := nextSeq + uint64(tcpSendWindow) - 1

	ready, ack := processTCPInbound(&nextSeq, pending, withinWindowSeq, []byte("tail"))

	if len(ready) != 0 {
		t.Fatalf("expected no ready payloads before seq 1 arrives, got %d", len(ready))
	}
	if ack != 0 {
		t.Fatalf("expected ack 0 before any contiguous payload, got %d", ack)
	}
	if nextSeq != 1 {
		t.Fatalf("expected nextSeq to remain 1, got %d", nextSeq)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one buffered payload, got %d", len(pending))
	}
	if got := string(pending[withinWindowSeq]); got != "tail" {
		t.Fatalf("unexpected buffered payload: %q", got)
	}
}

