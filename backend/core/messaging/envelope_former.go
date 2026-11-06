// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/prometheus/client_golang/prometheus"
)

// The EnvelopeFormer: the producer side of event envelopes groups ONE tenant's events into the
// members of an envelope, with a linger that is only paid when it can pay.
//
// # The predictive linger
//
// An event waits for company only if its tenant's recent arrival rate predicts another event
// inside the linger. The prediction is an exponentially weighted mean of the tenant's
// inter-arrival gap: when it is shorter than the linger the first event of a group opens a
// window, and the group is published when it is full or the linger runs out, whichever comes
// first; when it is longer the event is published at once. A tenant sending an event a second
// therefore never waits, and a tenant at 3,000 events a second pays at most the linger for a k
// of about 16. The wait is measured from the FIRST member and is never extended by a later
// arrival, so the linger is also the bound: no event waits longer than it.
//
// One sample of the gap is clamped (maxGapSamples linger lengths), so a tenant that comes back
// after an hour reads as "slow" for a handful of events and then as fast, not for the next
// forty.
//
// # Isolation
//
// The key is the tenant. A group holds one tenant's events and nothing else; a tenant's open
// group never delays another's, and no state of one tenant is read for another.
//
// # Who runs it
//
// Former is a state machine with no goroutine and no clock of its own: the caller passes the
// time, and owns the timer (Deadline). The capture path calls it from its ONE submitter
// goroutine, which is what an OrderedWriter requires. GroupCommit wraps it with a lock and a
// timer for many submitters (the HTTP handlers).

const (
	// DefaultFormerLinger is how long an event waits for a second one when its tenant's rate says
	// one is coming.
	DefaultFormerLinger = 5 * time.Millisecond
	// MaxFormerLinger is the hard cap on the linger: no configuration can make an event wait
	// longer, so no hop adds more than this to the persist path.
	MaxFormerLinger = 10 * time.Millisecond

	// maxGapSamples clamps one inter-arrival sample, in lingers.
	maxGapSamples = 10
	// gapWeight is the weight of the newest sample in the mean.
	gapWeight = 0.3
	// formerMaxTenants bounds the per-tenant arrival state; past it the idle tenants are dropped,
	// which costs a tenant one event published without a wait.
	formerMaxTenants = 4096
	// formerIdle is how long a tenant with nothing open must be quiet to be dropped.
	formerIdle = time.Minute
)

// FormerConfig sizes the EnvelopeFormer.
type FormerConfig struct {
	// Linger is how long a group may wait for company, measured from its first member. Zero is
	// DefaultFormerLinger.
	Linger time.Duration
	// MaxMembers bounds k. Zero is eventenvelope.MaxMembers; a larger value is refused.
	MaxMembers int
	// MaxBytes bounds the member bytes of one group. Zero is eventenvelope.MaxRawBytes; a larger
	// value is refused.
	MaxBytes int
}

// Validated returns c with its defaults applied, or an error naming what is out of range.
func (c FormerConfig) Validated() (FormerConfig, error) {
	if c.Linger == 0 {
		c.Linger = DefaultFormerLinger
	}
	if c.Linger < 0 || c.Linger > MaxFormerLinger {
		return c, fmt.Errorf("envelope linger %s is out of range: it must be between 0 (the default, %s) and the %s cap",
			c.Linger, DefaultFormerLinger, MaxFormerLinger)
	}
	if c.MaxMembers == 0 {
		c.MaxMembers = eventenvelope.MaxMembers
	}
	if c.MaxMembers < 1 || c.MaxMembers > eventenvelope.MaxMembers {
		return c, fmt.Errorf("envelope members %d is out of range: 1 to %d", c.MaxMembers, eventenvelope.MaxMembers)
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = eventenvelope.MaxRawBytes
	}
	if c.MaxBytes < 1 || c.MaxBytes > eventenvelope.MaxRawBytes {
		return c, fmt.Errorf("envelope bytes %d is out of range: 1 to %d", c.MaxBytes, eventenvelope.MaxRawBytes)
	}
	return c, nil
}

// FormedMember is one event waiting in a group: its bytes (the PUnresolvedEvent as its marshaller
// wrote it) and whatever the caller needs back when the group is published.
type FormedMember struct {
	Bytes []byte
	Token any
	// arrived is when it was added.
	arrived time.Time
}

// FormedBatch is a closed group: one tenant's members in arrival order.
type FormedBatch struct {
	Tenant  string
	Members []FormedMember
	// Waited is how long the first member waited for the group to close: the linger actually
	// paid.
	Waited time.Duration
}

// Former forms groups. It is not safe for concurrent use.
type Former struct {
	cfg     FormerConfig
	metrics *EnvelopeMetrics
	hop     string

	tenants map[string]*formerTenant
	// open is the tenants with an open group, which is what Deadline and Expired walk.
	open map[string]*formerTenant
}

type formerTenant struct {
	members []FormedMember
	bytes   int
	first   time.Time

	last    time.Time
	gap     time.Duration // mean inter-arrival gap
	haveGap bool
}

// NewFormer builds a Former for hop (a metric label: "capture", "http"). metrics may be nil.
func NewFormer(cfg FormerConfig, hop string, metrics *EnvelopeMetrics) (*Former, error) {
	cfg, err := cfg.Validated()
	if err != nil {
		return nil, err
	}
	return &Former{cfg: cfg, hop: hop, metrics: metrics,
		tenants: make(map[string]*formerTenant), open: make(map[string]*formerTenant)}, nil
}

// Linger is the linger in force.
func (f *Former) Linger() time.Duration { return f.cfg.Linger }

// Add adds m to tenant's group at now and returns the groups that closed: the tenant's own when
// m filled it (or when its rate says nobody else is coming), and the previous one when m did not
// fit beside it. An empty result means m is waiting; Deadline says until when.
func (f *Former) Add(now time.Time, tenant string, m FormedMember) []FormedBatch {
	t := f.tenants[tenant]
	if t == nil {
		if len(f.tenants) >= formerMaxTenants {
			f.sweep(now)
		}
		t = &formerTenant{}
		f.tenants[tenant] = t
	}
	if !t.last.IsZero() {
		g := now.Sub(t.last)
		if g < 0 {
			g = 0
		}
		if limit := maxGapSamples * f.cfg.Linger; g > limit {
			g = limit
		}
		if !t.haveGap {
			t.gap, t.haveGap = g, true
		} else {
			t.gap = time.Duration(float64(t.gap)*(1-gapWeight) + float64(g)*gapWeight)
		}
	}
	t.last = now
	m.arrived = now

	var out []FormedBatch
	if len(t.members) > 0 && (len(t.members)+1 > f.cfg.MaxMembers || t.bytes+len(m.Bytes) > f.cfg.MaxBytes) {
		out = append(out, f.closeTenant(now, tenant, t))
	}
	if len(t.members) == 0 {
		t.first = now
		f.open[tenant] = t
	}
	t.members = append(t.members, m)
	t.bytes += len(m.Bytes)

	switch {
	case len(t.members) >= f.cfg.MaxMembers || t.bytes >= f.cfg.MaxBytes:
		out = append(out, f.closeTenant(now, tenant, t))
	case len(t.members) == 1 && !(t.haveGap && t.gap < f.cfg.Linger):
		// The tenant's rate does not predict a second event inside the linger: waiting would cost
		// this event the linger and buy nothing.
		out = append(out, f.closeTenant(now, tenant, t))
	}
	return out
}

// Deadline is when the earliest open group must close, and false when none is open.
func (f *Former) Deadline() (time.Time, bool) {
	var d time.Time
	for _, t := range f.open {
		if end := t.first.Add(f.cfg.Linger); d.IsZero() || end.Before(d) {
			d = end
		}
	}
	return d, !d.IsZero()
}

// Expired closes and returns every group whose linger has run out at now.
func (f *Former) Expired(now time.Time) []FormedBatch {
	var out []FormedBatch
	for tenant, t := range f.open {
		if !now.Before(t.first.Add(f.cfg.Linger)) {
			out = append(out, f.closeTenant(now, tenant, t))
		}
	}
	return out
}

// DrainTenant closes and returns tenant's open group, if any. A message that bypasses the former
// (it cannot be grouped) calls it first, so it cannot overtake an earlier event of the same tenant
// that is still waiting for company.
func (f *Former) DrainTenant(now time.Time, tenant string) []FormedBatch {
	if t := f.open[tenant]; t != nil {
		return []FormedBatch{f.closeTenant(now, tenant, t)}
	}
	return nil
}

// Drain closes and returns every open group, whatever its age: the caller is stopping.
func (f *Former) Drain(now time.Time) []FormedBatch {
	var out []FormedBatch
	for tenant, t := range f.open {
		out = append(out, f.closeTenant(now, tenant, t))
	}
	return out
}

func (f *Former) closeTenant(now time.Time, tenant string, t *formerTenant) FormedBatch {
	b := FormedBatch{Tenant: tenant, Members: t.members, Waited: now.Sub(t.first)}
	if b.Waited < 0 {
		b.Waited = 0
	}
	t.members, t.bytes = nil, 0
	delete(f.open, tenant)
	f.metrics.observeBatch(f.hop, len(b.Members), b.Waited)
	return b
}

// sweep drops the tenants that are idle and have nothing open.
func (f *Former) sweep(now time.Time) {
	for tenant, t := range f.tenants {
		if len(t.members) == 0 && now.Sub(t.last) > formerIdle {
			delete(f.tenants, tenant)
		}
	}
	// Still full: forget the arrival history of EVERY tenant with no open group (an active
	// tenant relearns its rate within a few events) rather than grow without bound. Open groups
	// are never dropped.
	if len(f.tenants) >= formerMaxTenants {
		for tenant, t := range f.tenants {
			if len(t.members) == 0 {
				delete(f.tenants, tenant)
			}
		}
	}
}

// ---- metrics ----------------------------------------------------------------------------

// EnvelopeMetrics are the instruments of the producer side of envelopes: the realised k and the
// linger paid per hop, and the settlement protocol's counters. Build one in the INITIALIZE phase
// (a collector belongs to the process, not to a connection). Every method tolerates a nil
// receiver and a zero value.
type EnvelopeMetrics struct {
	members *prometheus.HistogramVec
	wait    *prometheus.HistogramVec

	duplicates    prometheus.Counter
	recoverySteps prometheus.Counter
	probeFailures prometheus.Counter
	probeDenied   prometheus.Counter
	roundsEvicted prometheus.Counter
	retries       prometheus.Counter
}

// NewEnvelopeMetrics builds the instruments. prefix names the producer ("inbound").
func NewEnvelopeMetrics(ms *core.Microservice, prefix string) *EnvelopeMetrics {
	return &EnvelopeMetrics{
		members: ms.NewHistogramVec(prefix+"_envelope_members",
			"Events in each group the envelope former closed, by hop: the realised k. A group of one is published in the legacy shape",
			[]string{"hop"}, []float64{1, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64}),
		wait: ms.NewHistogramVec(prefix+"_envelope_linger_seconds",
			"How long the first event of each group waited for the group to close, by hop: the linger actually paid",
			[]string{"hop"}, []float64{0, 0.0001, 0.00025, 0.0005, 0.001, 0.002, 0.003, 0.004, 0.005, 0.0075, 0.01, 0.02}),
		duplicates: ms.NewCounter(prefix+"_envelope_duplicates_total",
			"Envelope publishes the broker answered as duplicates of a round it already held (a redelivery, or another pod on the same bucket)"),
		recoverySteps: ms.NewCounter(prefix+"_envelope_recovery_steps_total",
			"Rounds an envelope walk moved past after finding them stored"),
		probeFailures: ms.NewCounter(prefix+"_envelope_probe_failures_total",
			"Stored envelopes a duplicate answer named that could not be read back or understood; each leaves its sources unacknowledged"),
		probeDenied: ms.NewCounter(prefix+"_envelope_probe_denied_total",
			"Read-backs of a stored envelope the broker REFUSED: this writer lacks the read-back grant, which no retry cures; each leaves its sources unacknowledged"),
		roundsEvicted: ms.NewCounter(prefix+"_envelope_rounds_evicted_total",
			"Envelope rounds a duplicate answer named whose message retention had already evicted, so the walk stepped past them: the stream holds less than one duplicate window of messages"),
		retries: ms.NewCounter(prefix+"_envelope_round_retries_total",
			"Envelope rounds the broker rejected, published again under the same id until their outcome was known"),
	}
}

// NewEnvelopeCounters wraps counters a service already registered, for a producer (device-
// management) whose settlement instruments predate the shared ones. Any may be nil.
func NewEnvelopeCounters(duplicates, recoverySteps, probeFailures, retries prometheus.Counter) *EnvelopeMetrics {
	return &EnvelopeMetrics{duplicates: duplicates, recoverySteps: recoverySteps, probeFailures: probeFailures, retries: retries}
}

func (m *EnvelopeMetrics) observeBatch(hop string, k int, wait time.Duration) {
	if m == nil || m.members == nil {
		return
	}
	m.members.WithLabelValues(hop).Observe(float64(k))
	m.wait.WithLabelValues(hop).Observe(wait.Seconds())
}

func incCounter(c prometheus.Counter) {
	if c != nil {
		c.Inc()
	}
}

func (m *EnvelopeMetrics) countDuplicate() {
	if m != nil {
		incCounter(m.duplicates)
	}
}
func (m *EnvelopeMetrics) countRecoveryStep() {
	if m != nil {
		incCounter(m.recoverySteps)
	}
}

// WithProbeDenied attaches the counter of read-backs the broker refused (a missing grant).
func (m *EnvelopeMetrics) WithProbeDenied(c prometheus.Counter) *EnvelopeMetrics {
	m.probeDenied = c
	return m
}

func (m *EnvelopeMetrics) countProbeDenied() {
	if m != nil {
		incCounter(m.probeDenied)
	}
}

// WithRoundsEvicted attaches the counter of rounds found evicted from the stream.
func (m *EnvelopeMetrics) WithRoundsEvicted(c prometheus.Counter) *EnvelopeMetrics {
	m.roundsEvicted = c
	return m
}

func (m *EnvelopeMetrics) countRoundEvicted() {
	if m != nil {
		incCounter(m.roundsEvicted)
	}
}

func (m *EnvelopeMetrics) countProbeFailure() {
	if m != nil {
		incCounter(m.probeFailures)
	}
}
func (m *EnvelopeMetrics) countRetry() {
	if m != nil {
		incCounter(m.retries)
	}
}

// ---- group commit -----------------------------------------------------------------------

// GroupCommit is the Former for MANY submitters: the HTTP handlers, each of which waits for the
// outcome of the group its event joined. Add returns once the group has been published, with the
// publish's error (nil only after the broker's PubAck), so a 202 sent on a nil answer is a
// 202 after the PubAck of the envelope that holds the event: a group commit.
//
// A group is published by whichever goroutine closes it: the submitter whose event filled it (or
// whose tenant's rate said nobody else was coming - then the group is its own event alone and
// it waits for nothing), or the linger's timer. The flush runs on that goroutine, outside the
// lock.
type GroupCommit struct {
	mu    sync.Mutex
	f     *Former
	flush func(FormedBatch)
	timer *time.Timer
	// stopped refuses new work once Stop ran.
	stopped bool
	// wg counts the flushes in flight. A batch is counted under mu, in the same critical section
	// that closed it, so Stop (which takes mu to set stopped) either sees the batch counted or the
	// batch was never closed: there is no window in which a closed batch is uncounted.
	wg sync.WaitGroup
	// afterUnlock, when set (tests only), runs between releasing mu and starting the flushes.
	afterUnlock func()
}

// ErrGroupCommitStopped answers an Add after Stop.
var ErrGroupCommitStopped = errors.New("messaging: the group commit is stopped")

// NewGroupCommit wraps f. flush publishes one closed batch and must settle every member's token
// (it is called on a submitter's or the timer's goroutine, never under the lock).
func NewGroupCommit(f *Former, flush func(FormedBatch)) *GroupCommit {
	g := &GroupCommit{f: f, flush: flush}
	g.timer = time.AfterFunc(time.Hour, g.tick)
	g.timer.Stop()
	return g
}

// Add joins tenant's group. The caller waits on whatever it put in m.Token for the outcome;
// Add itself returns as soon as the member is queued or its group published, and reports
// ErrGroupCommitStopped when it queued nothing.
func (g *GroupCommit) Add(tenant string, m FormedMember) error {
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		return ErrGroupCommitStopped
	}
	ready := g.f.Add(time.Now(), tenant, m)
	g.countLocked(len(ready))
	g.rearmLocked()
	g.mu.Unlock()
	g.flushAll(ready)
	return nil
}

func (g *GroupCommit) rearmLocked() {
	if d, ok := g.f.Deadline(); ok {
		g.timer.Reset(max(time.Until(d), 0))
	}
}

// countLocked registers n closed batches on the wait group. It never calls wg.Add(0): with the
// counter at zero and Stop already in Wait, Add takes the release-the-waiters branch that only the
// final Done is meant to reach. mu must be held, so a batch is counted in the critical section that
// closed it.
func (g *GroupCommit) countLocked(n int) {
	if n > 0 {
		g.wg.Add(n)
	}
}

func (g *GroupCommit) tick() {
	g.mu.Lock()
	if g.stopped {
		// Stop drained every open group and stopped the timer; a tick that was already running
		// has nothing to close and must not re-arm the timer.
		g.mu.Unlock()
		return
	}
	ready := g.f.Expired(time.Now())
	g.countLocked(len(ready))
	g.rearmLocked()
	g.mu.Unlock()
	g.flushAll(ready)
}

// flushAll publishes each closed batch on a goroutine of its own, so a batch whose PubAck stalls
// (one tenant's stream trouble, a slow broker) never delays another batch's answer: not the
// requests behind it in the same call, not the timer's next tick. The caller has already counted
// every batch in ready on wg, under mu, in the critical section that closed them.
//
// A batch closes only under mu and only before Stop: Add refuses once stopped, and Stop drains
// every open group in the same critical section that sets stopped, so a timer tick that runs
// after Stop finds nothing to close. Every batch ever closed is therefore published before Stop
// returns; a request that arrives after Stop is refused with ErrGroupCommitStopped, never queued.
func (g *GroupCommit) flushAll(ready []FormedBatch) {
	if g.afterUnlock != nil && len(ready) > 0 {
		g.afterUnlock()
	}
	for _, b := range ready {
		go func() {
			defer g.wg.Done()
			g.flush(b)
		}()
	}
}

// Stop refuses further Adds, flushes every open group at once and waits for every flush in flight.
func (g *GroupCommit) Stop() {
	g.mu.Lock()
	g.stopped = true
	g.timer.Stop()
	ready := g.f.Drain(time.Now())
	g.countLocked(len(ready))
	g.mu.Unlock()
	g.flushAll(ready)
	g.wg.Wait()
}
