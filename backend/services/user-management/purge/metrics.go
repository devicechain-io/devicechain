// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/iam"
)

// Why a purge is held back by one store. A closed set, because it is a metric label.
const (
	// ReasonDeferred is a store that answered, and said its data is still there or still
	// arriving (a retained stream message, a fenced write window, a blob listing it has not
	// finished). It will not clear on its own faster than the store lets it.
	ReasonDeferred = "deferred"
	// ReasonError is a store that could not answer: its Erase returned an error, so the
	// pass could not establish that the store is clean.
	ReasonError = "error"
)

// otherStore is the label value for any store whose name is not one of the fixed set.
const otherStore = "other"

// knownStores is the closed label vocabulary for the per-store series. It is the six stores
// DefaultStores registers, and it is a constant set on purpose: a store name that is not in
// it (a test fake, a future store nobody added here) is folded into "other" instead of
// minting a new series, so the label can never be driven by data.
var knownStores = []string{StoreRelational, StoreTelemetry, StoreBroker, StoreKeyValue, StoreDetect, StoreObject}

var knownReasons = []string{ReasonDeferred, ReasonError}

// storeLabel maps a store name onto the closed label set.
func storeLabel(name string) string {
	for _, k := range knownStores {
		if k == name {
			return k
		}
	}
	return otherStore
}

// Histogram buckets, seconds. Erase calls span milliseconds (a Timescale delete) to many
// minutes (a large blob listing); whole purges span the minimum token hold (minutes) to days.
var (
	eraseBuckets = []float64{0.01, 0.05, 0.25, 1, 5, 30, 120, 600, 1800, 7200}
	purgeBuckets = []float64{60, 600, 1800, 3600, 7200, 21600, 43200, 64800, 86400, 129600, 172800, 345600, 604800}
	cleanBuckets = []float64{1, 10, 60, 300, 900, 1800, 3600, 7200, 21600, 43200, 86400}
)

// purgeBucketsFor adds buckets just above the token hold to the fixed set.
//
// 🔴 EVERY COMPLETED DELETION TAKES AT LEAST THE HOLD, so with the fixed set alone they all
// land in the one bucket above it: at the 43200 s default, (43200, 64800]. A quantile over
// that interpolates linearly across six hours and reads a 12 h 5 min deletion as about 15 h.
// The interesting part of the distribution is how far past the hold a deletion ran, so
// that is where the resolution goes. The hold is per instance, hence derived, not listed.
func purgeBucketsFor(hold time.Duration) []float64 {
	set := map[float64]bool{}
	for _, b := range purgeBuckets {
		set[b] = true
	}
	if h := hold.Seconds(); h > 0 {
		for _, past := range []float64{120, 300, 900, 1800, 3600, 7200, 14400} {
			set[h+past] = true
		}
	}
	out := make([]float64, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Float64s(out)
	return out
}

// metrics are the series that say WHICH store is holding a purge back and HOW LONG a purge
// took. They carry no tenant label, for the reason given on Coordinator.observe: a series
// named for a tenant would outlive the erasure it describes.
type metrics struct {
	// blocked is the number of open deletions each store is currently holding back, by why.
	// Rewritten in full at the end of every pass (every store x reason pair, zeros included),
	// so a store that recovered reads 0 instead of keeping its last non-zero value.
	blocked *prometheus.GaugeVec
	// rows is the progress counter: rows (or objects, messages, keys) each store erased.
	rows *prometheus.CounterVec
	// eraseSeconds is how long one store's Erase call took.
	eraseSeconds *prometheus.HistogramVec

	completed prometheus.Counter
	failures  prometheus.Counter
	// duration is cut -> token release. It includes the token hold, so it is bounded below
	// by it. clean is cut -> the moment the last store went clean, which is the erasure
	// itself without the deliberate waits, and is the figure an erasure commitment is about.
	duration *prometheus.HistogramVec
	clean    prometheus.Observer
}

func newMetrics(ms *core.Microservice, tokenHold time.Duration) *metrics {
	m := &metrics{
		blocked: ms.NewGaugeVec("tenant_purge_store_blocked",
			"Open tenant deletions a store is currently holding back, by store (rdb, tsdb, broker, kv, detect, blob, other) "+
				"and reason (deferred: the store reported data still present or arriving; error: the store could not answer)",
			[]string{"store", "reason"}),
		rows: ms.NewCounterVec("tenant_purge_store_rows_erased_total",
			"Rows, objects, messages or keys each store has erased for tenant deletions; the progress signal for a store that is blocked",
			[]string{"store"}),
		eraseSeconds: ms.NewHistogramVec("tenant_purge_store_erase_duration_seconds",
			"How long one store's erase call took, by store", []string{"store"}, eraseBuckets),
		completed: ms.NewCounter("tenant_purge_completed_total",
			"Tenant deletions completed: every store clean, settled and held, fences lifted, token released"),
		failures: ms.NewCounter("tenant_purge_failures_total",
			"Times the coordinator could not carry one tenant's deletion through a pass (a ledger read or write, an unlifted fence, a refused interlock); "+
				"the deletion is retried on the next pass, so this counts attempts, not abandoned deletions"),
		duration: ms.NewHistogramVec("tenant_purge_duration_seconds",
			"Seconds from a tenant's deletion to its token being released, observed once per completed deletion. Includes the token hold.",
			nil, purgeBucketsFor(tokenHold)),
	}
	m.clean = ms.NewHistogramVec("tenant_purge_clean_duration_seconds",
		"Seconds from a tenant's deletion to the last store reporting clean, observed once per completed deletion. "+
			"Excludes the settle and token-hold waits that follow, so it is the time the erasure itself took.",
		nil, cleanBuckets).WithLabelValues()
	// Register every series at zero so an alert or a dashboard sees 0, not absence.
	for _, s := range append(append([]string{}, knownStores...), otherStore) {
		for _, r := range knownReasons {
			m.blocked.WithLabelValues(s, r).Set(0)
		}
		m.rows.WithLabelValues(s).Add(0)
		m.eraseSeconds.WithLabelValues(s)
	}
	m.completed.Add(0)
	m.duration.WithLabelValues()
	return m
}

// passTally accumulates, across one pass, how many open deletions each store is blocking.
type passTally struct {
	mu      sync.Mutex
	blocked map[[2]string]int
}

func newPassTally() *passTally { return &passTally{blocked: map[[2]string]int{}} }

func (t *passTally) add(store, reason string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.blocked[[2]string{storeLabel(store), reason}]++
	t.mu.Unlock()
}

// addLine classifies one ledger line. The passing replica calls it on the lines it writes
// and a replica that skipped calls it on the lines it reads back, so the two cannot
// disagree about what "blocked" means.
func (t *passTally) addLine(line *iam.TenantPurgeStore) {
	switch {
	case line.Failure != "":
		t.add(line.Store, ReasonError)
	case !line.Complete:
		t.add(line.Store, ReasonDeferred)
	}
}

// publish writes the tally to the gauge: every pair, so a recovered store goes back to 0.
func (m *metrics) publish(t *passTally) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range append(append([]string{}, knownStores...), otherStore) {
		for _, r := range knownReasons {
			m.blocked.WithLabelValues(s, r).Set(float64(t.blocked[[2]string{s, r}]))
		}
	}
}

// recordErase publishes one store call's progress and duration.
func (m *metrics) recordErase(store string, rows int64, d time.Duration) {
	l := storeLabel(store)
	if rows > 0 {
		m.rows.WithLabelValues(l).Add(float64(rows))
	}
	m.eraseSeconds.WithLabelValues(l).Observe(d.Seconds())
}
