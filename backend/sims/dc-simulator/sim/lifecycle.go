// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// State is one node of the sim lifecycle FSM (sim-subsystem-contract.md §Seam
// B): CREATED -> BOOTSTRAPPED -> RUNNING <-> STOPPED -> DELETED. dc-simulator
// never reaches DELETED itself — that transition belongs to dcctl (it deletes
// the scoped identity, then the process), so it is modeled here only as a
// terminal state name the /status response can report if a future control
// verb sets it.
type State string

const (
	StateCreated      State = "CREATED"
	StateBootstrapped State = "BOOTSTRAPPED"
	StateRunning      State = "RUNNING"
	StateStopped      State = "STOPPED"
	StateDeleted      State = "DELETED"
)

// Lifecycle drives one Sim's FSM and owns the background tick loop. It is safe
// for concurrent use — the control HTTP server calls Start/Stop/Reset from
// request goroutines while the tick loop runs independently.
type Lifecycle struct {
	sim Sim
	rt  *Runtime

	// name and farEndMode are read off the manifest ONCE, at construction.
	// Manifest() is pure but not cheap — a scenario builds its dashboards there, and
	// /status is polled — so re-deriving a constant on every poll would marshal two
	// board definitions a second to answer a boolean.
	//
	// farEndMode is stored NORMALIZED (SimManifest.FarEndMode, so never ""), which is
	// what lets attachCommandFarEnd and commandFarEndStatus switch on it exhaustively
	// instead of each re-deciding what an empty mode meant.
	name       string
	farEndMode CommandFarEndMode

	mu         sync.Mutex
	state      State
	lastError  string
	cancelTick context.CancelFunc
	// bootstrapMu serializes Bootstrap (and therefore Reset, which is Bootstrap)
	// end to end.
	//
	// 🔴 Bootstrap is reachable concurrently — nothing serializes POST /reset — and
	// while every step of Provision is create-or-ignore, that makes it idempotent,
	// not concurrency-safe: Provision WRITES rt.Devices, and two overlapping runs
	// race on it (confirmed by the race detector). The far end then made the
	// consequence worse than a torn slice, because attaching is check-then-act
	// across a network round trip: both runs see no far end, both build one, and the
	// loser's is overwritten while still connected — never Closed, and holding the
	// same MQTT client ids with CleanSession(false), so the two cohorts evict each
	// other through session takeover indefinitely.
	//
	// It is deliberately NOT mu. mu guards the FSM fields and is taken by /status on
	// every poll; Bootstrap does provisioning and per-device broker round trips, so
	// holding mu across it would stall every status read behind the network. Lock
	// order is bootstrapMu (outer) then mu (inner, briefly); nothing takes them the
	// other way round.
	bootstrapMu sync.Mutex

	// farEnd is the scenario's IN-PROCESS command receiver once Bootstrap attached
	// one — so nil for FarEndNone, for FarEndExternal (whose far end is another
	// process entirely), and for a FarEndInternal scenario started with the explicit
	// opt-out. It outlives Stop deliberately — see Close.
	farEnd CommandFarEnd
	// tickDone is closed by runTickLoop as it exits, so Stop can WAIT for the
	// loop rather than merely signalling it. Without the join, a Stop/Start
	// pair resets the counters while the previous run's workers are still
	// incrementing them, and run 2's window opens holding run 1's emits.
	tickDone chan struct{}
}

// NewLifecycle builds a Lifecycle in the CREATED state.
func NewLifecycle(s Sim, rt *Runtime) *Lifecycle {
	m := s.Manifest()
	return &Lifecycle{sim: s, rt: rt, state: StateCreated, name: m.Name, farEndMode: m.FarEndMode()}
}

// State returns the current FSM state.
func (l *Lifecycle) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// Bootstrap provisions the manifest and advances CREATED/STOPPED -> BOOTSTRAPPED.
// Idempotent: Provision itself create-or-ignores, so calling Bootstrap again
// (via Reset) from any state is always safe.
func (l *Lifecycle) Bootstrap(ctx context.Context) error {
	l.bootstrapMu.Lock()
	defer l.bootstrapMu.Unlock()

	if err := l.sim.Bootstrap(ctx, l.rt); err != nil {
		l.mu.Lock()
		l.lastError = err.Error()
		l.mu.Unlock()
		return err
	}
	// After Provision, so rt.Devices carries the credentials the far end
	// authenticates each subscription with; before BOOTSTRAPPED, so a scenario whose
	// command channel has no far end never reports itself as ready to run.
	if err := l.attachCommandFarEnd(ctx); err != nil {
		l.mu.Lock()
		l.lastError = err.Error()
		l.mu.Unlock()
		return err
	}
	l.mu.Lock()
	if l.state == StateCreated {
		l.state = StateBootstrapped
	}
	l.lastError = ""
	l.mu.Unlock()
	return nil
}

// Reset is an idempotent re-Bootstrap — the sim's whole "start over" story.
// Devices/credentials that already exist are left untouched; only what is
// genuinely missing gets (re-)created.
func (l *Lifecycle) Reset(ctx context.Context) error {
	return l.Bootstrap(ctx)
}

// Start transitions BOOTSTRAPPED/STOPPED -> RUNNING and starts the tick loop
// (one Sim.Tick every EmitInterval) on a goroutine tied to Stop.
func (l *Lifecycle) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.state != StateBootstrapped && l.state != StateStopped {
		s := l.state
		l.mu.Unlock()
		return fmt.Errorf("cannot start from state %s (bootstrap it first)", s)
	}
	tickCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l.cancelTick = cancel
	l.tickDone = done
	l.state = StateRunning
	l.rt.Stats.Reset(time.Now())
	l.mu.Unlock()

	go l.runTickLoop(tickCtx, done)
	return nil
}

// Stop transitions RUNNING -> STOPPED and halts the tick loop. Data already
// emitted is untouched; the tenant/devices/credentials are left in place, so a
// later Start resumes emitting without re-provisioning.
func (l *Lifecycle) Stop() error {
	l.mu.Lock()
	if l.state != StateRunning {
		s := l.state
		l.mu.Unlock()
		return fmt.Errorf("cannot stop from state %s", s)
	}
	cancel, done := l.cancelTick, l.tickDone
	l.cancelTick, l.tickDone = nil, nil
	l.state = StateStopped
	// Release the lock BEFORE joining: runTickLoop takes this same mutex to
	// record a tick error, so waiting for it while holding the lock would
	// deadlock the two against each other.
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	// Freeze only after the loop has genuinely exited, so the elapsed window
	// covers every emit that counted toward it.
	l.rt.Stats.Freeze(time.Now())
	return nil
}

func (l *Lifecycle) runTickLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	interval := l.rt.Load.Interval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			started := time.Now()
			l.rt.Stats.Ticks.Add(1)
			shedBefore := l.rt.Stats.Shed.Load()
			if err := l.sim.Tick(ctx, l.rt); err != nil {
				log.Error().Err(err).Msg("sim tick failed")
				l.mu.Lock()
				l.lastError = err.Error()
				l.mu.Unlock()
			}

			// Attribute this tick's sheds, and SAY SO when the answer changes.
			//
			// 🔴 A shed is not an emit failure — the ingress refuses it cleanly at the
			// per-tenant ceiling (ADR-023/063) and a governed load run expects them, so
			// EmitAll deliberately does not treat one as an error. But on a scenario
			// being watched rather than measured, that silence is the problem: a device
			// whose events are being refused looks exactly like a device that has
			// nothing to say, and on a board built to show what a widget does with
			// awkward data those are the two readings that must not be confused.
			// Logging the transition in both directions puts the fact where a person
			// will see it, rather than only in a counter they would have to think to
			// read. Only the LIFECYCLE does this: the load harness drives EmitAll
			// directly and is unaffected.
			shedNow := l.rt.Stats.Shed.Load() - shedBefore
			if was := l.rt.Stats.LastTickShed.Swap(shedNow); (was == 0) != (shedNow == 0) {
				if shedNow > 0 {
					log.Warn().Int64("shed", shedNow).
						Msg("ingress is SHEDDING this tenant's events at its rate ceiling; " +
							"devices whose widgets look silent are being refused, not quiet")
				} else {
					log.Info().Msg("ingress is no longer shedding this tenant's events")
				}
			}
			// Count a tick that outran the interval it was scheduled on.
			//
			// This counts OVERRUNNING TICKS, not dropped ones: the ticker's
			// channel buffers one, so the first late tick is delayed rather
			// than dropped, and past that a tick running k intervals long
			// drops about k-1 for every 1 counted here. So it is a reliable
			// indicator that the run was over-driven and a poor estimate of by
			// how much — the shortfall's SIZE is Snapshot.Rate against
			// Load.TargetRate.
			if time.Since(started) > interval {
				l.rt.Stats.Overruns.Add(1)
			}
		}
	}
}

// statusResponse is the GET /status body.
//
// It reports the CONFIGURED load next to the ACHIEVED one deliberately. A run
// that quotes a footprint against "N devices at X events/sec" is only honest if
// X was reached, and the two fields together are what let the operator of a
// measurement see that at a glance instead of assuming it.
type statusResponse struct {
	State      State  `json:"state"`
	Tenant     string `json:"tenant"`
	InstanceId string `json:"instanceId"`
	LastError  string `json:"lastError,omitempty"`

	DeviceCount    int      `json:"deviceCount"`
	EmitIntervalMs int64    `json:"emitIntervalMs"`
	TargetRate     float64  `json:"targetRatePerSec"`
	Stats          Snapshot `json:"stats"`

	// CommandFarEnd reports the CONTROL channel the way Stats reports the telemetry
	// one: what was asked for next to what is actually there. A scenario whose
	// command widget looks live is not evidence that anything answers it, and this
	// is the only place that distinction is visible without a live cluster.
	CommandFarEnd farEndStatus `json:"commandFarEnd"`
}

// ControlServer exposes the ADR-035 control-API seam (bootstrap/start/stop/
// status; reset is included as the documented idempotent-re-bootstrap verb) on
// the sim's own HTTP port, alongside the static presentation page and its
// config endpoint. dcctl calls this API for lifecycle management; it is the
// only thing besides the sim process itself that talks to it.
type ControlServer struct {
	lc *Lifecycle
	rt *Runtime
}

// NewControlServer builds a ControlServer for the given lifecycle/runtime.
func NewControlServer(lc *Lifecycle, rt *Runtime) *ControlServer {
	return &ControlServer{lc: lc, rt: rt}
}

// Register mounts the control routes on mux.
func (c *ControlServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /status", c.handleStatus)
	mux.HandleFunc("POST /start", c.handleStart)
	mux.HandleFunc("POST /stop", c.handleStop)
	mux.HandleFunc("POST /reset", c.handleReset)
}

func (c *ControlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	c.lc.mu.Lock()
	resp := statusResponse{State: c.lc.state, LastError: c.lc.lastError}
	resp.Stats = c.rt.Stats.Snapshot(now)
	c.lc.mu.Unlock()
	// Outside the lock: commandFarEndStatus takes the same mutex.
	resp.CommandFarEnd = c.lc.commandFarEndStatus()
	resp.Tenant = c.rt.Tenant
	resp.InstanceId = c.rt.InstanceId
	resp.DeviceCount = len(c.rt.Devices)
	resp.EmitIntervalMs = c.rt.Load.Interval().Milliseconds()
	resp.TargetRate = c.rt.Load.TargetRate(len(c.rt.Devices))
	writeJSON(w, http.StatusOK, resp)
}

func (c *ControlServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := c.lc.Start(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": string(c.lc.State())})
}

func (c *ControlServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := c.lc.Stop(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": string(c.lc.State())})
}

func (c *ControlServer) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := c.lc.Reset(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": string(c.lc.State())})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
