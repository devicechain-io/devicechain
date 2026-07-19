// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/userclient"
)

// httpTimeout bounds a single provisioning/emit round-trip.
const httpTimeout = 15 * time.Second

// Stats is the sim's own accounting of what it actually emitted, as opposed to
// what it was configured to emit.
//
// It exists because a load generator's configured rate is a REQUEST, not a
// measurement. Serial emits, ingress backpressure, per-tenant rate limiting
// (ADR-023) and a dropped tick all reduce the achieved rate silently, and a
// footprint number attributed to "N devices at X events/sec" is wrong by
// exactly that much. These counters are what let a run state the load it
// actually applied instead of the one it asked for.
type Stats struct {
	// Emitted/Failed count individual device emits (HTTP 202 vs anything else).
	Emitted atomic.Int64
	Failed  atomic.Int64
	// Overruns counts ticks that took LONGER than the interval they were
	// scheduled on. Two things it is NOT, both measured:
	//
	//  1. It is not a count of dropped ticks. It is bounded above by the number
	//     of ticks that RAN, while the number dropped is not — a tick running k
	//     intervals long drops about k-1 for every 1 counted here.
	//
	//  2. It does not detect a shortfall caused by FAST REJECTION. If the
	//     ingress rejects (a 429 from per-tenant rate limiting, ADR-023), the
	//     tick gets SHORTER, not longer. Measured: at a 10%-accept ingress the
	//     sim applied 10% of its target rate with Overruns sitting at exactly
	//     0 for the whole run.
	//
	// So it diagnoses ONE cause (emit latency) and is silent on another. The
	// shortfall itself is Snapshot.Rate against Load.TargetRate; Failed is what
	// distinguishes a rejecting ingress from a slow one.
	Overruns atomic.Int64

	// Ticks counts completed ticks, which is the SAMPLE SIZE behind the rate.
	//
	// The rate is discretized: after k ticks, emitted is k*devices while
	// elapsed is wall-clock, so a run only a few ticks long reports a sawtooth
	// that can sit far below the true rate (measured: a 5s cadence sampled over
	// 10s reads 50% low). It converges as ticks accumulate. Publishing the
	// count alongside the rate is what lets a reader see whether the rate is
	// worth believing, instead of inferring precision that a 2-tick run does
	// not have.
	Ticks atomic.Int64

	// sinceNanos is when the current run started and frozenNanos is its
	// elapsed time once stopped (0 while running), both as Unix/duration
	// nanoseconds.
	//
	// They are atomics rather than plain time.Times because Stats is exported
	// and its methods are exported: making thread-safety depend on a caller
	// happening to hold the Lifecycle's private mutex is an invariant nothing
	// enforces, that no test would catch (they drive Stats single-threaded),
	// and that the race detector cannot see. A second reader — a metrics
	// endpoint, the presentation page — would silently introduce a data race.
	sinceNanos  atomic.Int64
	frozenNanos atomic.Int64
}

// Reset zeroes the counters and restarts the clock, so the rate they describe
// is the CURRENT run's rather than an average smeared across however long the
// process happened to sit stopped in between. A measurement wants the former;
// nothing wants the latter.
func (s *Stats) Reset(now time.Time) {
	s.Emitted.Store(0)
	s.Failed.Store(0)
	s.Overruns.Store(0)
	s.Ticks.Store(0)
	s.frozenNanos.Store(0)
	s.sinceNanos.Store(now.UnixNano())
}

// Freeze fixes the elapsed time at the moment a run stopped.
//
// Without it the achieved rate decays without bound after Stop: the numerator
// stops growing while the denominator keeps counting wall-clock, so a 60s run
// at 2500/s reads as 1250/s when someone checks /status a minute later — half
// the truth, reported as confidently as the real number. That is the exact
// shape of failure this accounting exists to prevent, so the read has to stay
// correct after the run it describes has ended.
func (s *Stats) Freeze(now time.Time) {
	since := s.sinceNanos.Load()
	if since == 0 {
		return
	}
	if elapsed := now.UnixNano() - since; elapsed > 0 {
		s.frozenNanos.Store(elapsed)
	}
}

// Snapshot is a consistent-enough read of the counters for reporting: the
// achieved rate over the run so far, alongside the raw totals.
type Snapshot struct {
	Emitted  int64   `json:"emitted"`
	Failed   int64   `json:"failed"`
	Overruns int64   `json:"overruns"`
	Ticks    int64   `json:"ticks"`
	Seconds  float64 `json:"elapsedSeconds"`
	Rate     float64 `json:"achievedRatePerSec"`
}

// Snapshot reads the counters and derives the achieved emit rate. The reads are
// individually atomic but not mutually consistent — over a reporting interval
// that skew is far below the precision anyone should read into the rate.
func (s *Stats) Snapshot(now time.Time) Snapshot {
	snap := Snapshot{
		Emitted:  s.Emitted.Load(),
		Failed:   s.Failed.Load(),
		Overruns: s.Overruns.Load(),
		Ticks:    s.Ticks.Load(),
	}
	// A frozen elapsed wins: once a run has stopped, its rate is a fact about a
	// finished window and must not keep being divided by wall-clock.
	if frozen := s.frozenNanos.Load(); frozen > 0 {
		snap.Seconds = time.Duration(frozen).Seconds()
	} else if since := s.sinceNanos.Load(); since != 0 {
		snap.Seconds = now.Sub(time.Unix(0, since)).Seconds()
	}
	if snap.Seconds > 0 {
		snap.Rate = float64(snap.Emitted) / snap.Seconds
	}
	return snap
}

// Runtime is the shared handle every Sim implementation's Bootstrap/Tick
// receives: the authenticated tenant session, the resolved endpoints, and the
// devices Bootstrap provisioned (populated once bootstrap succeeds, so Tick can
// iterate them without re-deriving anything).
type Runtime struct {
	Session    *userclient.TenantSession
	Endpoints  Endpoints
	InstanceId string
	Tenant     string
	HTTPClient *http.Client

	// Devices is the manifest's Expand()'d device set, filled in by
	// bootstrap.go's Provision once every device+credential exists. Tick reads
	// it; nothing else mutates it after Bootstrap returns.
	Devices []DeviceInstance

	// Stats accumulates emit accounting across the process lifetime.
	Stats Stats

	// Load is the run-time load profile, carried here because Runtime is the
	// handle every Tick already receives — so a scenario derives its emit
	// concurrency from the SAME profile that sized this client's connection
	// pool, rather than from a second copy that could disagree with it.
	Load Load
}

// NewRuntime builds a Runtime from a validated Handshake, with its HTTP client
// sized for the concurrency load implies over deviceCount devices. No network
// call happens here — TenantSession authenticates lazily on first use.
func NewRuntime(hs *Handshake, load Load, deviceCount int) (*Runtime, error) {
	if err := core.ValidateToken(hs.Tenant); err != nil {
		return nil, err
	}

	// Size the idle-connection pool to the emit concurrency. net/http defaults
	// MaxIdleConnsPerHost to 2, so concurrent emits beyond that would open and
	// tear down a connection per POST — throttling the generator and charging
	// the platform for connection churn that no real device fleet produces.
	// Both of those corrupt a footprint measurement, in opposite directions.
	workers := load.Workers(deviceCount)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = workers * 2
	transport.MaxIdleConnsPerHost = workers

	httpc := &http.Client{Timeout: httpTimeout, Transport: transport}
	return &Runtime{
		Session:    userclient.NewTenantSession(httpc, hs.Endpoints.UserGraphQL, hs.SimEmail, hs.SimPassword, hs.Tenant),
		Endpoints:  hs.Endpoints,
		InstanceId: hs.InstanceId,
		Tenant:     hs.Tenant,
		HTTPClient: httpc,
		Load:       load,
	}, nil
}
