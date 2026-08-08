// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	if got, want := len(NewWidgetlab(1, zero).Manifest().Expand(1)), widgetlabSensorCount+widgetlabEdgeCount; got != want {
		t.Errorf("widgetlab default device count = %d, want %d", got, want)
	}
}

// resizableManifestIds is the scenarios a device-count override applies to at
// all — every registered id except the composed fixtures, which refuse one by
// design (see SimManifest.FixedTopology).
//
// Filtering here is only safe because TestEveryScenarioIsResizableOrSaysWhyNot
// asserts the OTHER direction: that each scenario is resizable or explicitly
// declares why not. Without it, this helper would let a scale scenario slip out
// of every resize test below simply by acquiring the flag. It also refuses to
// return an empty set, so the loops it feeds can never pass vacuously.
func resizableManifestIds(t *testing.T) []string {
	t.Helper()
	ids := ResizableManifestIds()
	if len(ids) == 0 {
		t.Fatal("no resizable scenarios remain, so every resize test below would pass without " +
			"asserting anything")
	}
	return ids
}

// The device-count override must actually change the rendered topology.
//
// The override is the whole point of the slice — a measurement run quoting "500
// devices" is a fabrication if Expand still yields 12 — so it is asserted on
// Expand's output rather than on the field that was set.
func TestDeviceCountOverrideResizesTheRenderedTopology(t *testing.T) {
	for _, id := range resizableManifestIds(t) {
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
	for _, id := range resizableManifestIds(t) {
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

// Every registered scenario is either resizable or explicitly a composed
// fixture, and the test asserts BOTH directions rather than only that a resize
// succeeds. A one-directional check would pass the day a scale scenario
// accidentally acquired a second population and quietly stopped accepting the
// --devices flag it exists to serve.
func TestEveryScenarioIsResizableOrSaysWhyNot(t *testing.T) {
	for _, id := range ManifestIds() {
		base := Registry[id](1, Load{}).Manifest()
		_, err := NewSim(id, 1, Load{DeviceCount: 3})

		if base.FixedTopology {
			if err == nil {
				t.Errorf("scenario %q declares FixedTopology but accepted a device count: "+
					"its dashboards bind named devices, so the run would provision a "+
					"topology those boards do not match", id)
			}
			continue
		}
		if err != nil {
			t.Errorf("scenario %q cannot be resized: %v.\n"+
				"Either give it a single population, decide how one --devices value "+
				"should size several and teach withDeviceCount that rule, or declare "+
				"it FixedTopology if resizing it is meaningless", id, err)
		}
	}
}

// ---- Resizable is NOT load-drivable --------------------------------------------

// The two lists must stay distinguishable for the same reason the two resize refusals
// must, and this is the harder case because the two lists were IDENTICAL for three
// scenarios running — so every consumer that wanted one and reached for the other was
// correct by coincidence, and stayed correct until a scenario broke the tie.
//
// sitepulse is that scenario: resizable (its population is a genuine scale knob) and
// NOT load-drivable (its devices publish their own telemetry, so Sim.Tick emits
// nothing). A load tool offering the resizable list would advertise it, and a run
// against it holds for its whole window, applies zero load, and fails the min-accepted
// floor with a message about lost load flags.
func TestLoadDrivableIsAStrictlyStrongerPropertyThanResizable(t *testing.T) {
	resizable := ResizableManifestIds()
	drivable := LoadDrivableManifestIds()

	// Negative controls first: either list empty would make every check below vacuous,
	// and an EQUAL pair would mean the distinction is currently untested by any real
	// scenario — which is precisely the state that let the conflation ship.
	if len(resizable) == 0 || len(drivable) == 0 {
		t.Fatalf("resizable %v / load-drivable %v: an empty list makes this test vacuous",
			resizable, drivable)
	}
	if slices.Equal(resizable, drivable) {
		t.Fatalf("every resizable scenario is also load-drivable (%v), so nothing in the "+
			"registry distinguishes the two lists and a consumer reaching for the wrong one "+
			"would be right by coincidence", resizable)
	}

	// Load-drivable must be a SUBSET of resizable — it is resizable plus a further
	// condition, so a member of neither direction's difference may appear here.
	for _, id := range drivable {
		if !slices.Contains(resizable, id) {
			t.Errorf("scenario %q is load-drivable but not resizable; a load run sizes its own "+
				"population, so it could never be started", id)
		}
	}

	// And every excluded scenario must be excluded for a REASON the manifest states,
	// rather than by an accident of the derivation.
	for _, id := range resizable {
		m, ok := ScenarioManifest(id)
		if !ok {
			t.Fatalf("ResizableManifestIds offered unregistered id %q", id)
		}
		if got, want := slices.Contains(drivable, id), !m.DevicesPublishTheirOwnTelemetry; got != want {
			t.Errorf("scenario %q: load-drivable=%v but DevicesPublishTheirOwnTelemetry=%v — the "+
				"list and the declaration disagree", id, got, m.DevicesPublishTheirOwnTelemetry)
		}
	}
}

// No load tool may build its --manifest offering from the RESIZABLE list.
//
// This is the regression the whole DevicesPublishTheirOwnTelemetry change exists for,
// and without a gate it reverts in one character: `ResizableManifestIds` and
// `LoadDrivableManifestIds` differ by six letters, both compile, and the wrong one
// produces help text that advertises a scenario whose run holds for its full window and
// then fails the min-accepted floor. Nothing else would notice — the tools are `main`
// packages, so no test can import one and call its flag setup.
//
// A source scan is therefore the instrument available, and it is the same one
// TestDcctlKnowsEveryRegisteredScenario uses for the same reason. It scans ALL of cmd/
// rather than naming loadtest-contention, so a tool added later is covered by existing.
//
// 🔴 go/parser RATHER THAN A GREP, and this file learned that the hard way: the first
// version used strings.Contains and immediately failed on the WORD "ResizableManifestIds"
// sitting in loadtest-contention's own explanatory comment. A regex over source cannot
// tell code from prose — the same defect TestEveryHarnessRuleBuilderIsCollected calls
// out — and it cuts both ways: here it produced a false failure, but the mirror image is
// a gate satisfied by documentation. The AST carries no comments, so a call is a call.
func TestNoLoadToolOffersTheResizableListInsteadOfTheLoadDrivableOne(t *testing.T) {
	const cmdDir = "../cmd"
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("read %s: %v (if the tools moved, re-point this check rather than deleting "+
			"it — it is the only thing standing between a one-word edit and help text that "+
			"advertises an impossible run)", cmdDir, err)
	}

	scanned, offersDrivable := 0, false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(cmdDir, entry.Name(), "main.go")
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			continue
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "ResizableManifestIds":
				t.Errorf("%s CALLS ResizableManifestIds. A load tool must offer "+
					"LoadDrivableManifestIds: resizable only says --devices can change the "+
					"topology, while a scenario whose devices publish their own telemetry emits "+
					"nothing from Sim.Tick and fails the min-accepted floor however it is sized",
					path)
			case "LoadDrivableManifestIds":
				offersDrivable = true
			}
			return true
		})
	}
	// Two negative controls, because the check has two ways to become vacuous: a scan
	// that parsed nothing, and a tree where no tool offers a scenario list at all — at
	// which point "nobody calls the wrong one" is true and means nothing.
	if scanned == 0 {
		t.Fatalf("parsed no main.go under %s; the check has rotted and would accept anything", cmdDir)
	}
	if !offersDrivable {
		t.Error("no tool under ../cmd calls LoadDrivableManifestIds, so this test is asserting " +
			"the absence of a call in a tree that makes no such calls either way")
	}
}

// ScenarioManifest must answer about the scenario AS DECLARED. If it ever applied a
// caller's override, every question asked through it — is this fixed-topology, does it
// self-publish — would be answered about a topology the caller chose.
func TestScenarioManifestAnswersAboutTheUnoverriddenScenario(t *testing.T) {
	for _, id := range ManifestIds() {
		m, ok := ScenarioManifest(id)
		if !ok {
			t.Fatalf("registered scenario %q is not resolvable through ScenarioManifest", id)
		}
		if m.Name == "" {
			t.Errorf("scenario %q resolved to a manifest with no name", id)
		}
		// Its population must be the scenario's own, i.e. what a zero Load renders.
		own := Registry[id](1, Load{}).Manifest()
		if DeviceCount(m) != DeviceCount(own) {
			t.Errorf("scenario %q resolved to %d devices but declares %d",
				id, DeviceCount(m), DeviceCount(own))
		}
	}
	if _, ok := ScenarioManifest("no-such-scenario"); ok {
		t.Error("an unregistered id resolved; a caller would then read a zero manifest as a " +
			"scenario that declares nothing, which is every flag's safe-looking value")
	}
}

// The two refusals must stay distinguishable: a fixed-topology scenario is not
// merely ambiguous to resize, and a reader sent looking for a sizing rule would
// be looking for something that should never be written.
func TestAFixedTopologyManifestIsRefusedOnItsOwnTerms(t *testing.T) {
	m := SimManifest{
		Name:          "composed",
		FixedTopology: true,
		Populations: []PopulationSpec{
			{OfType: "a", Count: 2, TokenPattern: "a-{n}"},
		},
	}
	_, err := withDeviceCount(m, 10)
	if err == nil {
		t.Fatal("a fixed-topology manifest accepted a device count, so its dashboards " +
			"would bind devices the resized run never provisions")
	}
	if !strings.Contains(err.Error(), "fixed topology") {
		t.Errorf("refusal did not name the reason: %v", err)
	}
	// A single population is the shape that WOULD have resized cleanly, so this
	// case proves the fixed-topology check is what refused it — not the
	// multi-population ambiguity guard.
	if strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("a fixed-topology manifest was refused as ambiguous instead: %v", err)
	}
	// With no override there is nothing to refuse.
	if _, err := withDeviceCount(m, 0); err != nil {
		t.Errorf("an un-overridden fixed-topology manifest was rejected: %v", err)
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
	for _, id := range resizableManifestIds(t) {
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

// The same invariant at each scenario's OWN topology, with no override.
//
// The resized test above is where this check started, and routing it through
// resizableManifestIds silently carved a hole in it: a fixed-topology scenario
// is excluded from every count, so it lost the check entirely — and it is the
// kind of scenario whose dashboards bind NAMED devices, which is the only way to
// dangle a binding once resizing is off the table. A dashboard binding a device
// token with a typo in it would have shipped green.
//
// So the invariant is asserted twice, on different axes: above, that resizing
// cannot orphan a binding; here, that authoring one cannot either. This one runs
// over every registered scenario, because "declares no dashboards yet" is a
// passing state that becomes a real check the moment a lane adds one.
func TestEveryScenarioDashboardBindsToADeviceThatExists(t *testing.T) {
	checked := 0
	for _, id := range ManifestIds() {
		s, err := NewSim(id, 1, Load{})
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
			if len(bound) == 0 {
				t.Errorf("%s: dashboard %q names no device at all — either it is no longer "+
					"device-bound, or this test can no longer see what it binds", id, ds.Token)
			}
			for _, token := range bound {
				checked++
				if !live[token] {
					t.Errorf("%s: dashboard %q binds device %q, which the scenario's own "+
						"topology does not contain", id, ds.Token, token)
				}
			}
		}
	}
	// Negative control. Every scenario declaring no dashboard is a legitimate
	// state mid-build, but it is indistinguishable from this test having gone
	// blind, so say which one it is rather than reporting a silent pass.
	if checked == 0 {
		t.Log("no scenario declares a device-bound dashboard yet, so this test asserted nothing")
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
	for _, id := range resizableManifestIds(t) {
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
