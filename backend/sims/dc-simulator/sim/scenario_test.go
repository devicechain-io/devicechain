// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScenarioOptionsAreRefusedByAScenarioWithNoDay(t *testing.T) {
	if err := ConfigureScenario(NewDevicepulse(1, Load{}), ScenarioOptions{}); err != nil {
		t.Fatalf("zero options were refused: %v", err)
	}
	if err := ConfigureScenario(NewDevicepulse(1, Load{}), ScenarioOptions{DayLength: time.Hour}); err == nil {
		t.Fatal("--day-length was accepted by a scenario with no day")
	}
	if err := ConfigureScenario(NewTowerpulse(1, Load{}), ScenarioOptions{Defects: []string{HarborpulseDefectRefuseDerate}}); err == nil {
		t.Fatal("a harborpulse defect was accepted by another scenario")
	}
}

func TestHarborpulseDayLengthAndDefectsAreChecked(t *testing.T) {
	for _, c := range []struct {
		o  ScenarioOptions
		ok bool
	}{
		{ScenarioOptions{DayLength: 24 * time.Minute}, true},
		{ScenarioOptions{DayLength: 48 * time.Minute}, true},
		{ScenarioOptions{DayLength: 12 * time.Minute}, false},
		{ScenarioOptions{DayLength: 25 * time.Hour}, false},
		{ScenarioOptions{Defects: []string{HarborpulseDefectDropBackfill, " " + HarborpulseDefectDropBackfill}}, true},
		{ScenarioOptions{Defects: []string{"drop-backfil"}}, false},
	} {
		err := ConfigureScenario(NewHarborpulse(1, Load{}), c.o)
		if (err == nil) != c.ok {
			t.Errorf("%+v: err %v, want ok=%v", c.o, err, c.ok)
		}
	}
}

func TestHarborpulseScenarioClockMapsTheDay(t *testing.T) {
	s := NewHarborpulse(1, Load{}).(*harborpulse)
	if err := s.ConfigureScenario(ScenarioOptions{DayLength: 48 * time.Minute, Defects: []string{HarborpulseDefectRefuseDerate}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	before := s.ScenarioClock(start)
	if before.Started || before.StartedAt != "" || before.SimTime != "06:00" {
		t.Fatalf("before the first tick: %+v", before)
	}
	s.ensureModel()
	s.started = start
	// 18:00 is twelve sim hours in: half of a 48-minute day.
	c := s.ScenarioClock(start.Add(24 * time.Minute))
	if !c.Started || c.SimTime != "18:00" || c.Day != 0 || c.DayLengthSeconds != 2880 || c.StartHour != 6 {
		t.Fatalf("at 24 wall minutes: %+v", c)
	}
	if !strings.Contains(c.Beat, "storm") {
		t.Fatalf("18:00 is in the storm, beat %q", c.Beat)
	}
	if len(c.Defects) != 1 || c.Defects[0] != HarborpulseDefectRefuseDerate {
		t.Fatalf("defects %v", c.Defects)
	}
	if next := s.ScenarioClock(start.Add(48 * time.Minute)); next.Day != 1 || next.SimTime != "06:00" {
		t.Fatalf("one day later: %+v", next)
	}
	if q := harborBeatAt(4); q != "night" {
		t.Fatalf("04:00 is %q", q)
	}
	if q := harborBeatAt(12.5); q != "quiet" {
		t.Fatalf("12:30 is %q", q)
	}
}

// /status carries the clock for harborpulse and nothing new for a scenario without a day.
func TestStatusCarriesTheScenarioClockOnlyForAScenarioWithADay(t *testing.T) {
	for _, c := range []struct {
		s    Sim
		want bool
	}{{NewHarborpulse(1, Load{}), true}, {NewDevicepulse(1, Load{}), false}} {
		lc := NewLifecycle(c.s, &Runtime{})
		mux := http.NewServeMux()
		NewControlServer(lc, lc.rt).Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
		var body map[string]json.RawMessage
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, has := body["scenario"]; has != c.want {
			t.Errorf("%T: /status has scenario=%v, want %v", c.s, has, c.want)
		}
	}
}

// Each planted defect breaks the one thing it is named for, in the model the day is played from.
func TestHarborpulseDefectsBreakWhatTheyAreNamedFor(t *testing.T) {
	derate := json.RawMessage(`{"max_kw": 11}`)
	if !newHarborModel(1).command(harborHotCharger, HarborpulseDerateKey, derate) {
		t.Fatal("a clean charger refused derate")
	}
	if newHarborModelWith(1, harborpulseDayLength, harborDefects{refuseDerate: true}).command(harborHotCharger, HarborpulseDerateKey, derate) {
		t.Fatal("refuse-derate accepted derate")
	}

	clean, basin := newHarborModel(1), newHarborModelWith(1, harborpulseDayLength, harborDefects{airLockBasin: true})
	if !clean.airLocked(harborAirLockPump, 19.5) || clean.airLocked(harborBasinPump, 19.5) {
		t.Fatal("the clean air lock is not on the East Quay pump alone")
	}
	if !basin.airLocked(harborBasinPump, 19.5) || basin.airLocked(harborAirLockPump, 19.5) {
		t.Fatal("air-lock-basin did not move the air lock to the Basin pump")
	}

	// 20:00 is inside the outage: 14 sim hours after 06:00, at the default day.
	inOutage := time.Duration(14.0 / 24 * float64(harborpulseDayLength))
	now := time.Now()
	if newHarborModel(1).reading(harborSilencedLamp, inOutage, now) == nil {
		t.Fatal("a clean North Quay lamp is silent in the East Quay outage")
	}
	if newHarborModelWith(1, harborpulseDayLength, harborDefects{silenceNorthLamp: true}).reading(harborSilencedLamp, inOutage, now) != nil {
		t.Fatal("silence-north-lamp left the lamp reporting")
	}

	var buf []harborBuffered
	for i := 0; i < 18; i++ {
		buf = append(buf, harborBuffered{at: now.Add(time.Duration(i) * time.Second)})
	}
	if got := len(harborDropEveryNth(buf, harborDropNth)); got != 18-3 {
		t.Fatalf("drop-backfill kept %d of 18, want 15", got)
	}

	keys := map[string]bool{}
	s := NewHarborpulse(1, Load{}).(*harborpulse)
	if err := s.ConfigureScenario(ScenarioOptions{Defects: []string{HarborpulseDefectFloodSplitKey}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Manifest().Profiles {
		if p.Token != HarborpulseGaugeProfile {
			continue
		}
		for _, r := range p.DetectionRules {
			for _, k := range []string{HarborpulseFloodAlarmKey, HarborpulseFloodSplitAlarmKey} {
				if strings.Contains(r.Definition, `"alarmKey":"`+k+`"`) {
					keys[k] = true
				}
			}
		}
	}
	if !keys[HarborpulseFloodSplitAlarmKey] || !keys[HarborpulseFloodAlarmKey] {
		t.Fatalf("flood-split-key: gauge alarm keys %v, want the flood key and the split one", keys)
	}
}

// The ledger counts what the chargers buffered, so it is the expectation the stored readings
// are held to, and drop-backfill does not shrink it.
func TestHarborpulseLedgerCountsWhatWasBuffered(t *testing.T) {
	for _, drop := range []bool{false, true} {
		m := newHarborModelWith(1, harborpulseDayLength, harborDefects{dropBackfill: drop})
		base := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
		delivered := map[string]int{}
		for at := time.Duration(0); at <= harborpulseDayLength; at += 5 * time.Second {
			m.advanceTo(at)
			for _, d := range harborLayout() {
				if d.kind == harborCharger {
					m.reading(d.token, at, base.Add(at))
				}
			}
			for _, b := range m.takeBackfill(at) {
				delivered[b.token] += len(b.readings)
			}
		}
		ledger := m.backfillLedger()
		if len(ledger) != 2 {
			t.Fatalf("drop=%v: ledger %+v, want the two East Quay chargers", drop, ledger)
		}
		for _, e := range ledger {
			if !strings.Contains(e.Device, "-east-") || e.Buffered < 10 || e.Day != 0 {
				t.Fatalf("drop=%v: ledger entry %+v", drop, e)
			}
			want := e.Buffered
			if drop {
				want = e.Buffered - e.Buffered/harborDropNth
			}
			if delivered[e.Device] != want {
				t.Fatalf("drop=%v: %s buffered %d, delivered %d, want %d", drop, e.Device, e.Buffered, delivered[e.Device], want)
			}
		}
	}
}
