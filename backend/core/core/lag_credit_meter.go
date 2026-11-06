// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"sync"
	"time"
)

// IngestPhase is the routing phase of ONE reader stream: fresh while the reader is within reach of
// the head of its stream, backlog while it is draining something older.
//
// The phase is per STREAM and driven only by the broker's own stamps of first deliveries, so no
// tenant can hold it open, and it has hysteresis so a reader hovering at the threshold does not flip
// every message between the two routes (each flip would otherwise hand the tenant a second allowance).
type IngestPhase struct {
	mu sync.Mutex
	// calm is how long the corrected age must stay below half the threshold before the phase leaves
	// backlog.
	calm time.Duration

	backlog   bool
	calmSince time.Time // zero while not calm
	exitEpoch uint64
	exitAt    time.Time
}

// DefaultIngestPhaseCalm is how long a stream must stay well below its threshold before it is fresh again.
const DefaultIngestPhaseCalm = 30 * time.Second

// NewIngestPhase builds a phase tracker; a calm that is not positive uses DefaultIngestPhaseCalm.
func NewIngestPhase(calm time.Duration) *IngestPhase {
	if calm <= 0 {
		calm = DefaultIngestPhaseCalm
	}
	return &IngestPhase{calm: calm}
}

// Observe feeds the corrected age (alpha) of one first-delivery message read from the stream and
// returns whether the stream is in its backlog phase AFTER the observation.
//
// It enters backlog the moment alpha exceeds threshold. It leaves only once alpha has stayed below
// threshold/2 continuously for the calm period, observed on both ends (a gap with no traffic between
// two calm observations counts as calm). Leaving advances the exit epoch.
func (p *IngestPhase) Observe(alpha, threshold time.Duration, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case alpha > threshold:
		p.backlog = true
		p.calmSince = time.Time{}
	case !p.backlog:
	case alpha < threshold/2:
		if p.calmSince.IsZero() {
			p.calmSince = now
		}
		if now.Sub(p.calmSince) >= p.calm {
			p.backlog = false
			p.calmSince = time.Time{}
			p.exitEpoch++
			p.exitAt = now
		}
	default:
		p.calmSince = time.Time{}
	}
	return p.backlog
}

// Backlog reports the current phase without observing anything.
func (p *IngestPhase) Backlog() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.backlog
}

// Exit returns the exit epoch and the pod time of the latest exit.
func (p *IngestPhase) Exit() (uint64, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitEpoch, p.exitAt
}

// IngestRoute says which bucket metered a message.
type IngestRoute uint8

const (
	// RouteFresh: a stamped first delivery within reach of its stream's head, metered at pod time with a lag credit.
	RouteFresh IngestRoute = iota + 1
	// RouteBacklog: stamped older than the tenant's cover or read in a stream's backlog phase, metered
	// on the send timeline in that stream's own backlog bucket. Never contention-scaled.
	RouteBacklog
	// RouteLive: no stamp (external MQTT, or metadata unavailable), metered at pod time like fresh traffic.
	RouteLive
)

func (r IngestRoute) String() string {
	switch r {
	case RouteFresh:
		return "fresh"
	case RouteBacklog:
		return "backlog"
	case RouteLive:
		return "live"
	}
	return fmt.Sprintf("IngestRoute(%d)", uint8(r))
}

// IngestCause says why a message was shed.
type IngestCause uint8

const (
	// CauseNone: the message was admitted.
	CauseNone IngestCause = iota
	// CauseCeiling: the tenant exceeded its base ceiling.
	CauseCeiling
	// CauseContention: shed by the overload factor on the fresh route.
	CauseContention
	// CauseHardDrop: factor 0, a boolean refusal of fresh traffic.
	CauseHardDrop
)

func (c IngestCause) String() string {
	switch c {
	case CauseNone:
		return "none"
	case CauseCeiling:
		return "ceiling"
	case CauseContention:
		return "contention"
	case CauseHardDrop:
		return "hard_drop"
	}
	return fmt.Sprintf("IngestCause(%d)", uint8(c))
}

// LagCreditMeter is the decision core of the lag-credited ingest gate: it routes each first delivery
// to the fresh route (a LagCreditLimiter ceiling bucket plus a separate contention bucket) or to its
// stream's backlog bucket, and holds the per-stream phase hysteresis. Like LagCreditLimiter it is a
// pure library; the gateway adopts it behind a switch.
//
//   - The ceiling bucket is ONE per tenant, shared by every source, so a tenant that spreads a send rate
//     over capture, the message store and external MQTT is held to its ceiling once, not once per source.
//   - The contention bucket is separate and is charged only on the fresh route. It never touches the
//     ceiling budget or a drained backlog. A factor of 0 is a boolean refusal of fresh traffic, not a price.
//   - Each stream has its own backlog bucket (a TenantRateLimiter on the BASE ceiling), so two independently
//     drained timelines never share a mark. It is charged at the stamp corrected for the stream's skew.
//   - The ceiling is held ONCE across the fresh sources, but not across the backlog route: each stream's
//     backlog bucket is a full allowance of its own, and a tenant draining two backlogs while also sending
//     live spends all of them. That is bounded (it needs a platform lag beyond the cover, which a tenant
//     cannot steer) and it matches the 2x a tenant already has on the gate it replaces while draining.
//   - A stream in its backlog phase meters ALL of its messages on the backlog route, young ones included,
//     until the hysteresis lets it out. That keeps a reader hovering at the cover from flipping messages
//     between routes, and it means that for the tail of a stall (the drain plus the calm period) the
//     stream's traffic is held to the ceiling but is not contention-scaled.
//
// Redeliveries are not the meter's business: a redelivery already paid on delivery 1, and must not be
// presented here (it would feed the last-stamp the credit is measured from).
type LagCreditMeter struct {
	now        func() time.Time
	threshold  time.Duration
	ceiling    *LagCreditLimiter
	contention *LagCreditLimiter
	backlog    []*TenantRateLimiter
	phases     []*IngestPhase
	base       TenantCeilingResolver
}

// LagCreditMeterConfig builds a LagCreditMeter.
type LagCreditMeterConfig struct {
	// Streams is the number of reader sources (streams); each gets its own backlog bucket and phase.
	Streams int
	// Ceiling is the base per-tenant ceiling. The backlog buckets and the fresh route's cover use it;
	// a contention factor never reaches it.
	Ceiling TenantCeilingResolver
	// PhaseThreshold is the corrected age beyond which a stream is in backlog. Zero uses LagCreditMaxCover.
	PhaseThreshold time.Duration
	// PhaseCalm is how long a stream stays calm before leaving backlog. Zero uses DefaultIngestPhaseCalm.
	PhaseCalm time.Duration
	// Clock replaces time.Now (tests). Nil is time.Now.
	Clock func() time.Time
}

// NewLagCreditMeter builds the meter. A missing resolver or stream count is a construction error and panics.
func NewLagCreditMeter(cfg LagCreditMeterConfig) *LagCreditMeter {
	if cfg.Ceiling == nil || cfg.Streams <= 0 {
		panic("core.NewLagCreditMeter: a ceiling resolver and at least one stream are required")
	}
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	threshold := cfg.PhaseThreshold
	if threshold <= 0 {
		threshold = LagCreditMaxCover
	}
	m := &LagCreditMeter{
		now:        now,
		threshold:  threshold,
		ceiling:    NewLagCreditLimiter(cfg.Ceiling, cfg.Streams, WithLagCreditClock(now)),
		contention: NewLagCreditLimiter(cfg.Ceiling, cfg.Streams, WithLagCreditClock(now)),
		base:       cfg.Ceiling,
	}
	for i := 0; i < cfg.Streams; i++ {
		m.backlog = append(m.backlog, NewTenantRateLimiter(cfg.Ceiling, WithClock(now)))
		m.phases = append(m.phases, NewIngestPhase(cfg.PhaseCalm))
	}
	return m
}

// Phase exposes one stream's phase tracker (for metrics and tests).
func (m *LagCreditMeter) Phase(stream int) *IngestPhase { return m.phases[stream] }

// MeterRequest is one first-delivery message presented to the meter.
type MeterRequest struct {
	Tenant string
	N      int
	// Source is the stream index the message was read from, or negative for a message with no stream
	// (external MQTT). Stamp is the broker append time; zero means none, and the message is RouteLive.
	Source int
	Stamp  time.Time
	// Sources describes every stream's lag and skew (see LagCreditSource); ExitEpoch/ExitAt are filled in
	// by the meter from its own phases.
	Sources []LagCreditSource
	// Factor is the contention factor for this message's tenant class: 1 means none (gold, or no
	// overload), a value in (0,1) scales the tenant's allowance, and 0 refuses fresh traffic outright.
	// The caller maps the tenant class to the factor; gold always passes 1.
	Factor float64
	// Delay and CreditCap are the reading stage's extras; see LagCreditRequest.
	Delay     time.Duration
	CreditCap time.Duration
}

// MeterDecision is the outcome for one message.
type MeterDecision struct {
	Admitted bool
	Route    IngestRoute
	Cause    IngestCause
	// Credit is the lag credit the fresh route was judged against (zero on the backlog route).
	Credit time.Duration
}

// Admit routes and charges one first-delivery message.
func (m *LagCreditMeter) Admit(req MeterRequest) MeterDecision {
	if len(req.Sources) != len(m.phases) {
		panic(fmt.Sprintf("core.LagCreditMeter.Admit: %d sources described, meter has %d streams", len(req.Sources), len(m.phases)))
	}
	if req.Factor < 0 || req.Factor > 1 {
		panic(fmt.Sprintf("core.LagCreditMeter.Admit: factor %v is outside [0,1]", req.Factor))
	}
	if req.N <= 0 {
		return MeterDecision{Admitted: true, Route: RouteFresh}
	}
	now := m.now()
	stamped := req.Source >= 0 && !req.Stamp.IsZero()
	if req.Source >= len(m.phases) {
		panic(fmt.Sprintf("core.LagCreditMeter.Admit: source %d out of range", req.Source))
	}

	// The sources handed to the limiter carry the phases' exit marks.
	srcs := make([]LagCreditSource, len(req.Sources))
	copy(srcs, req.Sources)
	for k := range srcs {
		srcs[k].ExitEpoch, srcs[k].ExitAt = m.phases[k].Exit()
	}

	if stamped {
		alpha := max(now.Sub(req.Stamp)-srcs[req.Source].Skew, 0)
		cover := LagCreditMax(m.base(req.Tenant))
		inBacklog := m.phases[req.Source].Observe(alpha, m.threshold, now)
		if inBacklog || alpha > cover {
			// Metered on the send timeline, with the broker's skew taken back out of the stamp: a leader
			// whose clock runs ahead would otherwise stamp the stream's own near-future, which the limiter
			// clamps to now, charging a whole drain at one instant.
			sent := req.Stamp.Add(srcs[req.Source].Skew)
			m.ceiling.NoteBacklog(req.Tenant, req.Source)
			ok := m.backlog[req.Source].AllowNAt(req.Tenant, sent, req.N)
			d := MeterDecision{Admitted: ok, Route: RouteBacklog}
			if !ok {
				d.Cause = CauseCeiling
			}
			return d
		}
	}

	route := RouteFresh
	if !stamped {
		route = RouteLive
	}
	source, stamp := req.Source, req.Stamp
	if !stamped {
		source, stamp = -1, time.Time{}
	}

	// Contention first, on its own bucket, so a denial there spends nothing of the ceiling budget.
	if req.Factor == 0 {
		return MeterDecision{Route: route, Cause: CauseHardDrop}
	}
	if req.Factor < 1 {
		d := m.contention.Admit(LagCreditRequest{Tenant: req.Tenant, N: req.N, Scale: req.Factor,
			Source: source, Stamp: stamp, Sources: srcs, Delay: req.Delay, CreditCap: req.CreditCap})
		if !d.Admitted {
			return MeterDecision{Route: route, Cause: CauseContention, Credit: d.Credit}
		}
	}
	d := m.ceiling.Admit(LagCreditRequest{Tenant: req.Tenant, N: req.N, Scale: 1,
		Source: source, Stamp: stamp, Sources: srcs, Delay: req.Delay, CreditCap: req.CreditCap})
	if !d.Admitted {
		return MeterDecision{Route: route, Cause: CauseCeiling, Credit: d.Credit}
	}
	return MeterDecision{Admitted: true, Route: route, Credit: d.Credit}
}
