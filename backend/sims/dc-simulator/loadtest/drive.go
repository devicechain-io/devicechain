// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Shared harness-drive primitives used by every planted-probe harness (detection
// → alarm, command round-trip, …). They plant the SAME three-population topology
// — a saturating background fleet plus low-cadence safety/edge probes over one
// pinned threshold rule — and only the oracle/classifier differs per harness. The
// emit shapes live here, in ONE place, so the two harnesses can never drift on how
// the background saturates or how a probe crosses the threshold.

// probeStats accumulates the probe-emit accounting. Probe emits are the oracle, so
// unlike the background fleet a probe emit that the ingress does NOT accept (non-202)
// is a run-integrity failure (the planted signal never entered the pipeline), not a
// count to reconcile — surfaced as the probe-delivery invariant.
type probeStats struct {
	mu       sync.Mutex
	failures int
	firstErr error
}

func (s *probeStats) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.firstErr == nil {
		s.firstErr = err
	}
}

func (s *probeStats) snapshot() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures, s.firstErr
}

// partitionByPrefix splits Provision's rendered device set into the three harness
// roles by token prefix. It returns an error if a device matches none — a rendered
// token that fell outside every population prefix means the manifest and this
// partition disagree, and silently dropping it would understate the probe set.
func partitionByPrefix(devices []sim.DeviceInstance, safetyPrefix, edgePrefix, bgPrefix string) (safety, edge, background []sim.DeviceInstance, err error) {
	for _, d := range devices {
		switch {
		case strings.HasPrefix(d.Token, safetyPrefix):
			safety = append(safety, d)
		case strings.HasPrefix(d.Token, edgePrefix):
			edge = append(edge, d)
		case strings.HasPrefix(d.Token, bgPrefix):
			background = append(background, d)
		default:
			return nil, nil, nil, fmt.Errorf("device %q matches no harness population prefix", d.Token)
		}
	}
	return safety, edge, background, nil
}

// driveBackground emits a threshold-crossing sine of the temp metric across the
// background fleet every interval until ctx is cancelled, reusing sim.EmitAll
// (which owns the worker pool + Stats accounting). rt.Devices has been set to the
// background slice by the caller. The sine spans 15–45 C so background devices
// genuinely raise and resolve the rule — and therefore genuinely exercise whatever
// REACT action the harness planted — putting the detection + react path under real
// load.
func driveBackground(ctx context.Context, rt *sim.Runtime, interval time.Duration) {
	if len(rt.Devices) == 0 {
		return
	}
	offset := 2 * math.Pi / float64(len(rt.Devices))
	tick := int64(0)
	emit := func() {
		n := tick
		_ = sim.EmitAll(ctx, rt, rt.Load.Workers(len(rt.Devices)),
			func(i int, _ sim.DeviceInstance) map[string]float64 {
				phase := float64(n)*0.3 + float64(i)*offset
				return map[string]float64{HarnessMetricKey: 30 + 15*math.Sin(phase)}
			})
	}
	emit() // immediate first tick
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			emit()
		}
	}
}

// driveProbes runs the scripted probe emits to completion: 2K steps at interval.
// On even steps the edge probes emit ABOVE (a raise), on odd steps BELOW (a resolve);
// the safety probes emit BELOW on every step (never a raise). Starting on an even
// step and ending on the last odd step means each edge probe does exactly K
// [above, below] cycles, ending on a resolve — so it drives exactly K rising edges,
// each of which is one planted signal (an alarm raise, a command send, …). A probe
// emit the ingress rejects is recorded in probeStats (a run-integrity failure), not
// retried.
//
// It respects ctx: an aborted run returns early (the caller turns that into an error,
// not a verdict). It returns when all 2K steps have been emitted.
func driveProbes(ctx context.Context, rt *sim.Runtime, safety, edge []sim.DeviceInstance, cycles int, interval time.Duration, ps *probeStats) {
	steps := 2 * cycles
	emitStep := func(step int) {
		above := step%2 == 0
		var wg sync.WaitGroup
		emit := func(d sim.DeviceInstance, value float64) {
			defer wg.Done()
			if err := sim.EmitMeasurement(ctx, rt, d, HarnessMetricKey, value); err != nil {
				ps.fail(err)
			}
		}
		for _, d := range edge {
			wg.Add(1)
			value := harnessBelowValue
			if above {
				value = harnessAboveValue
			}
			go emit(d, value)
		}
		for _, d := range safety {
			wg.Add(1)
			go emit(d, harnessBelowValue)
		}
		wg.Wait()
	}

	emitStep(0) // immediate first step so the first raise lands promptly
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for step := 1; step < steps; step++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emitStep(step)
		}
	}
}

// tokensOf extracts the device tokens from a device slice.
func tokensOf(devices []sim.DeviceInstance) []string {
	out := make([]string, len(devices))
	for i, d := range devices {
		out[i] = d.Token
	}
	return out
}
