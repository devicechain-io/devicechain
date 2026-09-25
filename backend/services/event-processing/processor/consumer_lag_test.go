// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
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

// consumerLagSkipLevels returns the level of every "Consumer-lag sample skipped" line
// in captured.
func consumerLagSkipLevels(captured string) []string {
	var levels []string
	for _, line := range strings.Split(captured, "\n") {
		var entry struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil &&
			strings.HasPrefix(entry.Message, "Consumer-lag sample skipped") {
			levels = append(levels, entry.Level)
		}
	}
	return levels
}

// A lag gauge that has silently stopped refreshing is broker trouble an operator should
// see, so a failed probe on a live context is a Warn; a probe that failed because the
// processor is stopping stays at Debug, so a shutdown does not warn. Each case asserts
// exactly one line at its level — a positive assertion, so a muted logger fails.
func TestSampleConsumerLagWarnsOnlyWhileLive(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.backlogProbe = &countingProber{err: errors.New("broker unreachable")}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"live", context.Background(), "warn"},
		{"cancelled", cancelled, "debug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := logSink.Capture(t)
			rp.sampleConsumerLag(tc.ctx)
			if got := consumerLagSkipLevels(logs.String()); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("want exactly one %s consumer-lag skip line, got %v\ncaptured:\n%s", tc.want, got, logs.String())
			}
		})
	}
}
