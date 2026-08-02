// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"math"
	"net/http"
	"testing"
)

// ---- The temperature curve and the alarm threshold, checked against each other --
//
// buildingpulse is the sellable demo, and its headline is a live alarm. The failure
// this file exists to catch is not a wrong number in isolation — it is the curve and
// the rule drifting apart: Tick changes its amplitude, the rule keeps its threshold,
// both diffs read as local and correct, and the demo's alarm-table is empty forever
// with every other test green. Every assertion below reads BOTH sides.

// A fast algebraic diagnosis, not unique coverage: every failure this catches also fails
// the per-device wire test below, which is the real gate. It earns its place by saying
// WHICH of the constants is wrong instead of reporting that twelve devices misbehaved.
func TestBuildingpulseThresholdLiesStrictlyInsideTheCurve(t *testing.T) {
	min := BuildingpulseTempCentre - BuildingpulseTempAmplitude
	max := BuildingpulseTempCentre + BuildingpulseTempAmplitude
	if BuildingpulseTempAmplitude <= 0 {
		t.Fatalf("amplitude %v leaves the temperature flat, so it can never cross anything",
			BuildingpulseTempAmplitude)
	}
	if BuildingpulseAlarmThreshold <= min {
		t.Errorf("threshold %v is at or below the curve's minimum %v, so the alarm raises "+
			"immediately and never clears", BuildingpulseAlarmThreshold, min)
	}
	if BuildingpulseAlarmThreshold >= max {
		t.Errorf("threshold %v is at or above the curve's maximum %v, so the curve can never "+
			"reach it and no alarm ever raises", BuildingpulseAlarmThreshold, max)
	}
}

// A phase step that does not divide the period into enough samples would step OVER
// the region above the threshold: the curve would genuinely exceed it between two
// ticks while no emitted sample ever did, so the platform — which only ever sees the
// samples — would raise nothing. This is the sampling-rate half of the same
// design decision, and it is why the step is a named constant rather than a literal
// inside Tick.
func TestBuildingpulsePhaseStepSamplesTheRegionAboveTheThreshold(t *testing.T) {
	if BuildingpulsePhaseStep <= 0 {
		t.Fatalf("phase step %v does not advance, so the temperature never moves",
			BuildingpulsePhaseStep)
	}
	// The fraction of a period the sine spends above the threshold, from the phases
	// where centre + amplitude*sin(phase) == threshold.
	sinAtThreshold := (BuildingpulseAlarmThreshold - BuildingpulseTempCentre) / BuildingpulseTempAmplitude
	if sinAtThreshold >= 1 || sinAtThreshold <= -1 {
		t.Fatalf("threshold %v is not inside the curve's range — "+
			"TestBuildingpulseThresholdLiesStrictlyInsideTheCurve explains why that is fatal",
			BuildingpulseAlarmThreshold)
	}
	// asin gives the first crossing; the region above runs from there to pi minus it.
	widthInPhase := (math.Pi - math.Asin(sinAtThreshold)) - math.Asin(sinAtThreshold)
	samples := widthInPhase / BuildingpulsePhaseStep
	// Two, not one: one sample above the threshold raises an alarm but says nothing
	// about the region being observable rather than hit by luck.
	if samples < 2 {
		t.Errorf("the curve is above %v for %.2f radians of phase but each tick advances %v, "+
			"so only %.1f sample(s) land in that region — the demo would step over its own "+
			"alarm", BuildingpulseAlarmThreshold, widthInPhase, BuildingpulsePhaseStep, samples)
	}
}

// ---- What actually reaches the wire ------------------------------------------
//
// The tests above are about the constants. They would all pass while Tick emitted a
// constant, a different metric, or a curve built from other numbers entirely — so the
// claim that matters is checked where it is true or false: on the wire, out of the
// real Tick through the real emit path.

// buildingpulseWireTemperatures ticks a real buildingpulse through a fake ingress and
// returns, per device index, the temperatures that reached the wire.
//
// Keyed by device token. The load-bearing part is only that the devices are NOT
// collapsed together: "some device was above and some other device was below" is exactly
// the claim a per-device raise-and-clear is not.
func buildingpulseWireTemperatures(t *testing.T, deviceCount, ticks int) map[string][]float64 {
	t.Helper()
	byDevice := map[string][]float64{}
	rt := fakeIngress(t, deviceCount, func(w http.ResponseWriter, r *http.Request) {
		// The device token rides on the JsonEvent envelope, not inside an entry —
		// this is the real device-plane shape, read as the platform reads it.
		var body struct {
			Device  string `json:"device"`
			Payload struct {
				Entries []struct {
					Measurements map[string]string `json:"measurements"`
				} `json:"entries"`
			} `json:"payload"`
		}
		decodeJSON(t, r, &body)
		for _, entry := range body.Payload.Entries {
			raw, ok := entry.Measurements[BuildingpulseTemperatureKey]
			if !ok {
				continue
			}
			byDevice[body.Device] = append(byDevice[body.Device], parseFloat(t, raw))
		}
		w.WriteHeader(http.StatusAccepted)
	})

	// EmitAll fans out across goroutines, so this handler would otherwise run
	// concurrently and race the appends above. Tick sizes its pool from rt.Load, so
	// one worker there serialises the whole run — which is also what makes a slice
	// index mean "tick N" rather than "whichever request landed first".
	rt.Load = Load{Concurrency: 1}

	s := NewBuildingpulse(1, Load{})
	for i := 0; i < ticks; i++ {
		if err := s.Tick(context.Background(), rt); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	if len(byDevice) == 0 {
		t.Fatal("no measurement reached the ingress")
	}
	return byDevice
}

// buildingpulsePeriodTicks is how many ticks one full sine period takes, rounded up —
// derived from the phase step rather than written down, so changing the step cannot
// leave a test window that no longer covers a period.
func buildingpulsePeriodTicks() int {
	return int(math.Ceil(2 * math.Pi / BuildingpulsePhaseStep))
}

// The property the whole alarm channel rests on, asserted PER DEVICE: within one
// period every thermostat goes above the threshold and comes back below it. Crossing
// without clearing leaves the integrator monotonic — alarm-count only climbs and
// alarm-table never shows a resolve, which is half the demo unexercised.
//
// Per device, not "some sample was above and some sample was below", because the
// phase offsets mean the aggregate claim is satisfied by twelve devices each stuck on
// one side of the threshold.
func TestBuildingpulseEmittedTemperatureCrossesAndClearsPerDevice(t *testing.T) {
	// One extra tick so a device starting exactly at a crossing still has a full
	// period of samples after it.
	byDevice := buildingpulseWireTemperatures(t, buildingpulseThermostatCount, buildingpulsePeriodTicks()+1)
	if len(byDevice) != buildingpulseThermostatCount {
		t.Fatalf("expected %d devices on the wire, got %d — the per-device claim below would "+
			"be about the wrong set", buildingpulseThermostatCount, len(byDevice))
	}
	for token, temps := range byDevice {
		var above, below bool
		for _, v := range temps {
			switch {
			case v > BuildingpulseAlarmThreshold:
				above = true
			case v < BuildingpulseAlarmThreshold:
				below = true
			}
		}
		if !above {
			t.Errorf("device %s never exceeded the threshold %v in a full period, so it never "+
				"raises an alarm", token, BuildingpulseAlarmThreshold)
		}
		if !below {
			t.Errorf("device %s never fell below the threshold %v in a full period, so a raised "+
				"alarm never clears", token, BuildingpulseAlarmThreshold)
		}
	}
}

// The board's alarm-table is scoped to ONE building (dashboard.go binds the `building`
// slot), so what keeps it fed is that the building being viewed HAS thermostats — not
// that the tenant as a whole has a hot one. Combined with the per-device property
// above, every building therefore raises and clears.
//
// 🔴 A tenant-wide "someone is hot at every tick" claim does NOT establish this one, and
// an earlier version of this file conflated them. See Tick's comment for the sizes at
// which each holds.
func TestBuildingpulseGivesEveryBuildingAThermostat(t *testing.T) {
	m := NewBuildingpulse(1, Load{}).Manifest()
	perBuilding := map[string]int{}
	for _, a := range m.Areas {
		perBuilding[a.Token] = 0
	}
	if len(perBuilding) == 0 {
		t.Fatal("the manifest declares no buildings, so there is nothing for a board to scope to")
	}
	for _, d := range m.Expand(m.Seed) {
		for _, a := range d.Assignments {
			if a.TargetType == "area" {
				perBuilding[a.TargetToken]++
			}
		}
	}
	for token, count := range perBuilding {
		if count == 0 {
			t.Errorf("building %s has no thermostats assigned, so a board scoped to it shows an "+
				"empty alarm-table forever — no rule can fix that", token)
		}
	}
}

// The tenant-wide "someone is hot at every tick" property is real at the demo sizing
// and false below a population floor, so the floor is DERIVED here rather than left as
// a number in a comment that a changed threshold or amplitude would quietly invalidate.
//
// The worst tick is the one where the sine peak falls midway between two neighbouring
// devices, putting the hottest device pi/n away from the peak — so the population's
// maximum is centre + amplitude*cos(pi/n), and coverage holds exactly while that
// exceeds the threshold.
func TestBuildingpulseWholePopulationCoverageHasAStatedFloor(t *testing.T) {
	floor := buildingpulseCoverageFloor()

	// The demo's own sizing must be above its own floor, or the scenario ships in the
	// degraded state Bootstrap's warning exists to name.
	if buildingpulseThermostatCount < floor {
		t.Errorf("the demo provisions %d thermostats but whole-population coverage needs at "+
			"least %d, so the tenant-wide view has moments with no active alarm at the "+
			"DEFAULT sizing", buildingpulseThermostatCount, floor)
	}

	// 🔴 The algebra cannot check itself. Asserting the formula's own next-lower case
	// would be vacuous — the loop inside it already evaluated that and found it false, so
	// such a "control" can never fail. So the boundary is confirmed against an
	// INDEPENDENT source: the wire, out of a real Tick. The formula and the running
	// scenario are two separate derivations of the same number, and only their agreement
	// means anything.
	//
	// Three periods, so a boundary that exists in continuous phase but is only visited on
	// some ticks still gets sampled. Deterministic in both directions — no clock, no
	// randomness — so a failure here is a real disagreement, not a flake.
	window := 3*buildingpulsePeriodTicks() + 1
	if gaps := ticksWithNoDeviceAboveThreshold(t, floor, window); gaps != 0 {
		t.Errorf("the formula says %d devices cover every tick, but a real run at that size "+
			"left %d of %d ticks with nobody above %v", floor, gaps, window,
			BuildingpulseAlarmThreshold)
	}
	if floor > 1 {
		if gaps := ticksWithNoDeviceAboveThreshold(t, floor-1, window); gaps == 0 {
			t.Errorf("the formula says %d devices do NOT cover every tick, but a real run at "+
				"that size covered all %d — the floor is not where the algebra puts it, so "+
				"the number Bootstrap warns on is wrong", floor-1, window)
		}
	}
}

// ticksWithNoDeviceAboveThreshold drives a real Tick at the given population and counts
// the ticks on which no device's emitted temperature exceeded the alarm threshold.
func ticksWithNoDeviceAboveThreshold(t *testing.T, deviceCount, ticks int) int {
	t.Helper()
	byDevice := buildingpulseWireTemperatures(t, deviceCount, ticks)
	gaps := 0
	for tick := 0; tick < ticks; tick++ {
		hot := false
		for _, temps := range byDevice {
			if tick < len(temps) && temps[tick] > BuildingpulseAlarmThreshold {
				hot = true
				break
			}
		}
		if !hot {
			gaps++
		}
	}
	return gaps
}

// The curve on the wire IS the one the constants describe. Without this, every test
// above holds for a scenario emitting some unrelated shape that happens to straddle
// the threshold — and the rule's threshold would then be pinned to numbers nothing
// emits.
func TestBuildingpulseEmitsTheDeclaredCurve(t *testing.T) {
	// One device, so the phase offset term is zero and the expected value is a
	// function of the tick alone. (With one device the offset is 2*pi, which is a
	// full period per device index — index 0 either way.)
	const ticks = 5
	byDevice := buildingpulseWireTemperatures(t, 1, ticks)
	for token, temps := range byDevice {
		for i, got := range temps {
			phase := float64(i+1) * BuildingpulsePhaseStep
			want := BuildingpulseTempCentre + BuildingpulseTempAmplitude*math.Sin(phase)
			if got != want {
				t.Errorf("device %s tick %d emitted %v, but the declared curve gives %v — the "+
					"threshold is chosen against a curve the scenario does not emit",
					token, i+1, got, want)
			}
		}
	}
}

// ---- The rule ------------------------------------------------------------------

func buildingpulseProfile(t *testing.T) ProfileSpec {
	t.Helper()
	return profileByToken(t, NewBuildingpulse(1, Load{}).Manifest(), BuildingpulseProfileToken)
}

// The rule is authored FROM the constants above, not from literals that happen to
// agree with them today. This is the seam ADR-062 bit us on: two halves of one
// decision, each individually defensible, drifting apart across separate PRs.
func TestBuildingpulseRuleIsAuthoredFromTheCurveConstants(t *testing.T) {
	rule := soleDetectionRule(t, buildingpulseProfile(t))
	def := decodeThresholdRule(t, rule.Definition)

	if def.When.Threshold != BuildingpulseAlarmThreshold {
		t.Errorf("rule fires above %v but the curve is designed around %v — the two halves of "+
			"the threshold decision have drifted", def.When.Threshold, BuildingpulseAlarmThreshold)
	}
	if def.When.Metric != BuildingpulseTemperatureKey {
		t.Errorf("rule reads metric %q, but %q is the metric the curve drives",
			def.When.Metric, BuildingpulseTemperatureKey)
	}
	// "gt" is what makes crossing UPWARD the raise. With "lt" the curve would still
	// cross twice a period and the alarm would simply be inverted — hot thermostats
	// clear and cool ones alarm, which reads as working.
	if def.When.Op != "gt" {
		t.Errorf("rule operator is %q, not \"gt\" — the alarm would be inverted", def.When.Op)
	}
	if len(def.Actions) != 1 || def.Actions[0].Type != "raiseAlarm" {
		t.Fatalf("expected a single raiseAlarm action, got %+v", def.Actions)
	}
	if def.Actions[0].RaiseAlarm.AlarmKey != BuildingpulseAlarmKey {
		t.Errorf("rule raises alarm key %q, not the declared %q",
			def.Actions[0].RaiseAlarm.AlarmKey, BuildingpulseAlarmKey)
	}
	// The AUTHORING severity, lowercase. The wire form is a different constant and is
	// held to device-management's real enum through the authored-rules fixture; what
	// this checks is that the rule carries the authoring one, since a raiseAlarm action
	// requires a non-empty severity and the compiler rejects the uppercase spelling.
	if def.Severity != BuildingpulseSeverity {
		t.Errorf("rule declares severity %q, not the authoring constant %q",
			def.Severity, BuildingpulseSeverity)
	}
	if !rule.Enabled {
		t.Error("rule is disabled: publish-time validation only gates ENABLED rules, so a " +
			"disabled one is published unchecked and never fires")
	}
}
