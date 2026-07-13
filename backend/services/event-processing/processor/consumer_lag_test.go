// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
)

// countingProber is a Backlog probe that records how many times it was called, so the consumer-lag
// sampler's short-circuit branches (nil probe, probe error, success) are observable even though the
// Prometheus gauges are nil in unit tests (no Microservice) and cannot be asserted directly.
type countingProber struct {
	pending    uint64
	ackPending uint64
	err        error
	calls      int
}

func (p *countingProber) Backlog(context.Context) (uint64, uint64, error) {
	p.calls++
	return p.pending, p.ackPending, p.err
}

// TestSampleConsumerLag proves the slice-8 consumer-lag sampler: it probes the broker when a
// backlog probe is wired (and does not panic recording into nil test metrics), skips entirely when
// no probe is wired (the scaffold/test path), and tolerates a probe error without panicking.
func TestSampleConsumerLag(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)

	// Wired probe, healthy: the sampler reads the backlog once.
	pr := &countingProber{pending: 7, ackPending: 2}
	rp.backlogProbe = pr
	rp.sampleConsumerLag(context.Background())
	if pr.calls != 1 {
		t.Fatalf("healthy sampler must probe the backlog exactly once; got %d calls", pr.calls)
	}

	// Probe error: still probes, must not panic, records nothing.
	perr := &countingProber{err: errors.New("broker unreachable")}
	rp.backlogProbe = perr
	rp.sampleConsumerLag(context.Background())
	if perr.calls != 1 {
		t.Fatalf("error sampler must still attempt one probe; got %d calls", perr.calls)
	}

	// No probe wired (scaffold/test): skipped, no panic.
	rp.backlogProbe = nil
	rp.sampleConsumerLag(context.Background())
}
