// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// Invariant is one judged property of the day.
type Invariant struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
	// Control names the planted defect that must turn this invariant red, for the rig.
	Control string `json:"control"`
}

// The invariants, by name. The rig matches a control run to its invariant by these strings.
const (
	InvFloodEscalates     = "flood-escalates"
	InvPumpCommanded      = "pump-commanded"
	InvDerateObeyed       = "derate-obeyed"
	InvAirLockOnlyEast    = "air-lock-only-east"
	InvCorrelationFires   = "correlation-fires"
	InvCorrelationControl = "correlation-control-is-red"
	InvOutageBlastRadius  = "outage-blast-radius"
	InvBackfillStored     = "backfill-stored"
)

// Controls maps each invariant to the defect that must turn it red. The pump's control is the
// simulator's --no-command-far-end (nothing answers start-pump); the correlation's is run by the
// verifier itself in the same day (InvCorrelationControl).
var Controls = map[string]string{
	InvFloodEscalates:     sim.HarborpulseDefectFloodSplitKey,
	InvPumpCommanded:      "no-command-far-end",
	InvDerateObeyed:       sim.HarborpulseDefectRefuseDerate,
	InvAirLockOnlyEast:    sim.HarborpulseDefectAirLockBasin,
	InvCorrelationFires:   "count above the gauges a quay has (" + InvCorrelationControl + ")",
	InvCorrelationControl: "none: it IS the control of " + InvCorrelationFires,
	InvOutageBlastRadius:  sim.HarborpulseDefectSilenceNorthLamp,
	InvBackfillStored:     sim.HarborpulseDefectDropBackfill,
}

// Evaluate judges the day. Every invariant is evaluated; none short-circuits another.
func Evaluate(o Observations) []Invariant {
	checks := []struct {
		name string
		fn   func(Observations) error
	}{
		{InvFloodEscalates, floodEscalates},
		{InvPumpCommanded, pumpCommanded},
		{InvDerateObeyed, derateObeyed},
		{InvAirLockOnlyEast, airLockOnlyEast},
		{InvCorrelationFires, func(o Observations) error { return correlationFires(o.Correlation) }},
		{InvCorrelationControl, func(o Observations) error { return correlationControlIsRed(o.CorrelationControl) }},
		{InvOutageBlastRadius, outageBlastRadius},
		{InvBackfillStored, backfillStored},
	}
	for _, c := range s3Checks() {
		if o.S3.Sink == nil && sliceHas(SinkInvariants, c.name) {
			continue // not judged without a sink: see Omitted
		}
		checks = append(checks, c)
	}
	out := make([]Invariant, 0, len(checks))
	for _, c := range checks {
		inv := Invariant{Name: c.name, Passed: true, Control: Controls[c.name]}
		if err := c.fn(o); err != nil {
			inv.Passed, inv.Detail = false, err.Error()
		} else {
			inv.Detail = "holds"
		}
		out = append(out, inv)
	}
	return out
}

// Coverage is the no-verdict check: a watch whose polls left a gap a tier could hide in cannot
// say a tier was skipped, so it says nothing.
func Coverage(o Observations) error {
	if len(o.Polls) == 0 {
		return fmt.Errorf("no poll of the alarm list answered")
	}
	from, to := o.at(WatchFrom), o.at(WatchTo)
	if o.Polls[0].After(from.Add(maxPollGap)) {
		return fmt.Errorf("the first answered poll (%s) came %s after the watch began", o.Polls[0].Format(time.RFC3339), o.Polls[0].Sub(from))
	}
	if o.Polls[len(o.Polls)-1].Before(to) {
		return fmt.Errorf("the last answered poll (%s) came before the watch ended (%s)", o.Polls[len(o.Polls)-1].Format(time.RFC3339), to.Format(time.RFC3339))
	}
	for i := 1; i < len(o.Polls); i++ {
		if gap := o.Polls[i].Sub(o.Polls[i-1]); gap > maxPollGap {
			return fmt.Errorf("no poll answered for %s from %s: a flood tier could have come and gone unseen", gap.Round(time.Second), o.Polls[i-1].Format(time.RFC3339))
		}
	}
	return nil
}

// ---- helpers ------------------------------------------------------------------------------------

func severityRank(s string) int {
	switch s {
	case sim.AlarmSeverityMinorWire:
		return 1
	case sim.AlarmSeverityMajorWire:
		return 2
	case sim.AlarmSeverityCriticalWire:
		return 3
	}
	return 0
}

// samples returns the alarm samples matching keep inside [from, to], in time order.
func samples(o Observations, from, to time.Time, keep func(AlarmSample) bool) []AlarmSample {
	var out []AlarmSample
	for _, a := range o.Alarms {
		if !a.At.Before(from) && !a.At.After(to) && keep(a) {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// activeDevices is the set of devices seen with an ACTIVE alarm of key in [from, to].
func activeDevices(o Observations, key string, from, to time.Time) map[string]time.Time {
	first := map[string]time.Time{}
	for _, a := range samples(o, from, to, func(a AlarmSample) bool { return a.Key == key && a.State == sim.AlarmStateActiveWire }) {
		if _, seen := first[a.Device]; !seen {
			first[a.Device] = a.At
		}
	}
	return first
}

// lastState is the last sampled state of the alarm token, or "" when it was never sampled.
func lastState(o Observations, token string) string {
	var last AlarmSample
	for _, a := range o.Alarms {
		if a.Token == token && !a.At.Before(last.At) {
			last = a
		}
	}
	return last.State
}

func fleetOf(deviceType, quay string) []string {
	var out []string
	for _, d := range sim.HarborpulseFleet() {
		if d.DeviceType == deviceType && (quay == "" || d.Quay == quay) {
			out = append(out, d.Token)
		}
	}
	return out
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameSet reports a difference between the devices seen and the devices wanted, or "".
func sameSet(seen map[string]time.Time, want []string) string {
	w := setOf(want)
	var extra, missing []string
	for d := range seen {
		if !w[d] {
			extra = append(extra, d)
		}
	}
	for _, d := range want {
		if _, ok := seen[d]; !ok {
			missing = append(missing, d)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	var parts []string
	if len(extra) > 0 {
		parts = append(parts, "raised outside the expected set: "+strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "never raised for: "+strings.Join(missing, ", "))
	}
	return strings.Join(parts, "; ")
}

func commandsOf(o Observations, device, name string, from, to time.Time) []Command {
	var out []Command
	for _, c := range o.Commands {
		if c.Device == device && c.Name == name && !c.Queued.Before(from) && !c.Queued.After(to) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Queued.Before(out[j].Queued) })
	return out
}

func firstSuccessful(cmds []Command) (Command, bool) {
	for _, c := range cmds {
		if c.Status == "SUCCESSFUL" && !c.Responded.IsZero() {
			return c, true
		}
	}
	return Command{}, false
}

func statuses(cmds []Command) string {
	if len(cmds) == 0 {
		return "none"
	}
	var s []string
	for _, c := range cmds {
		s = append(s, c.Status)
	}
	return strings.Join(s, ", ")
}

func readingsOf(o Observations, device, name string, from, to time.Time) []Reading {
	var out []Reading
	for _, r := range o.Readings[device] {
		if r.Name == name && !r.At.Before(from) && !r.At.After(to) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ---- the invariants -----------------------------------------------------------------------------

// floodEscalates: each gauge keeps ONE flood alarm through the storm, which is raised at MINOR,
// escalates in place (to CRITICAL on East Quay, to MAJOR and no further elsewhere) and clears.
func floodEscalates(o Observations) error {
	from, to := o.at(stormFrom), o.end()
	var problems []string
	for _, d := range sim.HarborpulseFleet() {
		if d.DeviceType != sim.HarborpulseGaugeType {
			continue
		}
		g := d.Token
		active := samples(o, from, to, func(a AlarmSample) bool {
			return a.Device == g && strings.HasPrefix(a.Key, sim.HarborpulseFloodAlarmKey) && a.State == sim.AlarmStateActiveWire
		})
		if len(active) == 0 {
			problems = append(problems, g+": no flood alarm was ever active")
			continue
		}
		tokens := map[string]bool{}
		keys := map[string]bool{}
		for _, a := range active {
			tokens[a.Token] = true
			keys[a.Key] = true
		}
		if len(tokens) != 1 {
			problems = append(problems, fmt.Sprintf("%s: %d flood alarms active (keys %s), not one escalating in place", g, len(tokens), strings.Join(sortedKeys(keys), ", ")))
			continue
		}
		if active[0].Severity != sim.AlarmSeverityMinorWire {
			problems = append(problems, fmt.Sprintf("%s: first seen at %s, not raised at MINOR", g, active[0].Severity))
			continue
		}
		peak, peakAt := 0, -1
		for i, a := range active {
			if r := severityRank(a.Severity); r > peak {
				peak, peakAt = r, i
			}
		}
		// On the way up, the severity never falls.
		for i := 1; i <= peakAt; i++ {
			if severityRank(active[i].Severity) < severityRank(active[i-1].Severity) {
				problems = append(problems, fmt.Sprintf("%s: fell from %s to %s before its peak", g, active[i-1].Severity, active[i].Severity))
				break
			}
		}
		seenMajor := false
		for _, a := range active[:peakAt+1] {
			seenMajor = seenMajor || a.Severity == sim.AlarmSeverityMajorWire
		}
		want := 2
		if d.Quay == sim.HarborpulseOutageQuay {
			want = 3
		}
		switch {
		case peak != want:
			problems = append(problems, fmt.Sprintf("%s (%s): peaked at rank %d, want %s", g, d.Quay, peak, map[int]string{2: "MAJOR", 3: "CRITICAL"}[want]))
		case !seenMajor:
			problems = append(problems, g+": never seen at MAJOR on the way up")
		}
		if st := lastState(o, active[0].Token); st != sim.AlarmStateClearedWire {
			problems = append(problems, fmt.Sprintf("%s: flood alarm last seen %s, not cleared", g, st))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// pumpCommanded: each healthy pump's wet-well-high firing is answered — a start-pump reaches
// SUCCESSFUL, the pump then reports running with the well falling — and the well never reaches
// the pump's own float, so the platform, not the float, started it.
func pumpCommanded(o Observations) error {
	from, to := o.at(stormFrom), o.at(pumpTo)
	var problems []string
	for _, p := range fleetOf(sim.HarborpulsePumpType, "") {
		if p == sim.HarborpulseAirLockPump {
			continue // its air lock is its own beat
		}
		if _, ok := activeDevices(o, sim.HarborpulseWetWellAlarmKey, from, o.end())[p]; !ok {
			problems = append(problems, p+": wet-well-high never active through the storm")
			continue
		}
		cmds := commandsOf(o, p, sim.HarborpulseStartPumpKey, from, to)
		c, ok := firstSuccessful(cmds)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no start-pump reached SUCCESSFUL (statuses: %s)", p, statuses(cmds)))
		} else {
			running := false
			for _, r := range readingsOf(o, p, sim.HarborpulsePumpRunningKey, c.Responded.Add(-time.Second), c.Responded.Add(responseWithin)) {
				running = running || r.Value > 0.5
			}
			well := readingsOf(o, p, sim.HarborpulseWetWellKey, c.Responded.Add(-responseWithin), c.Responded.Add(responseWithin))
			var before, low float64 = math.NaN(), math.Inf(1)
			for _, r := range well {
				if !r.At.After(c.Responded) {
					before = r.Value
				} else {
					low = math.Min(low, r.Value)
				}
			}
			switch {
			case !running:
				problems = append(problems, fmt.Sprintf("%s: start-pump answered at %s but the pump never reported running", p, c.Responded.Format(time.RFC3339)))
			case math.IsNaN(before) || math.IsInf(low, 1) || low > before-0.1:
				problems = append(problems, fmt.Sprintf("%s: the well did not fall after start-pump (%.3f m before, lowest %.3f m after)", p, before, low))
			}
		}
		peak := math.Inf(-1)
		for _, r := range readingsOf(o, p, sim.HarborpulseWetWellKey, from, to) {
			peak = math.Max(peak, r.Value)
		}
		switch {
		case math.IsInf(peak, -1):
			problems = append(problems, p+": no wet-well reading stored through the storm")
		case peak >= sim.HarborpulseFloatStartM:
			problems = append(problems, fmt.Sprintf("%s: the well reached %.3f m, at or above the float (%.1f m): the float started the pump, not the platform", p, peak, sim.HarborpulseFloatStartM))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// derateObeyed: the overheating charger's connector-hot fires, a derate reaches SUCCESSFUL, the
// charger then reports its cap at the derated power and its connector cools below the rule's level
// before the long session ends (so it was the derate that cooled it), and the alarm clears.
func derateObeyed(o Observations) error {
	c := sim.HarborpulseHotCharger
	from, to := o.at(WatchFrom), o.at(derateTo)
	active := samples(o, from, to, func(a AlarmSample) bool {
		return a.Device == c && a.Key == sim.HarborpulseConnectorHotAlarmKey && a.State == sim.AlarmStateActiveWire
	})
	if len(active) == 0 {
		return fmt.Errorf("%s: connector-hot never active", c)
	}
	cmds := commandsOf(o, c, sim.HarborpulseDerateKey, from, to)
	cmd, ok := firstSuccessful(cmds)
	if !ok {
		return fmt.Errorf("%s: no derate reached SUCCESSFUL (statuses: %s)", c, statuses(cmds))
	}
	capped := false
	for _, r := range readingsOf(o, c, sim.HarborpulseMaxKwKey, cmd.Responded.Add(-time.Second), cmd.Responded.Add(responseWithin)) {
		capped = capped || math.Abs(r.Value-sim.HarborpulseDeratedKw) < 0.01
	}
	if !capped {
		return fmt.Errorf("%s: derate answered at %s but the charger never reported its cap at %.0f kW", c, cmd.Responded.Format(time.RFC3339), sim.HarborpulseDeratedKw)
	}
	// The derate's effect, in the telemetry: the connector reads below the rule's level while the
	// long session is still running. Without the derate it stays hot until the session ends. (The
	// alarm itself clears on a one-minute average, which on a 24-minute day lands too close to the
	// session's end to tell the two apart.)
	sessionEnds := o.at(sim.HarborpulseOverheatTo)
	cooled := false
	for _, r := range readingsOf(o, c, sim.HarborpulseConnectorKey, cmd.Responded, sessionEnds) {
		cooled = cooled || r.Value < sim.HarborpulseConnectorHotC
	}
	if !cooled {
		return fmt.Errorf("%s: the connector never read below %.0f C between the derate (%s) and the end of the session (%s): the derate did not cool it",
			c, sim.HarborpulseConnectorHotC, cmd.Responded.Format(time.RFC3339), sessionEnds.Format(time.RFC3339))
	}
	cleared := samples(o, active[0].At, to, func(a AlarmSample) bool {
		return a.Token == active[0].Token && a.State == sim.AlarmStateClearedWire
	})
	if len(cleared) == 0 {
		return fmt.Errorf("%s: connector-hot did not clear by %s", c, to.Format(time.RFC3339))
	}
	return nil
}

// airLockOnlyEast: pump-air-lock is raised for the East Quay pump, inside its window, and for no
// other pump.
func airLockOnlyEast(o Observations) error {
	seen := activeDevices(o, sim.HarborpulseAirLockAlarmKey, o.at(WatchFrom), o.end())
	if diff := sameSet(seen, []string{sim.HarborpulseAirLockPump}); diff != "" {
		return fmt.Errorf("pump-air-lock %s", diff)
	}
	at := seen[sim.HarborpulseAirLockPump]
	if at.Before(o.at(sim.HarborpulseAirLockFrom)) || at.After(o.at(sim.HarborpulseAirLockTo)) {
		return fmt.Errorf("pump-air-lock first active at %s, outside the air lock (%s to %s)", at.Format(time.RFC3339),
			o.at(sim.HarborpulseAirLockFrom).Format(time.RFC3339), o.at(sim.HarborpulseAirLockTo).Format(time.RFC3339))
	}
	return nil
}

// correlationFires: previewing quay-flooding over the storm, it fires for East Quay and not for
// the other quays.
func correlationFires(p Preview) error {
	if !p.OK {
		return fmt.Errorf("previewRule refused the rule")
	}
	if p.Degraded != "" {
		return fmt.Errorf("previewRule answered a partial result: %s", p.Degraded)
	}
	raised := map[string]time.Time{}
	for _, f := range p.Firings {
		if f.Signal == "raised" {
			if _, ok := raised[f.Series]; !ok {
				raised[f.Series] = f.At
			}
		}
	}
	if diff := sameSet(raised, []string{sim.HarborpulseEastQuay}); diff != "" {
		return fmt.Errorf("quay-flooding %s", diff)
	}
	return nil
}

// correlationControlIsRed: the same preview with a count above the gauges a quay has must fire for
// no quay, so correlationFires must judge it red. A control that comes back green means the check
// cannot tell a firing from a non-firing.
func correlationControlIsRed(p Preview) error {
	if !p.OK || p.Degraded != "" {
		return fmt.Errorf("the control preview did not run cleanly (ok=%v, degraded=%q), so it shows nothing", p.OK, p.Degraded)
	}
	if correlationFires(p) == nil {
		return fmt.Errorf("the control (a count no quay can reach) was judged green: %s cannot fail", InvCorrelationFires)
	}
	for _, f := range p.Firings {
		if f.Signal == "raised" {
			return fmt.Errorf("the control fired for %s with a count no quay can reach", f.Series)
		}
	}
	return nil
}

// outageBlastRadius: the absence rules raise for exactly the East Quay lamps and chargers, inside
// the outage, nothing anywhere else, and every one clears after the feeder is restored.
func outageBlastRadius(o Observations) error {
	var problems []string
	for _, k := range []struct{ key, deviceType string }{
		{sim.HarborpulseLightSilentAlarmKey, sim.HarborpulseLightType},
		{sim.HarborpulseChargerOfflineKey, sim.HarborpulseChargerType},
	} {
		seen := activeDevices(o, k.key, o.at(WatchFrom), o.end())
		if diff := sameSet(seen, fleetOf(k.deviceType, sim.HarborpulseOutageQuay)); diff != "" {
			problems = append(problems, k.key+" "+diff)
			continue
		}
		outFrom, outTo := o.at(sim.HarborpulseOutageFrom), o.at(sim.HarborpulseOutageTo)
		for _, d := range sortedKeys(seen) {
			if at := seen[d]; at.Before(outFrom) || at.After(outTo) {
				problems = append(problems, fmt.Sprintf("%s: %s first active at %s, outside the outage", d, k.key, at.Format(time.RFC3339)))
			}
		}
		for _, a := range samples(o, o.at(WatchFrom), o.end(), func(a AlarmSample) bool { return a.Key == k.key }) {
			if st := lastState(o, a.Token); st != sim.AlarmStateClearedWire {
				problems = append(problems, fmt.Sprintf("%s: %s last seen %s, not cleared after the restore", a.Device, k.key, st))
				break
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

// backfillStored: every reading an East Quay charger buffered through the outage is stored, at
// the time it was TAKEN. The window [First, Last] holds no live reading (the charger sent nothing
// live while its feeder was down), so a platform that stamped the arrival time stores none there.
func backfillStored(o Observations) error {
	want := fleetOf(sim.HarborpulseChargerType, sim.HarborpulseOutageQuay)
	got := map[string]time.Time{}
	for _, b := range o.Backfill {
		got[b.Device] = b.First
	}
	if diff := sameSet(got, want); diff != "" {
		return fmt.Errorf("the outage ledger %s", strings.ReplaceAll(diff, "raised", "buffered"))
	}
	var problems []string
	for _, b := range o.Backfill {
		if b.Buffered < 1 {
			problems = append(problems, b.Device+": buffered nothing")
			continue
		}
		distinct := map[int64]bool{}
		for _, t := range b.Stored {
			if !t.Before(b.First.Add(-time.Millisecond)) && !t.After(b.Last.Add(time.Millisecond)) {
				distinct[t.UnixMicro()] = true
			}
		}
		if len(distinct) != b.Buffered {
			problems = append(problems, fmt.Sprintf("%s: buffered %d readings from %s to %s, %d stored at their own times",
				b.Device, b.Buffered, b.First.Format(time.RFC3339), b.Last.Format(time.RFC3339), len(distinct)))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}
