// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"fmt"
	"time"
)

// DefaultEmitInterval is the sim's demo telemetry cadence (contract: "~5s").
// It is the cadence a scenario runs at when nothing overrides it, which is what
// the presentation page was built around — slow enough to watch.
const DefaultEmitInterval = 5 * time.Second

// maxConcurrency bounds the derived worker count so a large device population
// does not open an unbounded number of simultaneous connections to the ingress.
// It is a guard on the SIM, not a statement about what the platform can take.
const maxConcurrency = 64

// Load is the run-time load profile: how many devices a scenario runs and how
// often each one emits. It exists because the built-in scenarios are sized as
// DEMOS — 1 device at 5s, or 12 at 5s (2.4 events/sec) — which is the right
// default for watching the presentation page and useless for measuring what an
// instance costs under load.
//
// Every field's zero value means "use the scenario's own default", so a Load{}
// reproduces the pre-existing behaviour exactly.
type Load struct {
	// DeviceCount overrides the scenario's population size. 0 keeps the
	// manifest's own count.
	DeviceCount int
	// EmitInterval overrides the tick cadence. 0 keeps DefaultEmitInterval.
	EmitInterval time.Duration
	// Concurrency bounds how many emits are in flight at once. 0 derives it
	// from the device count (see Workers).
	Concurrency int
}

// Interval returns the effective tick cadence.
func (l Load) Interval() time.Duration {
	if l.EmitInterval <= 0 {
		return DefaultEmitInterval
	}
	return l.EmitInterval
}

// Workers returns the effective emit concurrency for a population of n devices.
//
// Concurrency is not a cosmetic tuning knob here: Tick emits one HTTP POST per
// device and the ticker DROPS a tick that arrives while the previous one is
// still running. A serial emit of n devices therefore silently caps the
// achieved rate at 1/latency regardless of what interval was asked for — the
// load generator would report a target it never reached. Emitting concurrently
// is what makes the configured rate reachable; nothing here can guarantee it
// for an arbitrary n, which is why Snapshot reports the rate actually achieved
// rather than leaving anyone to assume this worked.
func (l Load) Workers(n int) int {
	if l.Concurrency > 0 {
		return l.Concurrency
	}
	if n < 1 {
		return 1
	}
	if n > maxConcurrency {
		return maxConcurrency
	}
	return n
}

// Validate rejects a load profile that cannot be run as asked, rather than
// quietly substituting a default for it. A negative count or interval is a
// typo, not a request.
func (l Load) Validate() error {
	if l.DeviceCount < 0 {
		return fmt.Errorf("device count %d is negative", l.DeviceCount)
	}
	if l.EmitInterval < 0 {
		return fmt.Errorf("emit interval %s is negative", l.EmitInterval)
	}
	if l.Concurrency < 0 {
		return fmt.Errorf("concurrency %d is negative", l.Concurrency)
	}
	return nil
}

// TargetRate returns the events/sec this load profile asks for over a
// population of n devices — the number to compare Stats' achieved rate against.
func (l Load) TargetRate(n int) float64 {
	return float64(n) / l.Interval().Seconds()
}

// withDeviceCount returns m with its single population resized to count.
//
// It returns an error rather than resizing "the first" population when a
// manifest declares several: with more than one population, a single
// DeviceCount has no unambiguous meaning (split evenly? scale proportionally?
// apply to each?), and every answer silently produces a topology the caller did
// not ask for. Today every registered scenario declares exactly one population
// — TestEveryScenarioAcceptsADeviceCountOverride holds that true — so this
// branch is a guard against a future scenario, and the day one lands it fails
// loudly here instead of mis-sizing a measurement run.
func withDeviceCount(m SimManifest, count int) (SimManifest, error) {
	if count <= 0 {
		return m, nil
	}
	if len(m.Populations) != 1 {
		return SimManifest{}, fmt.Errorf(
			"scenario %q declares %d populations, so a single device count of %d is "+
				"ambiguous; size its populations in the manifest instead",
			m.Name, len(m.Populations), count)
	}
	pops := make([]PopulationSpec, len(m.Populations))
	copy(pops, m.Populations)
	pops[0].Count = count
	m.Populations = pops
	return m, nil
}

// resize applies load's device-count override to a scenario's own manifest.
//
// It panics on the ambiguity withDeviceCount rejects because NewSim already
// refused that combination at startup: reaching it means a driver was
// constructed by calling a New* constructor directly, which is a programming
// error rather than a runtime condition. Same reasoning (and same house style)
// as buildingpulse's panic on a dashboard it cannot marshal.
func resize(load Load, m SimManifest) SimManifest {
	resized, err := withDeviceCount(m, load.DeviceCount)
	if err != nil {
		panic(fmt.Sprintf("sim: %v (construct scenarios with NewSim, which refuses this)", err))
	}
	return resized
}

// NewSim builds the registered scenario named manifestId under load.
//
// It is the only supported way to construct a driver, because it is where a
// load profile meets the manifest it has to be legal against — the Registry
// constructors themselves cannot report that mismatch (Manifest returns no
// error). Validating here means an impossible run is refused at startup rather
// than discovered as a wrong number hours into a measurement.
func NewSim(manifestId string, seed int64, load Load) (Sim, error) {
	newDriver, ok := Registry[manifestId]
	if !ok {
		return nil, fmt.Errorf("unknown manifest id %q (known: %v)", manifestId, ManifestIds())
	}
	if err := load.Validate(); err != nil {
		return nil, err
	}
	// Inspect the scenario's UNMODIFIED shape (a zero Load applies no override)
	// to decide whether the requested count is legal against it. Asking the
	// already-overridden manifest would be asking a question whose answer the
	// override itself determined.
	base := newDriver(seed, Load{}).Manifest()
	if _, err := withDeviceCount(base, load.DeviceCount); err != nil {
		return nil, err
	}
	return newDriver(seed, load), nil
}
