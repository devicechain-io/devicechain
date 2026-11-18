// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// These tests hold the scripted day to the rules that are meant to see it. Every level, window,
// hold, count and timeout is read back OUT OF THE PUBLISHED RULE DEFINITION, never from the Go
// constant that built it, so a threshold moved without the curve that crosses it (or the other
// way round) fails here instead of producing a demo in which a beat silently never fires. Where
// a beat depends on the platform obeying a command, the same day is also run WITHOUT the command
// as a control, and the beat's evidence must then be absent.

const harborCadence = 5 * time.Second

type harborRule map[string]any

func harborRules(t *testing.T) map[string]harborRule {
	t.Helper()
	out := map[string]harborRule{}
	for _, p := range NewHarborpulse(1, Load{}).Manifest().Profiles {
		for _, r := range p.DetectionRules {
			var doc harborRule
			if err := json.Unmarshal([]byte(r.Definition), &doc); err != nil {
				t.Fatalf("rule %s: %v", r.Token, err)
			}
			out[r.Token] = doc
		}
	}
	return out
}

func (r harborRule) num(t *testing.T, path ...string) float64 {
	t.Helper()
	var cur any = map[string]any(r)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("rule has no %v", path)
		}
		cur = m[k]
	}
	v, ok := cur.(float64)
	if !ok {
		t.Fatalf("rule field %v is %T, not a number", path, cur)
	}
	return v
}

func (r harborRule) dur(t *testing.T, key string) time.Duration {
	t.Helper()
	s, _ := r[key].(string)
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatalf("rule field %s = %q: %v", key, s, err)
	}
	return d
}

// harborSample is every device's reading at one tick; a device absent from values was silent.
type harborSample struct {
	at     time.Duration
	hour   float64
	values map[string]map[string]float64
}

// simulateHarborDay plays one full sim day (06:00 to 06:00) at the default cadence. With obey,
// it plays the platform's half too: each rising edge of wet-well-high sends start-pump with the
// rule's own payload, and each rising edge of connector-hot sends derate with its own.
func simulateHarborDay(t *testing.T, obey bool) []harborSample {
	t.Helper()
	rules := harborRules(t)
	wellLevel := rules[HarborpulseWetWellRule].num(t, "when", "threshold")
	hotLevel := rules[HarborpulseConnectorHotRule].num(t, "threshold")
	hotWindow := rules[HarborpulseConnectorHotRule].dur(t, "window")
	startPayload := harborCommandPayload(t, rules[HarborpulseWetWellRule], HarborpulseStartPumpKey)
	deratePayload := harborCommandPayload(t, rules[HarborpulseConnectorHotRule], HarborpulseDerateKey)

	m := newHarborModel(1)
	base := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	var out []harborSample
	wellHigh := map[string]bool{}
	hot := map[string]bool{}
	for at := time.Duration(0); at <= harborpulseDayLength; at += harborCadence {
		m.advanceTo(at)
		s := harborSample{at: at, hour: harborHour(at), values: map[string]map[string]float64{}}
		for _, d := range harborLayout() {
			if v := m.reading(d.token, at, base.Add(at)); v != nil {
				s.values[d.token] = v
			}
		}
		out = append(out, s)
		if !obey {
			continue
		}
		for _, d := range harborLayout() {
			v := s.values[d.token]
			switch d.kind {
			case harborPump:
				high := v[HarborpulseWetWellKey] > wellLevel
				if high && !wellHigh[d.token] && !m.command(d.token, HarborpulseStartPumpKey, startPayload) {
					t.Fatalf("%s refused the start-pump its own rule sends (%s)", d.token, startPayload)
				}
				wellHigh[d.token] = high
			case harborCharger:
				avg, ok := harborSlidingAvg(out, d.token, HarborpulseConnectorKey, hotWindow)
				over := ok && avg > hotLevel
				if over && !hot[d.token] && !m.command(d.token, HarborpulseDerateKey, deratePayload) {
					t.Fatalf("%s refused the derate its own rule sends (%s)", d.token, deratePayload)
				}
				hot[d.token] = over
			}
		}
	}
	return out
}

func harborCommandPayload(t *testing.T, r harborRule, key string) json.RawMessage {
	t.Helper()
	actions, _ := r["actions"].([]any)
	for _, a := range actions {
		send, _ := a.(map[string]any)["sendCommand"].(map[string]any)
		if send != nil && send["command"] == key {
			p, _ := send["payload"].(string)
			if p == "" {
				t.Fatalf("the %s action carries no payload, and the command's parameter is required", key)
			}
			return json.RawMessage(p)
		}
	}
	t.Fatalf("the rule sends no %s", key)
	return nil
}

// harborSlidingAvg is the mean of metric over the trailing window ending at the last sample.
func harborSlidingAvg(samples []harborSample, token, metric string, window time.Duration) (float64, bool) {
	if len(samples) == 0 {
		return 0, false
	}
	end := samples[len(samples)-1].at
	sum, n := 0.0, 0
	for i := len(samples) - 1; i >= 0 && end-samples[i].at < window; i-- {
		if v, ok := samples[i].values[token][metric]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

func harborTokens(kind harborKind, quay string) []string {
	var out []string
	for _, d := range harborLayout() {
		if d.kind == kind && (quay == "" || d.quay == quay) {
			out = append(out, d.token)
		}
	}
	return out
}

func TestHarborpulseManifestShape(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, ok := Registry["harborpulse"]; !ok {
		t.Fatal("harborpulse is not registered")
	}
	if !m.FixedTopology || m.FarEndMode() != FarEndInternal {
		t.Fatalf("FixedTopology=%v far end=%q; want a fixed fixture with an in-process far end", m.FixedTopology, m.FarEndMode())
	}
	if _, err := NewSim("harborpulse", 1, Load{DeviceCount: 50}); err == nil {
		t.Fatal("a device-count override was accepted; the planted faults name devices a resize cannot keep")
	}
	devices := m.Expand(1)
	if len(devices) != 33 {
		t.Fatalf("%d devices, want 33", len(devices))
	}
	quays := map[string]bool{HarborpulseNorthQuay: true, HarborpulseEastQuay: true, HarborpulseBasin: true}
	owners := map[string]string{harborLoftsMeter: HarborpulseLoftsToken, harborFerryMeter: HarborpulseFerryToken}
	for _, d := range devices {
		var areas, customers []string
		for _, a := range d.Assignments {
			switch a.TargetType {
			case "area":
				areas = append(areas, a.TargetToken)
			case "customer":
				customers = append(customers, a.TargetToken)
			}
		}
		// One quay each: a second area anchor would make the correlation rule fan the device
		// into a second anchor and fire for it too.
		if len(areas) != 1 || !quays[areas[0]] {
			t.Errorf("%s is in areas %v, want exactly one quay", d.Token, areas)
		}
		want := owners[d.Token]
		if want == "" {
			want = HarborpulsePublicRealmToken
		}
		if len(customers) != 1 || customers[0] != want {
			t.Errorf("%s belongs to %v, want %s", d.Token, customers, want)
		}
	}
	if len(m.CustomerUsers) != 1 || m.CustomerUsers[0].CustomerToken != HarborpulseLoftsToken {
		t.Fatalf("customer users %+v, want the Quayside Lofts owner only", m.CustomerUsers)
	}
}

// The duration rule's CEL reads two metrics; Validate can check only the one the spec names.
func TestHarborpulseCELReadsOnlyDeclaredMetrics(t *testing.T) {
	m := NewHarborpulse(1, Load{}).Manifest()
	for _, p := range m.Profiles {
		declared := map[string]bool{}
		for _, mx := range p.Metrics {
			declared[mx.Key] = true
		}
		for _, r := range p.DetectionRules {
			var doc struct {
				When struct {
					CEL string `json:"cel"`
				} `json:"when"`
			}
			_ = json.Unmarshal([]byte(r.Definition), &doc)
			reads := regexp.MustCompile(`m\["([^"]+)"\]`).FindAllStringSubmatch(doc.When.CEL, -1)
			if doc.When.CEL != "" && len(reads) < 2 {
				t.Fatalf("rule %s: parsed %d metrics out of %q; the check has rotted", r.Token, len(reads), doc.When.CEL)
			}
			for _, read := range reads {
				if !declared[read[1]] {
					t.Errorf("rule %s reads %q, which profile %s does not declare", r.Token, read[1], p.Token)
				}
			}
		}
	}
}

func TestHarborpulseFloodTiersEscalateOnEastQuayOnly(t *testing.T) {
	rules := harborRules(t)
	tiers := []string{HarborpulseFloodMinorRule, HarborpulseFloodMajorRule, HarborpulseFloodCriticalRule}
	for _, tok := range tiers {
		if key := rules[tok]["actions"].([]any)[0].(map[string]any)["raiseAlarm"].(map[string]any)["alarmKey"]; key != HarborpulseFloodAlarmKey {
			t.Fatalf("%s raises under %v; all three tiers must share one key or the platform opens an alarm per tier", tok, key)
		}
	}
	minor := rules[HarborpulseFloodMinorRule].num(t, "when", "threshold")
	major := rules[HarborpulseFloodMajorRule].num(t, "when", "threshold")
	critical := rules[HarborpulseFloodCriticalRule].num(t, "when", "threshold")

	day := simulateHarborDay(t, true)
	peak := map[string]float64{}
	for _, s := range day {
		for _, g := range harborTokens(harborGauge, "") {
			v := s.values[g][HarborpulseWaterLevelKey]
			if v > peak[g] {
				peak[g] = v
			}
			// Quiet: nothing above the first tier outside the storm.
			if (s.hour < 16 || s.hour >= 23.5) && v > minor {
				t.Errorf("%s at %.2fh reads %.3f m, above the minor tier %.2f outside the storm", g, s.hour, v, minor)
			}
		}
	}
	for _, g := range harborTokens(harborGauge, HarborpulseEastQuay) {
		if peak[g] <= critical {
			t.Errorf("East Quay gauge %s peaks at %.3f m, never reaching critical %.2f", g, peak[g], critical)
		}
	}
	for _, q := range []string{HarborpulseNorthQuay, HarborpulseBasin} {
		for _, g := range harborTokens(harborGauge, q) {
			if peak[g] <= major || peak[g] >= critical {
				t.Errorf("%s peaks at %.3f m; it must reach major (%.2f) and stay below critical (%.2f)", g, peak[g], major, critical)
			}
		}
	}
}

func TestHarborpulseSurgeRiseIsFasterThanAnyTide(t *testing.T) {
	limit := harborRules(t)[HarborpulseSurgeRule].num(t, "threshold")
	day := simulateHarborDay(t, true)
	fired := map[string]bool{}
	for i := 1; i < len(day); i++ {
		dt := (day[i].at - day[i-1].at).Seconds()
		for _, g := range harborTokens(harborGauge, "") {
			rate := (day[i].values[g][HarborpulseWaterLevelKey] - day[i-1].values[g][HarborpulseWaterLevelKey]) / dt
			if rate > limit {
				if day[i].hour < 17 || day[i].hour > 18.75 {
					t.Errorf("%s rises at %.4f m/s at %.2fh, outside the surge", g, rate, day[i].hour)
				}
				fired[g] = true
			}
		}
	}
	for _, g := range harborTokens(harborGauge, "") {
		if !fired[g] {
			t.Errorf("%s never rises faster than %.4f m/s; surge-rising would never fire", g, limit)
		}
	}
}

func TestHarborpulseCorrelationFiresForEastQuayOnly(t *testing.T) {
	r := harborRules(t)[HarborpulseQuayFloodingRule]
	if r["anchorType"] != "area" || r["actions"] != nil || r["severity"] != nil {
		t.Fatalf("the correlation rule must anchor on area and carry no severity or action: %v", r)
	}
	level := r.num(t, "when", "threshold")
	need := int(r.num(t, "count"))
	window := r.dur(t, "window")
	day := simulateHarborDay(t, true)
	fired := map[string]bool{}
	for i := range day {
		for _, q := range harborQuays {
			distinct := map[string]bool{}
			for j := i; j >= 0 && day[i].at-day[j].at < window; j-- {
				for _, g := range harborTokens(harborGauge, q.token) {
					if day[j].values[g][HarborpulseWaterLevelKey] > level {
						distinct[g] = true
					}
				}
			}
			if len(distinct) >= need {
				fired[q.token] = true
			}
		}
	}
	if !fired[HarborpulseEastQuay] || fired[HarborpulseNorthQuay] || fired[HarborpulseBasin] {
		t.Fatalf("correlation fires for %v; want East Quay alone", fired)
	}
}

func TestHarborpulsePumpsAreStartedByThePlatformNotTheirFloats(t *testing.T) {
	level := harborRules(t)[HarborpulseWetWellRule].num(t, "when", "threshold")
	peaks := func(obey bool) (map[string]float64, map[string]int) {
		peak, raises := map[string]float64{}, map[string]int{}
		high := map[string]bool{}
		for _, s := range simulateHarborDay(t, obey) {
			for _, p := range harborTokens(harborPump, "") {
				v := s.values[p][HarborpulseWetWellKey]
				if v > peak[p] {
					peak[p] = v
				}
				if v > level && !high[p] {
					raises[p]++
				}
				high[p] = v > level
			}
		}
		return peak, raises
	}
	obeyed, raises := peaks(true)
	ignored, _ := peaks(false)
	for _, p := range []string{"hp-pump-north", "hp-pump-basin"} {
		if raises[p] < 2 {
			t.Errorf("%s crossed the wet-well level %d times; the storm should cycle it", p, raises[p])
		}
		// The two runs must be told apart by a margin no sampling or noise can blur: the
		// obeyed well turns just above the rule's level, the ignored one only at its float.
		if obeyed[p] >= harborFloatStart-0.3 {
			t.Errorf("%s peaked at %.2f m although every start-pump was obeyed; its float is %.2f", p, obeyed[p], harborFloatStart)
		}
		// Control: the same day with nobody answering commands must reach the float, or the
		// obeyed run proves nothing about the commands.
		if ignored[p] < harborFloatStart-0.1 {
			t.Errorf("control: %s peaked at %.2f m with no commands answered, below its float %.2f — "+
				"the obeyed run's peak would be the same with or without the platform", p, ignored[p], harborFloatStart)
		}
	}
}

func TestHarborpulseAirLockHoldsOnTheEastPumpOnly(t *testing.T) {
	r := harborRules(t)[HarborpulseAirLockRule]
	hold := r.dur(t, "hold")
	if cel := r["when"].(map[string]any)["cel"]; cel != harborpulseAirLockCEL {
		t.Fatalf("air-lock CEL is %v", cel)
	}
	since := map[string]time.Duration{}
	fired := map[string]bool{}
	for _, s := range simulateHarborDay(t, true) {
		for _, p := range harborTokens(harborPump, "") {
			v := s.values[p]
			// The CEL, evaluated: running AND drawing below the amps it names.
			if v[HarborpulsePumpRunningKey] > 0.5 && v[HarborpulsePumpCurrentKey] < HarborpulseAirLockAmps {
				if _, ok := since[p]; !ok {
					since[p] = s.at
				}
				if s.at-since[p] >= hold {
					fired[p] = true
					if s.hour < 19 || s.hour > 20.5 {
						t.Errorf("%s holds the air-lock condition at %.2fh, outside its fault", p, s.hour)
					}
				}
			} else {
				delete(since, p)
			}
		}
	}
	if !fired[harborAirLockPump] || len(fired) != 1 {
		t.Fatalf("air-lock fires for %v; want %s alone", fired, harborAirLockPump)
	}
}

func TestHarborpulseFlickerRepeatsOnOneLampOnly(t *testing.T) {
	r := harborRules(t)[HarborpulseFlickerRule]
	need, window, level := int(r.num(t, "count")), r.dur(t, "window"), r.num(t, "when", "threshold")
	day := simulateHarborDay(t, true)
	fired := map[string]bool{}
	for i := range day {
		for _, l := range harborTokens(harborLight, "") {
			n := 0
			for j := i; j >= 0 && day[i].at-day[j].at < window; j-- {
				if v, ok := day[j].values[l][HarborpulseLampFaultKey]; ok && v > level {
					n++
				}
			}
			if n >= need {
				fired[l] = true
			}
		}
	}
	if !fired[harborFlickerLamp] || len(fired) != 1 {
		t.Fatalf("lamp-flicker fires for %v; want %s alone", fired, harborFlickerLamp)
	}
}

func TestHarborpulseOutageSilencesExactlyEastQuayLightsAndChargers(t *testing.T) {
	rules := harborRules(t)
	for _, tok := range []string{HarborpulseLightSilentRule, HarborpulseChargerOfflineRule} {
		timeout := rules[tok].dur(t, "timeout")
		if timeout <= harborCadence*2 {
			t.Fatalf("%s times out after %s, too close to the %s cadence", tok, timeout, harborCadence)
		}
	}
	timeout := rules[HarborpulseLightSilentRule].dur(t, "timeout")
	day := simulateHarborDay(t, true)
	last := map[string]time.Duration{}
	silent := map[string]bool{}
	for _, s := range day {
		for _, d := range harborLayout() {
			if _, ok := s.values[d.token]; ok {
				last[d.token] = s.at
			} else if s.at-last[d.token] > timeout {
				silent[d.token] = true
			}
		}
	}
	want := append(harborTokens(harborLight, HarborpulseEastQuay), harborTokens(harborCharger, HarborpulseEastQuay)...)
	var got []string
	for tok := range silent {
		got = append(got, tok)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("silent past the absence timeout: %v; want exactly %v", got, want)
	}
}

func TestHarborpulseChargersFlushTheirOutageReadingsOnRestore(t *testing.T) {
	m := newHarborModel(1)
	base := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	silentTicks := map[string]int{}
	flushed := map[string][]harborBuffered{}
	for at := time.Duration(0); at <= harborpulseDayLength; at += harborCadence {
		m.advanceTo(at)
		for _, c := range harborTokens(harborCharger, "") {
			if m.reading(c, at, base.Add(at)) == nil {
				silentTicks[c]++
			}
		}
		for _, b := range m.takeBackfill(at) {
			if harborOutage(HarborpulseEastQuay, harborHour(at)) {
				t.Fatalf("a buffer was handed over at %.2fh, while the feeder is still down", harborHour(at))
			}
			flushed[b.token] = append(flushed[b.token], b.readings...)
		}
	}
	for _, c := range harborTokens(harborCharger, "") {
		east := m.devices[c].quay == HarborpulseEastQuay
		if east != (silentTicks[c] > 0) || len(flushed[c]) != silentTicks[c] {
			t.Errorf("%s: silent %d ticks, flushed %d readings; every silent reading must come back, and only East Quay's go silent",
				c, silentTicks[c], len(flushed[c]))
		}
		for i, r := range flushed[c] {
			h := harborHour(r.at.Sub(base))
			if !harborOutage(HarborpulseEastQuay, h) || (i > 0 && !r.at.After(flushed[c][i-1].at)) {
				t.Errorf("%s reading %d stamped %s (%.2fh): not inside the outage, or out of order", c, i, r.at, h)
			}
		}
	}
}

func TestHarborpulseDerateCoolsTheHotCharger(t *testing.T) {
	r := harborRules(t)[HarborpulseConnectorHotRule]
	level, window := r.num(t, "threshold"), r.dur(t, "window")
	run := func(obey bool) (fired map[string]bool, hotAtEnd bool) {
		fired = map[string]bool{}
		var seen []harborSample
		for _, s := range simulateHarborDay(t, obey) {
			seen = append(seen, s)
			for _, c := range harborTokens(harborCharger, "") {
				if avg, ok := harborSlidingAvg(seen, c, HarborpulseConnectorKey, window); ok && avg > level {
					fired[c] = true
					if c == harborHotCharger && s.hour >= 15.3 && s.hour < 15.5 {
						hotAtEnd = true
					}
				}
			}
		}
		return fired, hotAtEnd
	}
	fired, hot := run(true)
	if !fired[harborHotCharger] || len(fired) != 1 {
		t.Fatalf("connector-hot fires for %v; want %s alone", fired, harborHotCharger)
	}
	if hot {
		t.Fatal("the derated charger is still above the limit at the end of its fault; the derate did nothing")
	}
	if _, hotWithout := run(false); !hotWithout {
		t.Fatal("control: without the derate the charger cools anyway, so the obeyed run proves nothing about the command")
	}
}

func TestHarborpulsePeakDemandIsTheFerryTerminalsMorning(t *testing.T) {
	r := harborRules(t)[HarborpulsePeakDemandRule]
	level, window := r.num(t, "threshold"), r.dur(t, "window")
	var seen []harborSample
	fired := map[string]bool{}
	for _, s := range simulateHarborDay(t, true) {
		seen = append(seen, s)
		for _, mt := range harborTokens(harborMeter, "") {
			if avg, ok := harborSlidingAvg(seen, mt, HarborpulseMeterKwKey, window); ok && avg > level {
				fired[mt] = true
				if s.hour < 8.5 || s.hour > 10.25 {
					t.Errorf("%s over its cap at %.2fh, outside the commute", mt, s.hour)
				}
			}
		}
	}
	if !fired[harborFerryMeter] || len(fired) != 1 {
		t.Fatalf("peak-demand fires for %v; want %s alone", fired, harborFerryMeter)
	}
}

func TestHarborpulseTruckParksInTheFloodPlainAtTheSurge(t *testing.T) {
	fences := map[string][][2]float64{}
	for _, g := range harborpulseGeoFences() {
		fences[g.Token] = g.Ring
	}
	inside := func(ring [][2]float64, f Fix) bool {
		minLon, maxLon, minLat, maxLat := ring[0][0], ring[0][0], ring[0][1], ring[0][1]
		for _, p := range ring {
			minLon, maxLon = min(minLon, p[0]), max(maxLon, p[0])
			minLat, maxLat = min(minLat, p[1]), max(maxLat, p[1])
		}
		return f.Longitude >= minLon && f.Longitude <= maxLon && f.Latitude >= minLat && f.Latitude <= maxLat
	}
	for h := 0.0; h < 24; h += 0.25 {
		f := harborTruckFix(h)
		inPlain, inDepot := inside(fences[HarborpulseFloodPlainFence], f), inside(fences[HarborpulseDepotFence], f)
		switch {
		case h >= 18 && h < 21.5 && !inPlain:
			t.Errorf("at %.2fh the truck is outside the flood plain", h)
		case (h < 17 || h >= 22.5) && !inDepot:
			t.Errorf("at %.2fh the truck is outside the depot", h)
		case inPlain && inDepot:
			t.Errorf("at %.2fh the truck is in both fences; they must not overlap", h)
		}
	}
	m := newHarborModel(1)
	if _, ok := m.fix("hp-gauge-east-1", 0); ok {
		t.Fatal("a gauge reported a position; only the truck moves")
	}
}

func TestHarborpulseCommandsAreAppliedOrRefused(t *testing.T) {
	m := newHarborModel(1)
	cases := []struct {
		device, key, payload string
		ok                   bool
	}{
		{"hp-pump-north", HarborpulseStartPumpKey, `{"duty_pct":100}`, true},
		{"hp-pump-north", HarborpulseStartPumpKey, `{}`, false},
		{"hp-pump-north", HarborpulseStartPumpKey, `{"duty_pct":20}`, false},
		{"hp-pump-north", HarborpulseStopPumpKey, ``, true},
		{"hp-pump-north", HarborpulseDerateKey, `{"max_kw":11}`, false},
		{"hp-charger-north-1", HarborpulseDerateKey, `{"max_kw":11}`, true},
		{"hp-charger-north-1", HarborpulseDerateKey, `{"max_kw":"eleven"}`, false},
		{"hp-light-north-1", HarborpulseSetDimKey, `{"level_pct":40}`, true},
		{"hp-light-north-1", HarborpulseSetDimKey, `{"level_pct":140}`, false},
		{"hp-light-north-1", "reboot", `{}`, false},
		{"hp-nope", HarborpulseSetDimKey, `{"level_pct":40}`, false},
	}
	for _, c := range cases {
		if got := m.command(c.device, c.key, json.RawMessage(c.payload)); got != c.ok {
			t.Errorf("%s %s %s: accepted=%v, want %v", c.device, c.key, c.payload, got, c.ok)
		}
	}
	at := 13 * time.Minute // 19:00, inside the air lock
	m.advanceTo(at)
	if m.command(harborAirLockPump, HarborpulseStopPumpKey, nil) {
		t.Fatal("an air-locked pump acknowledged stop-pump; its contactor is latched")
	}
	if v := m.reading("hp-charger-north-1", at, time.Now()); v[HarborpulseMaxKwKey] != 11 {
		t.Fatalf("the derate was acknowledged but the charger reports a cap of %v", v[HarborpulseMaxKwKey])
	}
	if v := m.reading("hp-light-north-1", at, time.Now()); v[HarborpulseDimKey] != 40 || v[HarborpulseLampPowerKey] != 32 {
		t.Fatalf("the dim was acknowledged but the lamp reports %v", v)
	}
}

func TestHarborpulseDayClock(t *testing.T) {
	if h := harborHour(0); h != harborpulseStartHour {
		t.Fatalf("the day opens at %.2fh, want %.2f", h, harborpulseStartHour)
	}
	if h := harborHour(harborpulseDayLength); h != harborpulseStartHour {
		t.Fatalf("one day later it is %.2fh; the day must loop", h)
	}
	if d := harborDay(18 * time.Minute); d != 1 {
		t.Fatalf("18 minutes in (midnight) is day %d, want 1", d)
	}
	// Midnight resets derates, so the next day's overheat beat can happen again.
	m := newHarborModel(1)
	m.command(harborHotCharger, HarborpulseDerateKey, json.RawMessage(`{"max_kw":11}`))
	m.advanceTo(19 * time.Minute)
	if m.cap[harborHotCharger] != harborChargerMaxKw {
		t.Fatalf("the derate survived midnight (cap %.1f)", m.cap[harborHotCharger])
	}
}
