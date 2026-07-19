// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A zero Load must reproduce the pre-override behaviour exactly.
//
// This is the counterweight to every test below: making the sim configurable is
// only safe while the demo scenarios everyone already runs — and the
// presentation page built around a 5s cadence — are untouched by default.
func TestAZeroLoadChangesNothing(t *testing.T) {
	var zero Load

	if got := zero.Interval(); got != DefaultEmitInterval {
		t.Errorf("default interval = %s, want %s", got, DefaultEmitInterval)
	}
	if got := len(NewDevicepulse(1, zero).Manifest().Expand(1)); got != 1 {
		t.Errorf("devicepulse default device count = %d, want 1", got)
	}
	if got := len(NewBuildingpulse(1, zero).Manifest().Expand(1)); got != buildingpulseThermostatCount {
		t.Errorf("buildingpulse default device count = %d, want %d", got, buildingpulseThermostatCount)
	}
}

// The device-count override must actually change the rendered topology.
//
// The override is the whole point of the slice — a measurement run quoting "500
// devices" is a fabrication if Expand still yields 12 — so it is asserted on
// Expand's output rather than on the field that was set.
func TestDeviceCountOverrideResizesTheRenderedTopology(t *testing.T) {
	for _, id := range ManifestIds() {
		t.Run(id, func(t *testing.T) {
			s, err := NewSim(id, 1, Load{DeviceCount: 500})
			if err != nil {
				t.Fatalf("NewSim: %v", err)
			}
			devices := s.Manifest().Expand(1)
			if len(devices) != 500 {
				t.Fatalf("device count = %d, want 500", len(devices))
			}

			// Distinct tokens, or "500 devices" is 500 emits against far fewer
			// real ones — which the platform would dedupe into a load nothing
			// like the one being claimed.
			seen := make(map[string]bool, len(devices))
			for _, d := range devices {
				if seen[d.Token] {
					t.Fatalf("duplicate device token %q in a resized population", d.Token)
				}
				seen[d.Token] = true
			}
		})
	}
}

// A resized scenario must still be internally consistent: valid tokens, and
// every assignment pointing at an entity the manifest declares.
//
// Note what this does NOT cover: Validate checks dashboard tokens and
// definitions for shape, never the device tokens a definition binds to. That
// gap is why TestAResizedDashboardBindsToADeviceThatExists is separate.
func TestAResizedScenarioStillValidates(t *testing.T) {
	for _, id := range ManifestIds() {
		for _, count := range []int{1, 7, 250} {
			s, err := NewSim(id, 1, Load{DeviceCount: count})
			if err != nil {
				t.Fatalf("%s: NewSim: %v", id, err)
			}
			if err := s.Manifest().Validate(); err != nil {
				t.Errorf("%s resized to %d devices does not validate: %v", id, count, err)
			}
		}
	}
}

// Every registered scenario must accept a device-count override.
//
// withDeviceCount refuses a manifest with several populations, because one
// count cannot unambiguously size them. That refusal is correct and it is also
// invisible: it surfaces as a startup error on a scenario nobody has written
// yet. Enumerating the Registry means the day a multi-population scenario lands,
// THIS fails — naming the decision that has to be made — rather than the
// measurement run failing later with the same message and less context.
func TestEveryScenarioAcceptsADeviceCountOverride(t *testing.T) {
	for _, id := range ManifestIds() {
		if _, err := NewSim(id, 1, Load{DeviceCount: 3}); err != nil {
			t.Errorf("scenario %q cannot be resized: %v.\n"+
				"Either give it a single population, or decide how one --devices "+
				"value should size several and teach withDeviceCount that rule", id, err)
		}
	}
}

// The ambiguity guard must actually reject, not silently pick a population.
func TestAMultiPopulationManifestRefusesASingleCount(t *testing.T) {
	m := SimManifest{
		Name: "two-populations",
		Populations: []PopulationSpec{
			{OfType: "a", Count: 1, TokenPattern: "a-{n}"},
			{OfType: "b", Count: 1, TokenPattern: "b-{n}"},
		},
	}
	if _, err := withDeviceCount(m, 10); err == nil {
		t.Fatal("a two-population manifest accepted a single device count: one of " +
			"its populations was silently resized and the other was not")
	}
	// With no override there is nothing ambiguous, so it must still pass through.
	if _, err := withDeviceCount(m, 0); err != nil {
		t.Errorf("an un-overridden multi-population manifest was rejected: %v", err)
	}
}

func TestLoadRejectsValuesItCannotRun(t *testing.T) {
	cases := map[string]Load{
		"negative device count": {DeviceCount: -1},
		"negative interval":     {EmitInterval: -time.Second},
		"negative concurrency":  {Concurrency: -4},
	}
	for name, load := range cases {
		if err := load.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestNewSimRejectsAnUnknownScenario(t *testing.T) {
	_, err := NewSim("no-such-scenario", 1, Load{})
	if err == nil {
		t.Fatal("an unknown manifest id was accepted")
	}
	// The known ids are the only thing that makes the error actionable.
	if !strings.Contains(err.Error(), "devicepulse") {
		t.Errorf("error %q does not list the known ids", err)
	}
}

// Workers must be bounded and never zero.
//
// A zero would mean a tick that emits nothing while reporting a target rate;
// an unbounded one would open a connection per device on a 10k-device run.
func TestWorkersStayWithinBounds(t *testing.T) {
	var auto Load
	for _, n := range []int{0, 1, 12, 500, 100000} {
		got := auto.Workers(n)
		if got < 1 {
			t.Errorf("Workers(%d) = %d: a tick with no workers emits nothing", n, got)
		}
		if got > maxConcurrency {
			t.Errorf("Workers(%d) = %d, above the %d bound", n, got, maxConcurrency)
		}
		if n > 0 && got > n {
			t.Errorf("Workers(%d) = %d: more workers than devices to emit", n, got)
		}
	}

	// An explicit value is honoured as-is — the bound is on the DERIVED count,
	// so a deliberate high-concurrency run is still expressible.
	if got := (Load{Concurrency: maxConcurrency * 4}).Workers(10); got != maxConcurrency*4 {
		t.Errorf("explicit concurrency = %d, want %d", got, maxConcurrency*4)
	}
}

func TestTargetRateIsDevicesOverInterval(t *testing.T) {
	// 100 devices every 200ms = 500 events/sec.
	load := Load{DeviceCount: 100, EmitInterval: 200 * time.Millisecond}
	if got := load.TargetRate(100); got != 500 {
		t.Errorf("target rate = %v, want 500", got)
	}
	// The default demo sizing, stated so a regression in it is visible here:
	// 12 thermostats every 5s = 2.4 events/sec, which is the cadence that made
	// this whole slice necessary.
	if got := (Load{}).TargetRate(buildingpulseThermostatCount); got != 2.4 {
		t.Errorf("buildingpulse default rate = %v, want 2.4", got)
	}
}

// The HTTP client's idle-connection pool must be sized to the emit concurrency.
//
// net/http keeps MaxIdleConnsPerHost=2 by default, so concurrent emits beyond
// that tear down and re-dial a connection per POST. That throttles the
// generator AND charges the platform for connection churn no real fleet
// produces — corrupting a footprint measurement in both directions at once.
// It is invisible in every functional test: the emits still succeed.
func TestTheClientPoolIsSizedForTheConcurrency(t *testing.T) {
	hs := &Handshake{
		Tenant: "acme", SimEmail: "s@e", SimPassword: "p", InstanceId: "dc",
		Endpoints: Endpoints{
			UserGraphQL: "http://u", DeviceMgmtGraphQL: "http://d",
			Ingress: "http://i", EventMgmtWS: "ws://w",
		},
	}
	load := Load{DeviceCount: 500, EmitInterval: 100 * time.Millisecond}
	rt, err := NewRuntime(hs, load, 500)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	transport, ok := rt.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport is %T, not a *http.Transport whose pool can be sized",
			rt.HTTPClient.Transport)
	}
	// A concrete floor, not just agreement between two derivations: asserting
	// only "pool >= Workers(500)" would keep passing if both moved together —
	// including down to the net/http default this exists to escape.
	if transport.MaxIdleConnsPerHost <= 2 {
		t.Errorf("MaxIdleConnsPerHost = %d, at or below the net/http default of 2: "+
			"the pool was never sized", transport.MaxIdleConnsPerHost)
	}

	workers := load.Workers(500)
	if transport.MaxIdleConnsPerHost < workers {
		t.Errorf("MaxIdleConnsPerHost = %d for %d concurrent emits: every emit past "+
			"the %d-th re-dials, throttling the generator and billing the platform "+
			"for connection churn", transport.MaxIdleConnsPerHost, workers,
			transport.MaxIdleConnsPerHost)
	}
}

// A resized scenario's dashboards must bind to devices the run actually has.
//
// Nothing else checks this: Manifest.Validate inspects a dashboard's token and
// that its definition is non-empty, never the device tokens inside it, and
// dashboard-management stores the definition opaquely (ADR-039) so the platform
// will not complain either. A dashboard bound to a device that was never
// provisioned simply renders empty — the failure looks like "no data yet",
// which during a measurement run is indistinguishable from a real one.
//
// Scope, stated precisely so nobody over-trusts this: buildingpulse binds
// devices[0], which exists at every count, so the resize ORDER in sim.go is NOT
// pinned by this test — reverting that ordering alone still passes. What is
// pinned is the invariant the ordering protects: if a dashboard ever binds a
// device the run does not have, this fails. A hero picked from the end of the
// population plus the wrong order does trip it.
func TestAResizedDashboardBindsToADeviceThatExists(t *testing.T) {
	for _, id := range ManifestIds() {
		for _, count := range []int{1, 5, 300} {
			s, err := NewSim(id, 1, Load{DeviceCount: count})
			if err != nil {
				t.Fatalf("%s: NewSim: %v", id, err)
			}
			m := s.Manifest()
			live := make(map[string]bool)
			for _, d := range m.Expand(m.Seed) {
				live[d.Token] = true
			}

			for _, ds := range m.Dashboards {
				bound := deviceTokensIn(m, ds.Definition)
				// Naming NO device is the vacuous case: the loop below would
				// pass without checking anything. Either the dashboard stopped
				// binding devices (this test has gone blind) or the token
				// grammar changed under the scanner. Both need a human.
				if len(bound) == 0 {
					t.Errorf("%s at %d devices: dashboard %q names no device at "+
						"all — either it is no longer device-bound, or this test "+
						"can no longer see what it binds", id, count, ds.Token)
				}
				for _, token := range bound {
					if !live[token] {
						t.Errorf("%s at %d devices: dashboard %q binds device %q, "+
							"which the resized topology does not contain",
							id, count, ds.Token, token)
					}
				}
			}
		}
	}
}

// deviceTokensIn finds every device-shaped token a dashboard definition names.
//
// It derives a pattern from each population's own TokenPattern — "bp-therm-
// {n:03d}" becomes /bp-therm-\d+/ — rather than testing membership against the
// live set. Scanning the live set was the first version of this helper and it
// was structurally incapable of failing: it could only ever return tokens that
// were live, so the "binds a device that is gone" case it claimed to detect
// could not be expressed. Deriving the shape independently is what lets a
// STALE token be found and then judged.
func deviceTokensIn(m SimManifest, definition string) []string {
	var found []string
	for _, pop := range m.Populations {
		// Split on the placeholder and quote the literal parts, rather than
		// quoting the whole pattern and trying to un-quote the placeholder
		// back out — QuoteMeta escapes both braces, so the placeholder no
		// longer matches itself once escaped.
		parts := placeholderPattern.Split(pop.TokenPattern, -1)
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		re := regexp.MustCompile(`"(` + strings.Join(parts, `\d+`) + `)"`)
		for _, m := range re.FindAllStringSubmatch(definition, -1) {
			found = append(found, m[1])
		}
	}
	return found
}

// DeviceCount must agree with Expand, at every size.
//
// It exists so callers that need only the SIZE do not materialize a whole
// population (Expand derives a SHA-256 credential per device) — but a cheap
// second derivation of the same quantity is exactly the thing that drifts, and
// it is used to size the emit connection pool. If it ever under-reports, the
// pool is undersized and the generator throttles for a reason no test or log
// would attribute to it.
func TestDeviceCountAgreesWithExpand(t *testing.T) {
	for _, id := range ManifestIds() {
		for _, count := range []int{0, 1, 12, 501} {
			s, err := NewSim(id, 1, Load{DeviceCount: count})
			if err != nil {
				t.Fatalf("%s: NewSim: %v", id, err)
			}
			m := s.Manifest()
			if got, want := DeviceCount(m), len(m.Expand(m.Seed)); got != want {
				t.Errorf("%s at --devices %d: DeviceCount=%d but Expand yields %d",
					id, count, got, want)
			}
		}
	}
}
