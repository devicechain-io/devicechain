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

	mu         sync.Mutex
	state      State
	lastError  string
	cancelTick context.CancelFunc
	// tickDone is closed by runTickLoop as it exits, so Stop can WAIT for the
	// loop rather than merely signalling it. Without the join, a Stop/Start
	// pair resets the counters while the previous run's workers are still
	// incrementing them, and run 2's window opens holding run 1's emits.
	tickDone chan struct{}
}

// NewLifecycle builds a Lifecycle in the CREATED state.
func NewLifecycle(s Sim, rt *Runtime) *Lifecycle {
	return &Lifecycle{sim: s, rt: rt, state: StateCreated}
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
	if err := l.sim.Bootstrap(ctx, l.rt); err != nil {
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
			if err := l.sim.Tick(ctx, l.rt); err != nil {
				log.Error().Err(err).Msg("sim tick failed")
				l.mu.Lock()
				l.lastError = err.Error()
				l.mu.Unlock()
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
