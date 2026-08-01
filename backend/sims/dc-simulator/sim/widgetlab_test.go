// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
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

// ---- The rule and the command are read from two sides ------------------------
//
// The rule's predicate is authored here and the curve that must cross it is
// generated here, so they share constants. The COMMAND has the same shape one step
// later: L2's command-button bakes a commandName and a parameterSchema into its
// widget options, and a button naming a command the profile's published vocabulary
// does not contain looks configured, sends, and fails at the delivery boundary.
//
// These read the authored artifacts BACK — decoding the JSON rather than trusting
// the builder — so a literal typed into the definition in place of a constant is a
// failure rather than a coincidence that happens to agree today.

func widgetlabProfile(t *testing.T) ProfileSpec {
	t.Helper()
	for _, p := range NewWidgetlab(1, Load{}).Manifest().Profiles {
		if p.Token == WidgetlabProfileToken {
			return p
		}
	}
	t.Fatalf("manifest declares no profile %q", WidgetlabProfileToken)
	return ProfileSpec{}
}

func TestWidgetlabRuleIsAuthoredFromTheSweepConstants(t *testing.T) {
	rules := widgetlabProfile(t).DetectionRules
	if len(rules) != 1 {
		t.Fatalf("expected exactly one detection rule, got %d", len(rules))
	}

	var def struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		When     struct {
			Metric    string  `json:"metric"`
			Op        string  `json:"op"`
			Threshold float64 `json:"threshold"`
		} `json:"when"`
		Actions []struct {
			Type       string `json:"type"`
			RaiseAlarm struct {
				AlarmKey string `json:"alarmKey"`
			} `json:"raiseAlarm"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(rules[0].Definition), &def); err != nil {
		t.Fatalf("rule definition is not decodable: %v", err)
	}

	// The number the whole alarm channel turns on. A literal here that drifted from
	// the sweep would leave every constant-level test green and raise nothing.
	if def.When.Threshold != WidgetlabAlarmThreshold {
		t.Errorf("rule fires above %v but the sweep is designed around %v — the two halves of "+
			"the threshold decision have drifted", def.When.Threshold, WidgetlabAlarmThreshold)
	}
	if def.When.Metric != WidgetlabTemperatureKey {
		t.Errorf("rule reads metric %q, but %q is the metric the sweep drives",
			def.When.Metric, WidgetlabTemperatureKey)
	}
	// "gt" is what makes crossing UPWARD the raise. With "lt" the sweep would still
	// cross the threshold twice a period and the alarm would be inverted.
	if def.When.Op != "gt" {
		t.Errorf("rule operator is %q, not \"gt\" — the alarm would be inverted", def.When.Op)
	}
	// A raiseAlarm action requires a non-empty severity; without one the rule is
	// rejected at publish, which a create-or-get provisioner reports as a failure
	// only if someone reads the log.
	if def.Severity == "" {
		t.Error("rule declares no severity, but a raiseAlarm action requires one")
	}
	if len(def.Actions) != 1 || def.Actions[0].Type != "raiseAlarm" {
		t.Fatalf("expected a single raiseAlarm action, got %+v", def.Actions)
	}
	if def.Actions[0].RaiseAlarm.AlarmKey != WidgetlabAlarmKey {
		t.Errorf("rule raises alarm key %q, not the declared %q — the alarm widgets filter on "+
			"the constant", def.Actions[0].RaiseAlarm.AlarmKey, WidgetlabAlarmKey)
	}
	if !rules[0].Enabled {
		t.Error("rule is disabled: publish-time validation only gates ENABLED rules, so a " +
			"disabled one is published unchecked and never fires")
	}
}

func TestWidgetlabCommandExercisesMoreThanOneParameterType(t *testing.T) {
	commands := widgetlabProfile(t).Commands
	if len(commands) != 1 {
		t.Fatalf("expected exactly one command definition, got %d", len(commands))
	}
	if commands[0].CommandKey != WidgetlabCommandKey {
		t.Errorf("command definition declares key %q, not the constant %q the widget will bake",
			commands[0].CommandKey, WidgetlabCommandKey)
	}

	var params []struct {
		Name     string   `json:"name"`
		DataType string   `json:"dataType"`
		Required bool     `json:"required"`
		Enum     []string `json:"enum"`
	}
	if err := json.Unmarshal([]byte(commands[0].ParameterSchema), &params); err != nil {
		t.Fatalf("parameterSchema is not a decodable descriptor array: %v", err)
	}
	// The spec asks for at least two parameters of DIFFERENT types, because the
	// console's form branches on type: a one-parameter command leaves most of the
	// widget the board exists to show unexercised.
	if len(params) < 2 {
		t.Fatalf("command declares %d parameter(s); the form needs at least 2 to be exercised",
			len(params))
	}
	types := map[string]bool{}
	for _, p := range params {
		if p.Name == "" {
			t.Error("a parameter has no name, and parseParameterSchema drops a nameless " +
				"descriptor silently — the form would render one field fewer")
		}
		types[p.DataType] = true
	}
	if len(types) < 2 {
		t.Errorf("all %d parameters share a data type (%v); the form's branches stay unexercised",
			len(params), types)
	}
}

// ---- Validate rejects the ways a rule silently never fires -------------------

func TestValidateRejectsARuleReadingAnUndeclaredMetric(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	for i := range m.Profiles {
		for j := range m.Profiles[i].DetectionRules {
			m.Profiles[i].DetectionRules[j].Metric = "not_reported"
		}
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("a rule reading a metric no device reports was accepted; it would publish " +
			"cleanly and never fire")
	}
	if !strings.Contains(err.Error(), "not_reported") {
		t.Errorf("refusal did not name the offending metric: %v", err)
	}
}

func TestValidateRejectsMalformedRuleAndCommandJSON(t *testing.T) {
	base := NewWidgetlab(1, Load{}).Manifest()

	broken := base
	broken.Profiles = append([]ProfileSpec(nil), base.Profiles...)
	broken.Profiles[0].DetectionRules = []DetectionRuleSpec{{
		Token: "wl-bad", Name: "bad", Definition: "{not json", Metric: WidgetlabTemperatureKey,
	}}
	if err := broken.Validate(); err == nil {
		t.Error("a rule definition that is not JSON was accepted")
	}

	broken = base
	broken.Profiles = append([]ProfileSpec(nil), base.Profiles...)
	broken.Profiles[0].Commands = []CommandSpec{{
		Token: "wl-bad-cmd", CommandKey: "x", ParameterSchema: "[{name:1}]",
	}}
	if err := broken.Validate(); err == nil {
		t.Error("a parameterSchema that is not JSON was accepted; parseParameterSchema would " +
			"swallow it and render a bare Send button")
	}

	broken = base
	broken.Profiles = append([]ProfileSpec(nil), base.Profiles...)
	broken.Profiles[0].Commands = []CommandSpec{{Token: "wl-nokey-cmd", CommandKey: ""}}
	if err := broken.Validate(); err == nil {
		t.Error("a command with no commandKey was accepted")
	}
}

// DetectionRuleSpec.Metric is a hand-written copy of a value that also lives
// inside the opaque definition. Validate checks the DECLARED one against the
// profile, so a rule whose declared metric is reported and whose predicate reads
// something else would pass every other check and never fire.
func TestValidateRejectsARuleWhoseDeclaredMetricIsNotTheOneItReads(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	m.Profiles = append([]ProfileSpec(nil), m.Profiles...)
	m.Profiles[0].DetectionRules = []DetectionRuleSpec{{
		Token: "wl-mismatch",
		Name:  "mismatch",
		// Declares the metric the profile reports; reads a different one.
		Definition: `{"name":"m","type":"threshold","severity":"MAJOR","when":{"metric":"humidity","op":"gt","threshold":1}}`,
		Metric:     WidgetlabTemperatureKey,
		Enabled:    true,
	}}
	err := m.Validate()
	if err == nil {
		t.Fatal("a rule declaring one metric and reading another was accepted")
	}
	if !strings.Contains(err.Error(), "humidity") {
		t.Errorf("refusal did not name the metric the predicate actually reads: %v", err)
	}
}

// An AGGREGATE rule names its metric at the TOP level, not in a leaf comparison.
// Reading only `when.metric` left every such rule unchecked: it provisions cleanly
// against a profile that never reports the metric and never fires — the exact seam
// the declared-metric check exists to close.
func TestValidateReadsTheMetricOfAnAggregateRule(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	m.Profiles = append([]ProfileSpec(nil), m.Profiles...)
	m.Profiles[0].DetectionRules = []DetectionRuleSpec{{
		Token: "wl-aggregate",
		Name:  "aggregate",
		// Declares a metric the profile reports; its value selector reads one it does not.
		Definition: `{"name":"a","type":"aggregate","metric":"not_reported","agg":"avg","op":"gt","threshold":1}`,
		Metric:     WidgetlabTemperatureKey,
		Enabled:    true,
	}}
	err := m.Validate()
	if err == nil {
		t.Fatal("an aggregate rule whose value selector reads an unreported metric was accepted")
	}
	if !strings.Contains(err.Error(), "not_reported") {
		t.Errorf("refusal did not name the metric the rule actually reads: %v", err)
	}
}

// The counterweight: a rule that legitimately names NO metric must be left alone,
// not rejected. "No opinion" has to mean no opinion, or a connectivity rule —
// leaf-less and config-less by design, the presence event itself being the signal —
// becomes unauthorable.
//
// An earlier version of this test used a `composite` rule with an `all` array.
// There is no such rule type and no such field: the real decoder rejects it with
// DisallowUnknownFields, so the counterweight certified tolerance of a rule that
// cannot exist while the shapes that DO lack a leaf metric went untested.
func TestValidateAcceptsARuleThatNamesNoMetric(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	m.Profiles = append([]ProfileSpec(nil), m.Profiles...)
	m.Profiles[0].DetectionRules = []DetectionRuleSpec{{
		Token:      "wl-connectivity",
		Name:       "offline",
		Definition: `{"name":"offline","type":"connectivity","severity":"major"}`,
		Metric:     WidgetlabTemperatureKey,
		Enabled:    true,
	}}
	if err := m.Validate(); err != nil {
		t.Errorf("a rule that names no metric was rejected: %v", err)
	}
}

// ---- The rule severity vocabulary is not the alarm wire vocabulary -----------
//
// The rule compiler's severities are LOWERCASE; the durable alarm row's tier is
// the uppercase form the raise-alarm consumer produces, and it is what an alarm
// widget's `severity` filter is written in. Authoring a rule with the wire form is
// rejected by the compiler — so with the publish gate wired the scenario cannot
// bootstrap, and with it unwired the rule is dropped at load with only a log line
// and the alarm never fires.
//
// This is not hypothetical: this scenario shipped "MAJOR" as its authoring
// severity, having collapsed the two constants loadtest/detection.go deliberately
// keeps apart. So the vocabulary is read out of event-processing's own source
// rather than mirrored and hoped over — dc-simulator cannot import that module,
// and a comment asking the next person to remember is what failed the first time.
func TestWidgetlabSeverityIsTheRuleAuthoringVocabulary(t *testing.T) {
	const schema = "../../../services/event-processing/internal/rules/schema.go"
	source, err := os.ReadFile(schema)
	if err != nil {
		t.Fatalf("read %s: %v (if the rules schema moved, re-point this check rather than "+
			"deleting it — it is the only thing tying this constant to the compiler that "+
			"accepts or rejects it)", schema, err)
	}

	// 🔴 Read the ACCEPTANCE, not the spelling.
	//
	// An earlier version parsed the constant DECLARATIONS. That is the wrong surface:
	// the compiler accepts or rejects a severity through Severity.Valid(), a switch
	// over those identifiers. A severity dropped from the switch while its declaration
	// survives — kept for old data, or for another caller — passes a declaration scan
	// while the compiler rejects the rule, which is precisely the outcome this gate
	// exists to prevent. So both are read, and a severity counts only if it appears in
	// both: declared with a string value, AND named by the switch that accepts it.
	clean := stripGoComments(string(source))
	declared := map[string]string{}
	for _, m := range regexp.MustCompile(`(Severity\w+)\s+Severity\s*=\s*"([^"]+)"`).FindAllStringSubmatch(clean, -1) {
		declared[m[1]] = m[2]
	}
	accepts := regexp.MustCompile(`func \(s Severity\) Valid\(\) bool \{[^}]*case ([^:]+):`).FindStringSubmatch(clean)
	if accepts == nil {
		t.Fatalf("could not find Severity.Valid's accepting case in %s; the gate cannot see what "+
			"the compiler actually accepts and would pass for any value", schema)
	}
	valid := map[string]bool{}
	for _, ident := range strings.Split(accepts[1], ",") {
		if value, ok := declared[strings.TrimSpace(ident)]; ok {
			valid[value] = true
		}
	}
	// Negative control: an expression that matched nothing would make the
	// membership check below accept anything at all.
	if len(valid) == 0 {
		t.Fatalf("parsed no severities out of %s; the pattern has rotted and this test would "+
			"pass for any value", schema)
	}

	if !valid[WidgetlabSeverity] {
		t.Errorf("rule severity %q is not one of the compiler's severities %v — the rule would "+
			"be rejected at publish, or dropped at load and never fire",
			WidgetlabSeverity, sortedKeys(valid))
	}
	// The wire form is the authoring form uppercased, which is exactly what the
	// raise-alarm consumer does. An alarm widget filtering on the wrong case
	// matches nothing and renders an empty table that looks like "no alarms yet".
	if WidgetlabAlarmSeverityWire != strings.ToUpper(WidgetlabSeverity) {
		t.Errorf("alarm wire severity %q is not the uppercase of the authoring severity %q",
			WidgetlabAlarmSeverityWire, WidgetlabSeverity)
	}
	// And the authored rule must actually carry the authoring form.
	if !strings.Contains(widgetlabRuleDefinition(), `"severity":"`+WidgetlabSeverity+`"`) {
		t.Errorf("the authored rule does not carry severity %q", WidgetlabSeverity)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Command and rule tokens are not profile-prefixed the way metric tokens are, so
// two profiles can name the same one. The second profile's reconciler finds the
// token present, matches its content, and does nothing — so that profile publishes
// WITHOUT the command, and no step reports a failure.
func TestValidateRejectsAVersionContentTokenDeclaredTwice(t *testing.T) {
	base := NewWidgetlab(1, Load{}).Manifest()

	// Each duplicate is exercised ALONE. Sharing both tokens at once would let the
	// rule check stand in for the command check, leaving one of them unpinned —
	// which is what happened the first time this was written.
	withSecond := func(second ProfileSpec) SimManifest {
		m := base
		m.Profiles = append(append([]ProfileSpec(nil), base.Profiles...), second)
		return m
	}
	distinctRule := DetectionRuleSpec{
		Token: "wl-second-rule", Name: "Second",
		Definition: `{"name":"second"}`, Metric: WidgetlabTemperatureKey, Enabled: true,
	}
	distinctCommand := CommandSpec{Token: "wl-second-cmd", CommandKey: "other", Name: "Other"}

	dupCommandOnly := base.Profiles[0]
	dupCommandOnly.Token = "wl-second-profile"
	dupCommandOnly.DetectionRules = []DetectionRuleSpec{distinctRule}
	if err := withSecond(dupCommandOnly).Validate(); err == nil {
		t.Error("two profiles declaring the same COMMAND token were accepted; the second " +
			"would publish without it")
	}

	dupRuleOnly := base.Profiles[0]
	dupRuleOnly.Token = "wl-second-profile"
	dupRuleOnly.Commands = []CommandSpec{distinctCommand}
	if err := withSecond(dupRuleOnly).Validate(); err == nil {
		t.Error("two profiles declaring the same RULE token were accepted; the second " +
			"would publish without it")
	}

	// The counterweight: distinct tokens on two profiles are legitimate.
	ok := base.Profiles[0]
	ok.Token = "wl-second-profile"
	ok.Commands = []CommandSpec{distinctCommand}
	ok.DetectionRules = []DetectionRuleSpec{distinctRule}
	if err := withSecond(ok).Validate(); err != nil {
		t.Errorf("two profiles with distinct command and rule tokens were rejected: %v", err)
	}
}

// A parameterSchema that is valid JSON but not an ARRAY is rejected by the
// platform at create; naming the manifest field here is the difference between a
// message a reader can act on and one from a GraphQL round-trip.
func TestValidateRejectsANonArrayParameterSchema(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	m.Profiles = append([]ProfileSpec(nil), m.Profiles...)
	m.Profiles[0].Commands = []CommandSpec{{
		Token: "wl-obj-cmd", CommandKey: "x", ParameterSchema: `{"name":"target"}`,
	}}
	if err := m.Validate(); err == nil {
		t.Error("an object parameterSchema was accepted; the platform stores only an array")
	}
}

// ---- The boards ---------------------------------------------------------------

// A parsed board, decoded back from the JSON the builders emit. Reading the
// artifact rather than the builder is the point: these assert what the platform
// will actually store, not what the Go structs looked like on the way there.
type parsedBoard struct {
	SchemaVersion int    `json:"schemaVersion"`
	Title         string `json:"title"`
	Canvas        struct {
		Grid struct {
			Columns int `json:"columns"`
		} `json:"grid"`
	} `json:"canvas"`
	Slots map[string]struct {
		Type           string `json:"type"`
		DefaultBinding *struct {
			Kind        string `json:"kind"`
			DeviceToken string `json:"deviceToken"`
			Anchor      *struct {
				TargetToken string `json:"targetToken"`
			} `json:"anchor"`
		} `json:"defaultBinding"`
		Scope *struct {
			Parent string `json:"parent"`
		} `json:"scope"`
	} `json:"slots"`
	Widgets []struct {
		Id         string                                              `json:"id"`
		Type       string                                              `json:"type"`
		Layout     map[string]struct{ Col, ColSpan, Row, RowSpan int } `json:"layout"`
		Datasource *struct {
			Kind         string   `json:"kind"`
			Slot         string   `json:"slot"`
			Measurements []string `json:"measurements"`
		} `json:"datasource"`
		Options map[string]any `json:"options"`
	} `json:"widgets"`
}

func widgetlabBoard(t *testing.T, token string) parsedBoard {
	t.Helper()
	for _, d := range NewWidgetlab(1, Load{}).Manifest().Dashboards {
		if d.Token != token {
			continue
		}
		var board parsedBoard
		if err := json.Unmarshal([]byte(d.Definition), &board); err != nil {
			t.Fatalf("board %q is not decodable: %v", token, err)
		}
		return board
	}
	t.Fatalf("manifest declares no dashboard %q", token)
	return parsedBoard{}
}

// 🔑 THE COVERAGE GATE. The gallery is the catalog, so it must carry every widget
// type there is — and Go does not know what those are. The list lives in
// @devicechain/dashboards, so it is read from there rather than mirrored: a
// mirrored list is the thing that goes stale the day a widget is added, which is
// the exact event this scenario exists to keep up with.
//
// The TypeScript side will assert this again over the real parser (that is what the
// fixture gate is for). This is the same claim held one lane earlier, so a missing
// widget fails in the lane that added it.
func TestWidgetlabGalleryCarriesEveryWidgetType(t *testing.T) {
	const types = "../../../../frontend/packages/dashboards/src/types.ts"
	source, err := os.ReadFile(types)
	if err != nil {
		t.Fatalf("read %s: %v (if WIDGET_TYPES moved, re-point this check rather than deleting "+
			"it — it is what keeps the gallery a complete catalog)", types, err)
	}
	block := regexp.MustCompile(`WIDGET_TYPES\s*=\s*\[([^\]]*)\]`).FindSubmatch(source)
	if block == nil {
		t.Fatalf("could not find WIDGET_TYPES in %s; the check cannot see the list it compares "+
			"against and would pass without asserting anything", types)
	}
	var declared []string
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(string(block[1]), -1) {
		declared = append(declared, m[1])
	}
	// Negative control: an empty parse would make the coverage check vacuous.
	if len(declared) < 2 {
		t.Fatalf("parsed %d widget types out of %s; the pattern has rotted", len(declared), types)
	}

	present := map[string]int{}
	for _, w := range widgetlabBoard(t, WidgetlabGalleryDashboardToken).Widgets {
		present[w.Type]++
	}
	// At least once, not exactly once. A second instance of a type is sometimes the
	// only way to show what it does: the two entity-selectors are the root context
	// picker and the scoped child picker, and a rebind is only observable with both.
	for _, widgetType := range declared {
		if present[widgetType] == 0 {
			t.Errorf("the gallery carries no %q widget, so the catalog is missing an entry a "+
				"dashboard author can choose", widgetType)
		}
	}
	for widgetType := range present {
		if !slices.Contains(declared, widgetType) {
			t.Errorf("the gallery carries a %q widget, which is not a declared widget type — the "+
				"parser rejects an unknown type and the whole board fails to open", widgetType)
		}
	}
}

// Both boards, checked for the structural properties a reader would notice: unique
// ids, a base layout box, a datasource pointing at a declared slot, and no slot
// declared that nothing uses.
func TestWidgetlabBoardsAreStructurallySound(t *testing.T) {
	for _, token := range []string{WidgetlabGalleryDashboardToken, WidgetlabStressDashboardToken} {
		t.Run(token, func(t *testing.T) {
			board := widgetlabBoard(t, token)
			if board.SchemaVersion != 1 {
				t.Errorf("schemaVersion is %d, not 1", board.SchemaVersion)
			}
			if len(board.Widgets) == 0 {
				t.Fatal("board carries no widgets")
			}

			ids := map[string]bool{}
			usedSlots := map[string]bool{}
			for _, w := range board.Widgets {
				if ids[w.Id] {
					t.Errorf("duplicate widget id %q; React keys collide and one widget replaces "+
						"the other", w.Id)
				}
				ids[w.Id] = true

				// parseDashboardDefinition throws on a missing layout.base, so a widget
				// without one takes the entire board down rather than rendering badly.
				if _, ok := w.Layout["base"]; !ok {
					t.Errorf("widget %q has no base layout box; the parser rejects the board", w.Id)
				}
				if w.Datasource == nil {
					continue
				}
				if w.Datasource.Kind != "slot" {
					t.Errorf("widget %q binds a %q datasource; these boards bind slots so a "+
						"selector can re-point them", w.Id, w.Datasource.Kind)
				}
				if _, ok := board.Slots[w.Datasource.Slot]; !ok {
					t.Errorf("widget %q binds slot %q, which the board does not declare — the "+
						"hub resolves nothing and the widget renders empty",
						w.Id, w.Datasource.Slot)
				}
				usedSlots[w.Datasource.Slot] = true
				// A nil measurements array marshals as null, which the parser coerces to
				// [] — meaning EVERY measurement. It does not drop the datasource (an
				// earlier version of this comment claimed it did); it silently widens
				// the subscription, so a chart meant to draw two series draws all of
				// them.
				if w.Datasource.Measurements == nil {
					t.Errorf("widget %q has a null measurements array, which the parser reads as "+
						"\"every measurement\" — a wider subscription than was authored", w.Id)
				}
			}

			// A selection widget uses a slot without carrying a datasource, so count
			// those too before calling a slot dead.
			for _, w := range board.Widgets {
				if target, ok := w.Options["selectionTarget"].(string); ok {
					usedSlots[target] = true
				}
			}
			for name, slot := range board.Slots {
				if !usedSlots[name] {
					t.Errorf("slot %q is declared but nothing references it", name)
				}
				if slot.DefaultBinding == nil {
					t.Errorf("slot %q has no default binding, so it opens unbound and every "+
						"widget on it renders a placeholder", name)
				}
				if slot.Scope != nil {
					if _, ok := board.Slots[slot.Scope.Parent]; !ok {
						t.Errorf("slot %q is scoped to %q, which the board does not declare",
							name, slot.Scope.Parent)
					}
				}
			}
		})
	}
}

// No widget may sit on top of another. Overlap is legal in the model (z-order
// exists) and wrong on a board meant to be read.
//
// A NEGATIVE coordinate has to be rejected rather than merely mapped, because the
// parser CLAMPS col and row to zero: two widgets authored at col -6 and col 0 both
// arrive at column 0, a pixel-perfect overlap — while a gate that marked the
// authored cells would tick off phantom cells at -6..-1 and see no collision. So the
// coordinates are checked against the parser's domain before they are marked.
//
// Both boards, not just the gallery: the stress board is meant to look hostile, not
// to be unreadable, and nothing else checks its layout.
func TestWidgetlabBoardWidgetsDoNotOverlap(t *testing.T) {
	for _, token := range []string{WidgetlabGalleryDashboardToken, WidgetlabStressDashboardToken} {
		t.Run(token, func(t *testing.T) {
			type cell struct{ col, row int }
			occupied := map[cell]string{}
			for _, w := range widgetlabBoard(t, token).Widgets {
				b := w.Layout["base"]
				if b.Col < 0 || b.Row < 0 {
					t.Errorf("widget %q is placed at (%d,%d); the parser clamps a negative "+
						"coordinate to zero, so it would land on top of whatever is there",
						w.Id, b.Col, b.Row)
					continue
				}
				if b.ColSpan <= 0 || b.RowSpan <= 0 {
					t.Errorf("widget %q spans %dx%d; it would render as a zero-size box",
						w.Id, b.ColSpan, b.RowSpan)
					continue
				}
				if b.Col+b.ColSpan > dashboardGridColumns {
					t.Errorf("widget %q runs from column %d for %d, past the %d-column grid",
						w.Id, b.Col, b.ColSpan, dashboardGridColumns)
				}
				for c := b.Col; c < b.Col+b.ColSpan; c++ {
					for r := b.Row; r < b.Row+b.RowSpan; r++ {
						if other, taken := occupied[cell{c, r}]; taken {
							t.Errorf("widgets %q and %q both occupy cell (%d,%d)", other, w.Id, c, r)
						}
						occupied[cell{c, r}] = w.Id
					}
				}
			}
		})
	}
}

// The board's options and the profile's authored content are two sides of one
// decision, exactly like the sweep and the threshold. Each of these would render a
// widget that looks configured and shows nothing.
func TestWidgetlabBoardOptionsAgreeWithWhatIsProvisioned(t *testing.T) {
	board := widgetlabBoard(t, WidgetlabGalleryDashboardToken)
	// Keyed by ID, and every instance of a type is checked. Keying by TYPE let the
	// last instance win, so the day a board carries a second gauge or command-button
	// — which the coverage gate's at-least-once policy invites — the earlier one's
	// options would go unasserted with every test still green.
	each := func(widgetType string, check func(id string, opts map[string]any)) {
		seen := 0
		for _, w := range board.Widgets {
			if w.Type != widgetType {
				continue
			}
			seen++
			check(w.Id, w.Options)
		}
		if seen == 0 {
			t.Fatalf("the gallery carries no %q widget, so this check asserted nothing", widgetType)
		}
	}

	// Every command-button must name the command the profile actually publishes.
	command := widgetlabProfile(t).Commands[0]
	each("command-button", func(id string, o map[string]any) {
		if got := o["commandName"]; got != command.CommandKey {
			t.Errorf("%s issues %v but the profile publishes %q; the button would send and "+
				"fail at the delivery boundary", id, got, command.CommandKey)
		}
		if got := o["parameterSchema"]; got != command.ParameterSchema {
			t.Errorf("%s bakes a parameterSchema the profile does not declare; the form would "+
				"render fields the device does not accept", id)
		}
	})

	// An alarm widget filtering by severity must use the WIRE form — the authoring
	// severity uppercased by the raise-alarm consumer. The lowercase form matches no
	// alarm row and renders a permanent zero that reads as a quiet system.
	each("alarm-count", func(id string, o map[string]any) {
		if got := o["severity"]; got != WidgetlabAlarmSeverityWire {
			t.Errorf("%s filters severity %v, not the wire form %q a raised alarm carries",
				id, got, WidgetlabAlarmSeverityWire)
		}
	})

	// A gauge's scale is the sweep's own bounds, so the needle visits both ends, and
	// it displays the swept metric rather than tracking a flat line against it.
	each("gauge", func(id string, o map[string]any) {
		if got := o["min"]; got != WidgetlabSweepMin {
			t.Errorf("%s min is %v, not the sweep's %v", id, got, WidgetlabSweepMin)
		}
		if got := o["max"]; got != WidgetlabSweepMax {
			t.Errorf("%s max is %v, not the sweep's %v", id, got, WidgetlabSweepMax)
		}
		if got := o["measurement"]; got != WidgetlabTemperatureKey {
			t.Errorf("%s displays %v, not the swept metric %q", id, got, WidgetlabTemperatureKey)
		}
	})

	// The alarm-table must NOT pin a state: filtering to ACTIVE makes a resolve
	// render as a vanishing row, so the cleared state never appears on the catalog.
	each("alarm-table", func(id string, o map[string]any) {
		if state, pinned := o["state"]; pinned {
			t.Errorf("%s filters state %v, so a cleared alarm leaves the table and the resolve "+
				"path — half of what this widget draws — is never shown", id, state)
		}
	})
}

// Each board binds the device population it is about. The stress board bound to a
// nominal sensor would show well-behaved data under a banner announcing hostile
// input, which is worse than showing nothing.
func TestWidgetlabBoardsBindTheirOwnPopulations(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	byToken := map[string]string{}
	for _, d := range m.Expand(m.Seed) {
		byToken[d.Token] = d.DeviceTypeToken
	}

	cases := []struct{ board, wantType string }{
		{WidgetlabGalleryDashboardToken, WidgetlabDeviceTypeToken},
		{WidgetlabStressDashboardToken, WidgetlabEdgeDeviceTypeToken},
	}
	for _, c := range cases {
		bound := 0
		for name, slot := range widgetlabBoard(t, c.board).Slots {
			if slot.DefaultBinding == nil || slot.DefaultBinding.Kind != "device" {
				continue
			}
			bound++
			token := slot.DefaultBinding.DeviceToken
			if got, ok := byToken[token]; !ok {
				t.Errorf("%s: slot %q binds device %q, which the topology does not contain",
					c.board, name, token)
			} else if got != c.wantType {
				t.Errorf("%s: slot %q binds a %q device; this board is about %q devices",
					c.board, name, got, c.wantType)
			}
		}
		if bound == 0 {
			t.Errorf("%s binds no device slot at all, so this test asserted nothing", c.board)
		}
	}
}

// The stress board must say it is deliberately pathological. It is reachable by
// anyone who bootstraps the scenario, and a board that looks broken without saying
// why is a support ticket.
func TestWidgetlabStressBoardSaysItIsDeliberatelyPathological(t *testing.T) {
	var announced bool
	for _, w := range widgetlabBoard(t, WidgetlabStressDashboardToken).Widgets {
		if w.Type != "label" {
			continue
		}
		if text, ok := w.Options["text"].(string); ok && strings.Contains(strings.ToLower(text), "pathological") {
			announced = true
		}
	}
	if !announced {
		t.Error("the stress board carries no label announcing that its data is deliberately " +
			"hostile; a viewer would read it as a broken dashboard")
	}
}

// ---- The pathological lane ----------------------------------------------------

// Every declared extreme must actually be reached within one cycle. A cycle that
// silently skipped one would leave a widget's worst case unexercised while the
// stress board announces it — which is the board lying about itself.
func TestWidgetlabEdgeCycleVisitsEveryExtreme(t *testing.T) {
	// Checked FIRST, and with Fatalf, because a divergence here is what makes the
	// loop below index out of range — a panic that aborts the whole test binary
	// instead of naming the one-line edit that caused it.
	if want := int64(widgetlabEdgeNominalPhases + len(widgetlabEdgeExtremes)); widgetlabEdgeCyclePhases != want {
		t.Fatalf("the cycle runs %d steps but %d nominal steps plus %d extremes need %d; "+
			"derive the length from the list rather than fixing it",
			widgetlabEdgeCyclePhases, widgetlabEdgeNominalPhases, len(widgetlabEdgeExtremes), want)
	}
	// Negative control: an empty extremes list would make the loop assert nothing.
	if len(widgetlabEdgeExtremes) == 0 {
		t.Fatal("no extremes are declared, so this test asserts nothing")
	}

	seen := map[float64]bool{}
	// Two full sweep periods, so a cycle that only lines up on some periods is
	// still caught.
	for tick := int64(1); tick <= 2*WidgetlabSweepTicks; tick++ {
		values := widgetlabEdgeMetrics(edgeExtremes, tick)
		if values == nil {
			t.Fatalf("tick %d: the extremes device went silent; that is another device's job", tick)
		}
		seen[values[WidgetlabTemperatureKey]] = true
	}
	for _, want := range widgetlabEdgeExtremes {
		if !seen[want] {
			t.Errorf("the cycle never emits %v, so the case it represents is never shown", want)
		}
	}
}

// 🔴 The boundary case the rule's operator turns on. The rule fires on `gt`, which
// is STRICT, so a device sitting exactly ON the threshold must not raise. Nothing
// else in the scenario tests that boundary, and a rule that used `gte` would look
// correct in every other test.
func TestWidgetlabEdgeCycleSitsExactlyOnTheThresholdWithoutRaising(t *testing.T) {
	if !slices.Contains(widgetlabEdgeExtremes, float64(WidgetlabAlarmThreshold)) {
		t.Fatal("the cycle never sits exactly on the alarm threshold, so the strictness of the " +
			"rule's comparison is untested")
	}
	// The rule's own operator, read back out of the authored definition.
	var def struct {
		When struct {
			Op        string  `json:"op"`
			Threshold float64 `json:"threshold"`
		} `json:"when"`
	}
	if err := json.Unmarshal([]byte(widgetlabRuleDefinition()), &def); err != nil {
		t.Fatalf("rule definition is not decodable: %v", err)
	}
	if def.When.Op != "gt" {
		t.Errorf("the rule compares with %q; the at-threshold case only means something under "+
			"a strict comparison", def.When.Op)
	}
	if !(WidgetlabAlarmThreshold > def.When.Threshold) && WidgetlabAlarmThreshold != def.When.Threshold {
		t.Errorf("the at-threshold value %v is not the rule's threshold %v",
			WidgetlabAlarmThreshold, def.When.Threshold)
	}
}

// The silent device must actually stop emitting — nil, not an empty measurement,
// because EmitAll skips a device only on nil and an empty map would post a
// measurement carrying nothing, which is a device reporting that it has nothing to
// say rather than a device that is gone.
func TestWidgetlabEdgeSilenceIsRealSilence(t *testing.T) {
	silent, reporting := 0, 0
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		switch values := widgetlabEdgeMetrics(edgeSilent, tick); {
		case values == nil:
			silent++
		case len(values) == 0:
			t.Fatalf("tick %d returned an empty map; EmitAll would post a measurement with no "+
				"values, which is not silence", tick)
		default:
			reporting++
		}
	}
	if silent == 0 {
		t.Error("the silent device never goes silent")
	}
	if reporting == 0 {
		t.Error("the silent device never reports, so a viewer sees no transition into silence")
	}

	// CONTIGUOUS, not a flicker. A device alternating on/off every tick satisfies
	// "some ticks silent, some reporting" while producing no visible gap at all: the
	// chart just gets sparser, the timestamp never goes stale, and an absence rule
	// with a window over one interval never fires. Every observable property the
	// silence exists for depends on the run being unbroken.
	var longest, current int
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		if widgetlabEdgeMetrics(edgeSilent, tick) == nil {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	// Two phase steps' worth, which is what the cycle allots to silence.
	if want := int(2 * widgetlabEdgePhaseTicks); longest < want {
		t.Errorf("the longest unbroken silence is %d ticks, want at least %d — a device that "+
			"flickers produces a sparser chart, not a gap", longest, want)
	}
}

// The partial reporter must drop metrics AND recover them, or a table simply shows
// fewer rows forever and nothing about it reads as pathological.
func TestWidgetlabEdgePartialReporterDropsAndRestoresMetrics(t *testing.T) {
	full := len(widgetlabNominalMetrics(1))
	if full < 3 {
		t.Fatalf("a nominal report carries %d metrics; there is nothing to drop", full)
	}
	counts := map[int]bool{}
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		values := widgetlabEdgeMetrics(edgePartial, tick)
		if values == nil {
			t.Fatalf("tick %d: the partial reporter went silent; that is another device's job", tick)
		}
		counts[len(values)] = true
	}
	if !counts[full] {
		t.Error("the partial reporter never sends a complete report, so the drop has nothing " +
			"to contrast against")
	}
	if len(counts) < 2 {
		t.Error("the partial reporter always sends the same number of metrics, so no table row " +
			"ever appears or disappears")
	}
	// The swept metric is never dropped: it is what the chart, gauge, card and the
	// alarm rule all read, and a device that stopped reporting it would look silent
	// rather than partial.
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		if _, ok := widgetlabEdgeMetrics(edgePartial, tick)[WidgetlabTemperatureKey]; !ok {
			t.Fatalf("tick %d drops the swept metric, so this device is indistinguishable from "+
				"a silent one", tick)
		}
	}
}

// Behaviours are assigned by POSITION among the edge devices, not by parsing a
// token, and every edge device gets one while no nominal device does.
func TestWidgetlabEdgeBehavioursAreAssignedStructurally(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	devices := m.Expand(m.Seed)
	behaviours := widgetlabEdgeBehaviours(devices)

	assigned := map[edgeBehaviour]int{}
	for i, d := range devices {
		behaviour, isEdge := behaviours[i]
		switch d.DeviceTypeToken {
		case WidgetlabEdgeDeviceTypeToken:
			if !isEdge {
				t.Errorf("edge device %q was given no behaviour, so it emits nominal data on the "+
					"board that announces hostile data", d.Token)
				continue
			}
			assigned[behaviour]++
		default:
			if isEdge {
				t.Errorf("nominal device %q was given a pathological behaviour; the gallery would "+
					"show hostile data", d.Token)
			}
		}
	}
	// Every behaviour must be represented, or the stress board announces a case no
	// device produces.
	for _, behaviour := range []edgeBehaviour{edgeExtremes, edgePartial, edgeSilent} {
		if assigned[behaviour] == 0 {
			t.Errorf("no edge device carries behaviour %d, so the case it shows never happens",
				behaviour)
		}
	}
}

// The cycle is pure in the tick, like the sweep: a fixture, a test and a live run
// must agree on what tick N shows.
func TestWidgetlabEdgeCycleIsDeterministicAndTotal(t *testing.T) {
	for _, behaviour := range []edgeBehaviour{edgeExtremes, edgePartial, edgeSilent} {
		for _, tick := range []int64{-1, -91, 0, 1, 7, 1_000_003, math.MaxInt64 - 1} {
			a, b := widgetlabEdgeMetrics(behaviour, tick), widgetlabEdgeMetrics(behaviour, tick)
			if len(a) != len(b) {
				t.Fatalf("behaviour %d at tick %d is not deterministic", behaviour, tick)
			}
			for key, av := range a {
				if b[key] != av {
					t.Fatalf("behaviour %d at tick %d is not deterministic for %q", behaviour, tick, key)
				}
			}
		}
	}
	// The phase index must stay in range for any tick, including negative ones —
	// Go's % keeps the dividend's sign, which would index the extremes slice out of
	// bounds if it were not normalised.
	// These are chosen to actually produce a negative quotient. An earlier list
	// (-1, -1000, MinInt64+1) all happened to give phase 0, so removing the
	// normalisation changed nothing and the test proved the opposite of its claim.
	negatives := []int64{
		-widgetlabEdgePhaseTicks, -3 * widgetlabEdgePhaseTicks,
		-1, -1000, math.MinInt64 + 1,
	}
	for _, tick := range negatives {
		phase := widgetlabEdgePhase(tick)
		if int64(phase) < 0 || int64(phase) >= widgetlabEdgeCyclePhases {
			t.Errorf("tick %d gives phase %d, outside the %d-step cycle",
				tick, phase, widgetlabEdgeCyclePhases)
		}
		// And the value it produces must be indexable, which is what the phase
		// bound actually protects.
		if widgetlabEdgeMetrics(edgeExtremes, tick) == nil {
			t.Errorf("tick %d produced no metrics for the extremes device", tick)
		}
	}
}

// A nominal sensor is never touched by the pathological lane — the gallery is the
// catalog, and hostile data on it would misrepresent every widget it shows.
func TestWidgetlabNominalDevicesStayNominal(t *testing.T) {
	for tick := int64(1); tick <= WidgetlabSweepTicks; tick++ {
		values := widgetlabNominalMetrics(tick)
		temperature := values[WidgetlabTemperatureKey]
		if temperature < WidgetlabSweepMin || temperature > WidgetlabSweepMax {
			t.Fatalf("tick %d: a nominal device reported %v, outside the sweep", tick, temperature)
		}
		if len(values) != 4 {
			t.Fatalf("tick %d: a nominal device reported %d metrics, not the full 4", tick, len(values))
		}
	}
}

// 🔴 The pathological lane has to reach the WIRE, not just the pure functions.
//
// This is the seam that was already open once in this scenario: the sweep and the
// threshold agreed while nothing observed what Tick actually emitted. The generators
// are a second chance to make the same mistake — a Tick that classified devices
// wrongly, or dropped the behaviour map, would leave every test above green while
// every device on the stress board emitted nominal data.
func TestWidgetlabTickDrivesEdgeDevicesDownThePathologicalPath(t *testing.T) {
	var (
		mu    sync.Mutex
		posts = map[string][]map[string]float64{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Device  string `json:"device"`
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		values := map[string]float64{}
		for _, entry := range body.Payload.Entries {
			for key, raw := range entry.Measurements {
				values[key] = parseFloat(t, raw)
			}
		}
		mu.Lock()
		posts[body.Device] = append(posts[body.Device], values)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	// One nominal sensor and the three edge sensors, in Expand's order.
	m := NewWidgetlab(1, Load{}).Manifest()
	var devices []DeviceInstance
	for _, d := range m.Expand(m.Seed) {
		if d.DeviceTypeToken == WidgetlabDeviceTypeToken && len(devices) > 0 {
			continue
		}
		devices = append(devices, d)
	}
	rt := &Runtime{
		Endpoints:  Endpoints{Ingress: srv.URL},
		InstanceId: "dc", Tenant: "acme", HTTPClient: srv.Client(), Devices: devices,
	}

	s := NewWidgetlab(1, Load{})
	for i := 0; i < WidgetlabSweepTicks; i++ {
		if err := s.Tick(context.Background(), rt); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	behaviours := widgetlabEdgeBehaviours(devices)
	for i, d := range devices {
		got := posts[d.Token]
		behaviour, isEdge := behaviours[i]
		if !isEdge {
			if len(got) != WidgetlabSweepTicks {
				t.Errorf("nominal device %q posted %d of %d ticks", d.Token, len(got), WidgetlabSweepTicks)
			}
			for _, values := range got {
				if v := values[WidgetlabTemperatureKey]; v < WidgetlabSweepMin || v > WidgetlabSweepMax {
					t.Fatalf("nominal device %q emitted %v, outside the sweep", d.Token, v)
				}
			}
			continue
		}

		switch behaviour {
		case edgeExtremes:
			var sawExtreme bool
			for _, values := range got {
				if v := values[WidgetlabTemperatureKey]; v < WidgetlabSweepMin || v > WidgetlabSweepMax {
					sawExtreme = true
				}
			}
			if !sawExtreme {
				t.Errorf("device %q is the extremes device but every value it put on the wire was "+
					"inside the nominal sweep", d.Token)
			}
		case edgePartial:
			shapes := map[int]bool{}
			for _, values := range got {
				shapes[len(values)] = true
			}
			if len(shapes) < 2 {
				t.Errorf("device %q is the partial reporter but every measurement it put on the "+
					"wire carried the same metrics", d.Token)
			}
		case edgeSilent:
			if len(got) == 0 {
				t.Errorf("device %q never reported at all; it is the silent device, not an absent one",
					d.Token)
			}
			if len(got) >= WidgetlabSweepTicks {
				t.Errorf("device %q posted %d of %d ticks; it is supposed to go silent for part "+
					"of its cycle", d.Token, len(got), WidgetlabSweepTicks)
			}
		}
	}
}

// 🔴 The stress board's widgets must be bound to the devices that produce the
// pathologies their titles promise.
//
// Nothing pinned this: the board bound the FIRST edge device to every widget, so it
// showed one pathology under three titles — a chart titled "spikes and silence" over
// a device that never goes silent, and a "Partial reports" table over a device that
// never drops a metric. The wire test could not catch it either, because it derived
// its expectations from the same classifier the code uses, so any permutation of the
// assignment passed. This reads the BOARD and the GENERATOR and checks them against
// each other.
func TestWidgetlabStressWidgetsAreBoundToTheBehaviourTheyShow(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	devices := m.Expand(m.Seed)
	behaviourOf := map[string]edgeBehaviour{}
	for i, behaviour := range widgetlabEdgeBehaviours(devices) {
		behaviourOf[devices[i].Token] = behaviour
	}

	board := widgetlabBoard(t, WidgetlabStressDashboardToken)
	slotBehaviour := map[string]edgeBehaviour{}
	for name, slot := range board.Slots {
		if slot.DefaultBinding == nil || slot.DefaultBinding.Kind != "device" {
			t.Fatalf("stress slot %q does not bind a device", name)
		}
		behaviour, ok := behaviourOf[slot.DefaultBinding.DeviceToken]
		if !ok {
			t.Fatalf("stress slot %q binds %q, which is not an edge device — the board would "+
				"show nominal data under a banner announcing hostile data",
				name, slot.DefaultBinding.DeviceToken)
		}
		slotBehaviour[name] = behaviour
	}

	// Every behaviour must be on the board. Otherwise the lane produces a case
	// nothing displays, which is indistinguishable from not producing it.
	shown := map[edgeBehaviour]bool{}
	for _, behaviour := range slotBehaviour {
		shown[behaviour] = true
	}
	for _, behaviour := range []edgeBehaviour{edgeExtremes, edgePartial, edgeSilent} {
		if !shown[behaviour] {
			t.Errorf("no stress slot binds a device with behaviour %d, so that pathology is "+
				"generated and never shown", behaviour)
		}
	}

	// And each widget must read the slot whose device produces what it claims.
	want := map[string]edgeBehaviour{
		"wl-stress-chart":   edgeExtremes,
		"wl-stress-gauge":   edgeExtremes,
		"wl-stress-card":    edgeExtremes,
		"wl-stress-table":   edgePartial,
		"wl-stress-silence": edgeSilent,
	}
	checked := 0
	for _, w := range board.Widgets {
		expected, named := want[w.Id]
		if !named {
			continue
		}
		if w.Datasource == nil {
			t.Errorf("widget %q carries no datasource", w.Id)
			continue
		}
		checked++
		if got := slotBehaviour[w.Datasource.Slot]; got != expected {
			t.Errorf("widget %q reads slot %q, whose device has behaviour %d, but the widget "+
				"shows behaviour %d", w.Id, w.Datasource.Slot, got, expected)
		}
	}
	if checked != len(want) {
		t.Errorf("checked %d of %d named widgets; the rest are absent from the board and this "+
			"test asserted nothing about them", checked, len(want))
	}
}

// The gallery is the catalog, so its zone-scoped alarm widgets must not fill with
// pathological alarms. The profile is shared, so the DETECT rule fires for edge
// devices too — an edge sensor inside the gallery's zone would put a 350 C alarm on
// the catalog board, and one click on the alarm table's originator drill would
// rebind the gallery's card, gauge, chart and table to the extremes device.
func TestWidgetlabEdgeDevicesAreOutsideTheGalleryZones(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()

	zones := map[string]bool{}
	for _, a := range m.Areas {
		zones[a.Token] = true
	}
	if len(zones) == 0 {
		t.Fatal("the manifest declares no zones, so this test asserts nothing")
	}

	nominalInAZone := 0
	for _, d := range m.Expand(m.Seed) {
		for _, a := range d.Assignments {
			if a.TargetType != "area" || !zones[a.TargetToken] {
				continue
			}
			if d.DeviceTypeToken == WidgetlabEdgeDeviceTypeToken {
				t.Errorf("edge device %q is assigned to zone %q; the gallery's alarm widgets are "+
					"scoped to a zone, so its pathological alarms would appear on the catalog",
					d.Token, a.TargetToken)
			}
			nominalInAZone++
		}
	}
	if nominalInAZone == 0 {
		t.Error("no device is in any zone at all, so the gallery's zone-scoped widgets have " +
			"nothing to show and this test passed for the wrong reason")
	}
}

// stripGoComments blanks out comments before a source scan.
//
// 🔴 A regex over raw source cannot tell code from prose, so a gate that scans for a
// declaration is satisfied by the same text sitting in a comment — the retired check
// coming back to life as documentation. Verified: deleting a severity constant and
// leaving its name in a comment passed the gate while the real compiler rejected the
// rule. Comments are replaced with spaces rather than removed so that offsets, and
// therefore any multi-line pattern spanning them, behave the same.
func stripGoComments(source string) string {
	blank := func(m string) string {
		out := []rune(m)
		for i, r := range out {
			if r != '\n' {
				out[i] = ' '
			}
		}
		return string(out)
	}
	source = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllStringFunc(source, blank)
	return regexp.MustCompile(`(?m)//.*$`).ReplaceAllStringFunc(source, blank)
}

// ---- The command far end -------------------------------------------------------

// 🔑 THE CONTROL-CHANNEL GATE. A board offering control and a scenario whose devices
// answer are facts on opposite sides of the "definition is opaque JSON" boundary,
// so nothing but Manifest.Validate can see both — and while nothing did, widgetlab
// shipped a Send button whose every command reached SENT and expired.
//
// Asserted in both directions. Rejecting the unanswered board is only meaningful
// while a board WITH a far end still passes.
func TestValidateRefusesAControlBoardWithNoFarEnd(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("widgetlab's own manifest is invalid: %v", err)
	}
	if !m.CommandFarEnd {
		t.Fatal("widgetlab does not declare CommandFarEnd, so the check below asserts nothing")
	}

	// Negative control on the fixture itself: this test is only about the far end if
	// the board it runs against genuinely carries a command widget.
	if !boardCarriesACommandWidget(t, m) {
		t.Fatal("no board in the manifest carries a command widget; the rejection below " +
			"would fire for the wrong reason")
	}

	stripped := m
	stripped.CommandFarEnd = false
	err := stripped.Validate()
	if err == nil {
		t.Fatal("a board carrying a command widget was accepted with no far end: its commands " +
			"would reach SENT and expire unanswered")
	}
	if !strings.Contains(err.Error(), "CommandFarEnd") {
		t.Errorf("error %q does not name the manifest field a reader has to set", err)
	}
}

// The inverse. A far end is a live broker connection per device; acquiring one for a
// scenario with no command vocabulary subscribes every device to a topic nothing can
// publish to.
func TestValidateRefusesAFarEndWithNothingToAnswer(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	m.Profiles = append([]ProfileSpec(nil), m.Profiles...)
	for i := range m.Profiles {
		m.Profiles[i].Commands = nil
	}
	// Drop the boards too: without them the command-widget check cannot be what
	// fires, so the error below is unambiguously the inverse check.
	m.Dashboards = nil
	err := m.Validate()
	if err == nil {
		t.Fatal("a manifest declaring a far end with no command definitions was accepted")
	}
	if !strings.Contains(err.Error(), "nothing for the far end to answer") {
		t.Errorf("error %q is not the far-end-with-no-commands rejection", err)
	}
}

// The far end attaches to rt.Devices, so it covers the command widget's device only
// if that device is one the manifest expands. A board binding a token outside the
// population would render a Send button for a device nothing subscribed.
func TestWidgetlabsCommandWidgetBindsADeviceTheFarEndCovers(t *testing.T) {
	m := NewWidgetlab(1, Load{}).Manifest()
	expanded := map[string]bool{}
	for _, d := range m.Expand(m.Seed) {
		expanded[d.Token] = true
	}
	if len(expanded) == 0 {
		t.Fatal("the manifest expands no devices")
	}

	board := widgetlabBoard(t, WidgetlabGalleryDashboardToken)
	checked := 0
	for _, w := range board.Widgets {
		if w.Type != commandWidgetType {
			continue
		}
		if w.Datasource == nil {
			t.Errorf("command widget %q carries no datasource, so it has no device to command", w.Id)
			continue
		}
		slot, ok := board.Slots[w.Datasource.Slot]
		if !ok {
			t.Errorf("command widget %q binds slot %q, which the board does not declare",
				w.Id, w.Datasource.Slot)
			continue
		}
		if slot.DefaultBinding == nil || slot.DefaultBinding.DeviceToken == "" {
			t.Errorf("command widget %q binds slot %q, which has no default device binding",
				w.Id, w.Datasource.Slot)
			continue
		}
		if !expanded[slot.DefaultBinding.DeviceToken] {
			t.Errorf("command widget %q resolves to device %q, which the manifest does not "+
				"expand — the far end subscribes rt.Devices, so nothing would answer it",
				w.Id, slot.DefaultBinding.DeviceToken)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("the gallery carries no command widget; this test asserted nothing")
	}
}

// boardCarriesACommandWidget reports whether any dashboard in the manifest carries
// the widget type that makes a demand of the scenario's devices.
func boardCarriesACommandWidget(t *testing.T, m SimManifest) bool {
	t.Helper()
	for _, ds := range m.Dashboards {
		var parsed dashboardDefinition
		if err := json.Unmarshal([]byte(ds.Definition), &parsed); err != nil {
			t.Fatalf("dashboard %q does not parse: %v", ds.Token, err)
		}
		for _, w := range parsed.Widgets {
			if w.Type == commandWidgetType {
				return true
			}
		}
	}
	return false
}
