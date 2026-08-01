// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"sync"
	"testing"
)

// ---- The sweep and the threshold are checked against EACH OTHER --------------
//
// These are the tests the spec asks for by name. The failure they exist to catch
// is not a wrong number in isolation — it is two lanes drifting apart: the rule
// author moves the threshold, the generator changes the amplitude, both PRs stay
// green, and the scenario silently stops raising alarms. Every assertion below
// reads BOTH sides.

// The constants alone decide whether an alarm is possible, before any curve is
// evaluated. A threshold outside the sweep's bounds can never be crossed, and one
// exactly AT a bound can never be crossed and cleared.
func TestWidgetlabThresholdLiesStrictlyInsideTheSweep(t *testing.T) {
	if WidgetlabSweepMin >= WidgetlabSweepMax {
		t.Fatalf("sweep bounds are inverted or empty: min %v, max %v", WidgetlabSweepMin, WidgetlabSweepMax)
	}
	if WidgetlabAlarmThreshold <= WidgetlabSweepMin {
		t.Errorf("threshold %v is at or below the sweep's minimum %v, so the alarm would raise "+
			"immediately and never clear", WidgetlabAlarmThreshold, WidgetlabSweepMin)
	}
	if WidgetlabAlarmThreshold >= WidgetlabSweepMax {
		t.Errorf("threshold %v is at or above the sweep's maximum %v, so the sweep can never "+
			"reach it and no alarm would ever raise", WidgetlabAlarmThreshold, WidgetlabSweepMax)
	}
}

// The property the whole alarm channel rests on: within ONE period the sweep goes
// above the threshold and comes back below it. Crossing without clearing leaves
// the alarm integrator monotonic — alarm-count only ever climbs, and alarm-table
// never shows a resolve, which is half the widget unexercised.
func TestWidgetlabSweepCrossesAndClearsTheThreshold(t *testing.T) {
	var above, below bool
	for tick := int64(0); tick < WidgetlabSweepTicks; tick++ {
		switch v := widgetlabSweep(tick); {
		case v > WidgetlabAlarmThreshold:
			above = true
		case v < WidgetlabAlarmThreshold:
			below = true
		}
	}
	if !above {
		t.Errorf("the sweep never exceeds the alarm threshold %v in a full period, so no alarm raises",
			WidgetlabAlarmThreshold)
	}
	if !below {
		t.Errorf("the sweep never falls below the alarm threshold %v in a full period, so a raised "+
			"alarm never clears", WidgetlabAlarmThreshold)
	}
}

// A crossing has to happen in BOTH directions to be a raise and a resolve rather
// than two raises.
//
// It does NOT pin the shape, and an earlier version of this comment claimed it
// did. A sawtooth also scores one rise and one fall — its wrap-around cliff is
// sampled and counted as the fall — so it passes here unchanged. The shape is
// pinned by TestWidgetlabSweepHasAUniformStep instead.
func TestWidgetlabSweepCrossesTheThresholdExactlyTwicePerPeriod(t *testing.T) {
	rising, falling := 0, 0
	prev := widgetlabSweep(0)
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		v := widgetlabSweep(tick)
		if prev <= WidgetlabAlarmThreshold && v > WidgetlabAlarmThreshold {
			rising++
		}
		if prev > WidgetlabAlarmThreshold && v <= WidgetlabAlarmThreshold {
			falling++
		}
		prev = v
	}
	if rising != 1 || falling != 1 {
		t.Errorf("expected exactly one rise and one fall through %v per period, got %d rising and %d falling",
			WidgetlabAlarmThreshold, rising, falling)
	}
}

// The gauge's scale is authored from these bounds, so the needle only visits both
// ends if the sweep actually attains them. It attains the maximum only because the
// period is EVEN — with an odd period the peak falls between two ticks and the
// gauge silently never fills.
func TestWidgetlabSweepReachesBothEnds(t *testing.T) {
	if WidgetlabSweepTicks%2 != 0 {
		t.Fatalf("sweep period %d is odd, so the peak falls between ticks and the sweep never "+
			"reaches its own maximum", WidgetlabSweepTicks)
	}
	var min, max = math.Inf(1), math.Inf(-1)
	for tick := int64(0); tick < WidgetlabSweepTicks; tick++ {
		v := widgetlabSweep(tick)
		min = math.Min(min, v)
		max = math.Max(max, v)
	}
	if min != WidgetlabSweepMin {
		t.Errorf("sweep bottoms out at %v, not its declared minimum %v", min, WidgetlabSweepMin)
	}
	if max != WidgetlabSweepMax {
		t.Errorf("sweep peaks at %v, not its declared maximum %v", max, WidgetlabSweepMax)
	}
}

// Total in tick, including the negative and very large values a caller has no
// reason to pass but a refactor might. A wave that folds past zero would emit
// out-of-range temperatures the gauge cannot display.
func TestWidgetlabSweepStaysInBoundsForAnyTick(t *testing.T) {
	for _, tick := range []int64{-1, -45, -91, 0, 1, 1_000_003, math.MaxInt64 - 1} {
		v := widgetlabSweep(tick)
		if v < WidgetlabSweepMin || v > WidgetlabSweepMax {
			t.Errorf("tick %d produced %v, outside [%v, %v]", tick, v, WidgetlabSweepMin, WidgetlabSweepMax)
		}
	}
}

// The shape itself: consecutive ticks always move by the SAME amount.
//
// This is what makes it a triangle rather than merely something that crosses the
// threshold twice. An amplitude-correct sawtooth attains both bounds, crosses once
// each way, and satisfies every other test in this file — it differs only in
// having one enormous step where it wraps, which is exactly what this catches. The
// even period is what makes the step uniform across the wrap too, so this and
// TestWidgetlabSweepReachesBothEnds pin the same constant from two directions.
//
// It matters because the curve is a display artifact as much as an alarm trigger:
// a cliff draws a vertical line on the chart and snaps the gauge, and the whole
// argument for a triangle over a sine was that a reader can follow the slope.
func TestWidgetlabSweepHasAUniformStep(t *testing.T) {
	step := (WidgetlabSweepMax - WidgetlabSweepMin) / float64(WidgetlabSweepTicks/2)
	for tick := int64(0); tick < WidgetlabSweepTicks; tick++ {
		got := math.Abs(widgetlabSweep(tick+1) - widgetlabSweep(tick))
		// A tolerance, not equality: the ramp is computed by division, so the
		// endpoints differ from a repeated addition in the last bits.
		if math.Abs(got-step) > 1e-9 {
			t.Fatalf("tick %d → %d moved by %v, not the uniform step %v — the sweep has a "+
				"discontinuity, so it is not the triangle the gauge and chart are designed around",
				tick, tick+1, got, step)
		}
	}
}

// Rising through the first half and falling through the second, which is the
// direction half of the shape: a uniform step alone would also admit a curve that
// ran the ramp backwards.
func TestWidgetlabSweepRisesThenFalls(t *testing.T) {
	half := int64(WidgetlabSweepTicks / 2)
	for tick := int64(0); tick < half; tick++ {
		if widgetlabSweep(tick+1) <= widgetlabSweep(tick) {
			t.Fatalf("tick %d → %d does not rise, but it is in the sweep's first half", tick, tick+1)
		}
	}
	for tick := half; tick < WidgetlabSweepTicks; tick++ {
		if widgetlabSweep(tick+1) >= widgetlabSweep(tick) {
			t.Fatalf("tick %d → %d does not fall, but it is in the sweep's second half", tick, tick+1)
		}
	}
}

// Deterministic and periodic: a fixture, a test and a live run must agree on what
// tick N looks like. The tick counter is process-local, so a restart REPLAYS the
// curve from its start rather than resuming — reproducibility, not continuity.
func TestWidgetlabSweepIsDeterministicAndPeriodic(t *testing.T) {
	for tick := int64(0); tick < WidgetlabSweepTicks; tick++ {
		if a, b := widgetlabSweep(tick), widgetlabSweep(tick); a != b {
			t.Fatalf("tick %d is not deterministic: %v then %v", tick, a, b)
		}
		if a, b := widgetlabSweep(tick), widgetlabSweep(tick+WidgetlabSweepTicks); a != b {
			t.Errorf("tick %d and tick %d differ (%v vs %v), so the sweep is not periodic",
				tick, tick+WidgetlabSweepTicks, a, b)
		}
	}
}

// ---- The sweep has to reach the WIRE ----------------------------------------
//
// Everything above checks the sweep against the threshold. That closes one seam
// and leaves the next one open: nothing in it observes what Tick actually EMITS.
// Replacing widgetlabSweep(n) in Tick with a constant below the threshold — an
// alarm that can never raise — left every test in this package green, which is
// precisely the silent-drift failure the shared constants exist to prevent, just
// moved one link along.
//
// So this drives the real Tick through the real emit path and reads the value off
// the wire. It is deliberately end-to-end rather than a test of an extracted pure
// function: extracting one would move the seam again rather than close it.

// tickOnce runs one Tick against a fake ingress and returns the measurements the
// first device emitted, parsed back from the wire representation.
func tickOnce(t *testing.T, s Sim, tick int) map[string]float64 {
	t.Helper()
	var (
		mu   sync.Mutex
		last map[string]float64
	)
	rt := fakeIngress(t, 1, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		mu.Lock()
		defer mu.Unlock()
		last = map[string]float64{}
		for _, entry := range body.Payload.Entries {
			for key, raw := range entry.Measurements {
				last[key] = parseFloat(t, raw)
			}
		}
		w.WriteHeader(http.StatusAccepted)
	})

	for i := 0; i < tick; i++ {
		if err := s.Tick(context.Background(), rt); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if last == nil {
		t.Fatal("no measurement reached the ingress")
	}
	return last
}

// The temperature on the wire IS the sweep — the one link that makes the
// threshold tests mean anything about a running scenario.
func TestWidgetlabEmitsTheSweptTemperature(t *testing.T) {
	for _, tick := range []int{1, 2, 23, WidgetlabSweepTicks / 2, WidgetlabSweepTicks} {
		got := tickOnce(t, NewWidgetlab(1, Load{}), tick)
		want := widgetlabSweep(int64(tick))
		if got[WidgetlabTemperatureKey] != want {
			t.Errorf("tick %d emitted %s = %v, want the sweep's %v",
				tick, WidgetlabTemperatureKey, got[WidgetlabTemperatureKey], want)
		}
	}
}

// Emitting the sweep under some other key would satisfy the test above for a
// metric no rule reads. Every metric the profile declares must arrive, and no
// metric it does not declare may.
func TestWidgetlabEmitsExactlyTheMetricsItsProfileDeclares(t *testing.T) {
	s := NewWidgetlab(1, Load{})
	declared := map[string]bool{}
	for _, p := range s.Manifest().Profiles {
		for _, metric := range p.Metrics {
			declared[metric.Key] = true
		}
	}

	emitted := tickOnce(t, s, 1)
	for key := range declared {
		if _, ok := emitted[key]; !ok {
			t.Errorf("profile declares %q but no measurement carried it, so a widget bound "+
				"to it renders empty", key)
		}
	}
	for key := range emitted {
		if !declared[key] {
			t.Errorf("emitted %q, which the profile does not declare — the platform has no "+
				"metric definition to bind it to", key)
		}
	}
}

// A tick must cross the threshold on the wire, not merely in the pure function.
func TestWidgetlabEmittedTemperatureCrossesTheThreshold(t *testing.T) {
	s := NewWidgetlab(1, Load{})
	var above, below bool
	// One Sim instance, ticked through a whole period, so this reads the same
	// counter a running scenario does rather than a freshly-seeded one each time.
	rt := fakeIngress(t, 1, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		for _, entry := range body.Payload.Entries {
			switch v := parseFloat(t, entry.Measurements[WidgetlabTemperatureKey]); {
			case v > WidgetlabAlarmThreshold:
				above = true
			case v < WidgetlabAlarmThreshold:
				below = true
			}
		}
		w.WriteHeader(http.StatusAccepted)
	})
	for i := 0; i < WidgetlabSweepTicks; i++ {
		if err := s.Tick(context.Background(), rt); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if !above || !below {
		t.Errorf("over a full period the emitted temperature was above the threshold %v: %v, "+
			"below it: %v — an alarm needs both", WidgetlabAlarmThreshold, above, below)
	}
}

// ---- The manifest -----------------------------------------------------------

func TestWidgetlabManifestIsValid(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("widgetlab manifest is invalid: %v", err)
	}
}

// The counts are not arbitrary: each exists so a specific widget has something to
// show. Asserting the REASON rather than the number means a future resize has to
// keep the property, not just update a literal.
func TestWidgetlabTopologyGivesEveryChannelSomethingToShow(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()

	if len(m.Areas) < 2 {
		t.Errorf("widgetlab declares %d zones; the entity-selector's root slot needs at least 2 "+
			"candidates or a rebind is unobservable", len(m.Areas))
	}

	// Counted PER DEVICE TYPE, because the properties below are about the nominal
	// sensors the gallery binds. Counting all devices together let the edge sensors
	// satisfy them on the nominal population's behalf: dropping DistributeAcross
	// from the nominal population entirely — so no gallery device sits in any zone
	// — passed an earlier version of this test.
	perZone := map[string]map[string]int{}
	byType := map[string]int{}
	for _, d := range m.Expand(m.Seed) {
		byType[d.DeviceTypeToken]++
		for _, a := range d.Assignments {
			if a.TargetType != "area" {
				continue
			}
			if perZone[d.DeviceTypeToken] == nil {
				perZone[d.DeviceTypeToken] = map[string]int{}
			}
			perZone[d.DeviceTypeToken][a.TargetToken]++
		}
	}

	if byType[WidgetlabDeviceTypeToken] == 0 {
		t.Fatal("no nominal sensors: the gallery board has nothing to bind")
	}
	if byType[WidgetlabEdgeDeviceTypeToken] == 0 {
		t.Fatal("no edge sensors: the stress board has nothing to bind")
	}

	nominal := perZone[WidgetlabDeviceTypeToken]
	// EVERY zone must hold a nominal sensor. A zone the root selector offers but
	// which contains nothing renders an empty board on rebind — the selector
	// appears to work and demonstrates the opposite of what it is there to show.
	for _, zone := range m.Areas {
		if nominal[zone.Token] == 0 {
			t.Errorf("zone %q holds no nominal sensor, so selecting it shows an empty board",
				zone.Token)
		}
	}
	// A scoped child slot picks a device WITHIN a zone, so some zone must hold more
	// than one nominal sensor for that choice to exist at all.
	crowded := false
	for _, n := range nominal {
		if n > 1 {
			crowded = true
		}
	}
	if !crowded {
		t.Error("every zone holds at most one nominal sensor, so the entity-selector's scoped " +
			"child slot has no choice to offer")
	}
	// The customer anchor is what makes a tenant-wide alarm widget scope to
	// something; without it the manifest's customer is provisioned and unused.
	for _, d := range m.Expand(m.Seed) {
		if !slices.ContainsFunc(d.Assignments, func(a Assignment) bool { return a.TargetType == "customer" }) {
			t.Fatalf("device %q carries no customer assignment, so its events reach no customer anchor",
				d.Token)
		}
	}
}

// The profile must carry the metric the sweep drives and the rule alarms on —
// otherwise the rule references a metric no device reports, and it silently never
// fires against a topology that looks correct.
func TestWidgetlabProfileDeclaresTheSweptMetric(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	for _, p := range m.Profiles {
		if p.Token != WidgetlabProfileToken {
			continue
		}
		for _, metric := range p.Metrics {
			if metric.Key == WidgetlabTemperatureKey {
				return
			}
		}
		t.Fatalf("profile %q declares no %q metric, but the sweep and the alarm threshold are "+
			"defined in terms of it", p.Token, WidgetlabTemperatureKey)
	}
	t.Fatalf("manifest declares no profile %q", WidgetlabProfileToken)
}

// Both device types resolve to the one profile: they differ in behaviour, not in
// what they can report, and a widget bound to either reads the same vocabulary.
func TestWidgetlabDeviceTypesShareOneProfile(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	for _, dt := range m.DeviceTypes {
		if dt.ProfileToken != WidgetlabProfileToken {
			t.Errorf("device type %q resolves to profile %q, not the shared %q",
				dt.Token, dt.ProfileToken, WidgetlabProfileToken)
		}
	}
}

// ---- The two manifest-id lists must agree -----------------------------------

// dcctl mirrors this registry's ids as a literal list (backend/cli/sim/record.go)
// because it deliberately never imports the dc-simulator module. That mirror is a
// VALIDATION GATE — `dcctl sim create --manifest` refuses an id missing from it —
// so a scenario absent there is unreachable through the only supported
// provisioning path, however correctly it is registered here.
//
// Nothing enforced the agreement, and it had already drifted: the list still read
// {devicepulse, buildingpulse} after a third scenario was registered. Since a
// build-time dependency is ruled out by design in both directions, the check reads
// dcctl's source. It is deliberately in THIS module: a scenario is added here, so
// this is where forgetting happens.
func TestDcctlKnowsEveryRegisteredScenario(t *testing.T) {
	const dcctlRecord = "../../../cli/sim/record.go"
	source, err := os.ReadFile(dcctlRecord)
	if err != nil {
		t.Fatalf("read %s: %v (if dcctl's sim record moved, re-point this check rather than "+
			"deleting it — it is the only thing keeping the two id lists in step)", dcctlRecord, err)
	}

	declaration := regexp.MustCompile(`KnownManifestIds\s*=\s*\[\]string\{([^}]*)\}`).FindSubmatch(source)
	if declaration == nil {
		t.Fatalf("could not find KnownManifestIds in %s; the check cannot see the list it is "+
			"comparing against, so it would pass without asserting anything", dcctlRecord)
	}
	var known []string
	for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(string(declaration[1]), -1) {
		known = append(known, quoted[1])
	}
	// Negative control: an expression that parsed to nothing would make every
	// comparison below vacuous.
	if len(known) == 0 {
		t.Fatalf("parsed no ids out of dcctl's KnownManifestIds; the pattern has rotted")
	}

	sort.Strings(known)
	if got, want := known, ManifestIds(); !slices.Equal(got, want) {
		t.Errorf("dcctl's KnownManifestIds is %v but the registry has %v.\n"+
			"An id here and not there is REFUSED by `dcctl sim create --manifest`; an id "+
			"there and not here is accepted by dcctl and then fails at sim start.", got, want)
	}
}

// Constructing a driver directly with a load its manifest cannot honour is a
// programming error, and resize's contract is to panic on it rather than quietly
// return a topology nobody asked for. widgetlab reaches that contract through the
// FixedTopology refusal; before it was routed through resize, it was the one
// scenario that silently ignored an illegal device count instead.
func TestWidgetlabPanicsOnADirectlyConstructedIllegalLoad(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewWidgetlab with a device count returned a manifest instead of panicking, " +
				"so a misuse NewSim would have refused passes silently here")
		}
	}()
	_ = NewWidgetlab(1, Load{DeviceCount: 500}).Manifest()
}
