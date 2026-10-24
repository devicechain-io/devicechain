// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// termGauges lists every gauge that is sampled by the leader's detection loop and means
// something only while a term is live, by the name it is exported under.
func termGauges(m *DetectMetrics) map[string]prometheus.Gauge {
	return map[string]prometheus.Gauge{
		"detect_consumer_pending":                    m.consumerPending,
		"detect_consumer_ack_pending":                m.consumerAckPending,
		"detect_watermark_lag_seconds":               m.watermarkLagSeconds,
		"detect_checkpoint_seconds":                  m.checkpointSeconds,
		"detect_snapshot_bytes":                      m.snapshotBytes,
		"detect_restore_seconds":                     m.restoreSeconds,
		"detect_applied_stream_seq":                  m.appliedStreamSeq,
		"detect_live_keys":                           m.liveKeys,
		"detect_retained_samples":                    m.retainedSamples,
		"detect_pending_timers":                      m.pendingTimers,
		"detect_tenants_over_rule_budget":            m.tenantsOverRuleBudget,
		"detect_tenants_over_live_key_budget":        m.tenantsOverLiveKeyBudget,
		"detect_tenants_over_retained_sample_budget": m.tenantsOverRetainedSampleBudget,
	}
}

// TestAReplicaThatLosesItsTermStopsReportingTheTermsGauges pins what a former leader
// exports once it is a standby.
//
// Prometheus keeps scraping a replica that lost the partition, and these gauges are only
// ever written by the leader's loop, so without a reset the standby keeps serving the last
// value it saw. The board and the DetectConsumerBacklogHigh / DetectWatermarkLagHigh alerts
// aggregate them with a bare max() across replicas, so a former leader's frozen backlog
// would out-vote the live leader's real one: a page that never clears, or a board that
// shows a backlog nobody has. The term ending is the moment the reading stops describing
// anything, so the gauges are cleared there, at the source, rather than every query having
// to remember to filter on detect_live.
//
// It runs a real term on a real lease and ends it the way a lost lease does (the term
// context is cancelled, the supervisor tears the term down and releases the partition).
func TestAReplicaThatLosesItsTermStopsReportingTheTermsGauges(t *testing.T) {
	b := startOutageBroker(t)
	rp := b.liveTerm(t)

	// What a leader that has been working for a while reports: a large backlog, lag, state.
	rp.metrics.recordConsumerLag(120000, 800)
	rp.metrics.recordRestore(4.5, 77)
	rp.metrics.recordCheckpoint(900, 1.5, time.Second, time.Second, 4096, 321)
	rp.metrics.recordStateBudget(stateBudgetStats{
		totalLiveKeys: 5000, totalRetainedSamples: 9000, totalPendingTimers: 40,
		tenantsOverRules: 1, tenantsOverKeys: 1, tenantsOverSamples: 1,
	})
	gauges := termGauges(rp.metrics)
	for name, g := range gauges {
		// The control: each one must be non-zero before the term ends, or the assertion
		// after it would pass for a gauge that was never fed.
		require.NotZero(t, testutil.ToFloat64(g), "%s was not fed before the term ended; the test would prove nothing", name)
	}

	rp.pcancel() // the term ends; the supervisor goroutine runs endTerm
	require.Eventually(t, func() bool { return testutil.ToFloat64(rp.metrics.isLeader) == 0 },
		30*time.Second, 50*time.Millisecond, "the term never finished ending")

	for name, g := range gauges {
		require.Zero(t, testutil.ToFloat64(g),
			"%s still reports the last leader's value after this replica lost its term", name)
	}
}
