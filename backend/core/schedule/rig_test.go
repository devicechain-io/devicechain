// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schedule

// The schedule rig. It judges the trigger arithmetic against ORACLES that share no code with
// it, over a fleet of 50,000 simulated fixtures and over every day of a year in zones chosen
// for their daylight-saving oddities, and it carries planted defects to prove the judgement
// can fail: a check that has never been shown to fail is worth nothing.
//
//   - Solar: the oracle finds each fixture's sunset by scanning the sun's altitude with the
//     Spencer Fourier-series declination and equation of time, which is a different method
//     from the sunrise equation core/solar uses. The planted defect picks the solar day from
//     the UTC date instead of the fixture's own day.
//   - Zones: the oracle finds each local wall time by scanning UTC minutes against the
//     standard library's own conversion, which shares nothing with maintwindow.ResolveLocal.
//     The planted defect reads the wall time in UTC (the wrong-time-zone defect).

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

const rigFleet = 50000

type fixture struct{ lat, lon float64 }

func rigFixtures() []fixture {
	rng := rand.New(rand.NewSource(20450607))
	out := make([]fixture, rigFleet)
	for i := range out {
		// Sub-polar latitudes, every longitude: the fleet spans all 24 hours of dusk.
		out[i] = fixture{lat: -55 + rng.Float64()*115, lon: -180 + rng.Float64()*360}
	}
	return out
}

// sunAltitude is the sun's apparent altitude in degrees, from Spencer's (1971) series.
func sunAltitude(t time.Time, lat, lon float64) float64 {
	t = t.UTC()
	doy := float64(t.YearDay()-1) + (float64(t.Hour())-12)/24 + float64(t.Minute())/1440
	g := 2 * math.Pi / 365 * doy
	decl := 0.006918 - 0.399912*math.Cos(g) + 0.070257*math.Sin(g) - 0.006758*math.Cos(2*g) +
		0.000907*math.Sin(2*g) - 0.002697*math.Cos(3*g) + 0.00148*math.Sin(3*g)
	eqt := 229.18 * (0.000075 + 0.001868*math.Cos(g) - 0.032077*math.Sin(g) -
		0.014615*math.Cos(2*g) - 0.040849*math.Sin(2*g))
	minutes := float64(t.Hour()*60+t.Minute()) + float64(t.Second())/60
	solarTime := minutes + eqt + 4*lon
	ha := (solarTime/4 - 180) * math.Pi / 180
	phi := lat * math.Pi / 180
	sinAlt := math.Sin(phi)*math.Sin(decl) + math.Cos(phi)*math.Cos(decl)*math.Cos(ha)
	return math.Asin(sinAlt) * 180 / math.Pi
}

// oracleSunset scans the 24 hours around the fixture's local solar noon on the given civil
// date for the altitude crossing -0.833 degrees downwards. ok is false at polar day/night.
func oracleSunset(y int, m time.Month, d int, f fixture) (time.Time, bool) {
	utcNoon := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	localNoon := utcNoon.Add(-time.Duration(f.lon / 15 * float64(time.Hour)))
	const coarse = 10
	prev := sunAltitude(localNoon, f.lat, f.lon)
	for i := coarse; i <= 12*60; i += coarse {
		t := localNoon.Add(time.Duration(i) * time.Minute)
		if alt := sunAltitude(t, f.lat, f.lon); prev > -0.833 && alt <= -0.833 {
			for j := coarse - 1; j >= 0; j-- { // refine within the bracket, minute by minute
				u := t.Add(-time.Duration(coarse-j) * time.Minute)
				if sunAltitude(u, f.lat, f.lon) > -0.833 {
					return u.Add(time.Minute), true
				}
			}
			return t, true
		}
		prev = sunAltitude(t, f.lat, f.lon)
	}
	return time.Time{}, false
}

func sunsetTrigger(t *testing.T) *Trigger {
	t.Helper()
	return must(t, Spec{Kind: KindSolar, Zone: "UTC", Event: Sunset})
}

// utcDayDefect is the planted defect: the scheduler asks for "the sunset that falls on UTC
// date D" instead of "the sunset of the fixture's own solar day D". For a fixture whose dusk
// lands after 00:00 UTC (the Americas) the sunset of its day D falls on UTC date D+1, so the
// defect hands it the PREVIOUS evening's dusk, a day early.
func utcDayDefect(day string, f fixture) string {
	d, _ := parseCivil(day)
	ev, ok := oracleSunset(d.Y, d.M, d.D, f)
	if ok && ev.UTC().Format("2006-01-02") != day {
		return d.addDays(-1).String()
	}
	return day
}

type rigVerdict struct{ checked, skipped, wrong int }

func judgeSolar(t *testing.T, tr *Trigger, day string, fleet []fixture, planted bool) rigVerdict {
	t.Helper()
	d, _ := parseCivil(day)
	var v rigVerdict
	for _, f := range fleet {
		want, ok := oracleSunset(d.Y, d.M, d.D, f)
		useDay := day
		if planted {
			useDay = utcDayDefect(day, f)
		}
		got, err := tr.SolarFire(Cycle{Day: useDay}, f.lat, f.lon)
		if err != nil {
			t.Fatal(err)
		}
		if got.Skipped != "" || !ok {
			// A polar answer is judged only against agreement: the two methods may disagree
			// within the last minutes of a polar day; those fixtures are not counted.
			v.skipped++
			continue
		}
		v.checked++
		diff := got.At.Sub(want)
		if diff < 0 {
			diff = -diff
		}
		if diff > 5*time.Minute {
			v.wrong++
		}
	}
	return v
}

func TestRigSolarFleetAtItsOwnDusk(t *testing.T) {
	if testing.Short() {
		t.Skip("the 50k-fixture rig is skipped in -short")
	}
	fleet := rigFixtures()
	tr := sunsetTrigger(t)
	// Both solstices and both equinoxes: the dusk spread and its geometry differ on each.
	for _, day := range []string{"2026-03-20", "2026-06-21", "2026-09-23", "2026-12-21"} {
		v := judgeSolar(t, tr, day, fleet, false)
		if v.checked < rigFleet*8/10 {
			t.Fatalf("%s: only %d of %d fixtures were judged", day, v.checked, rigFleet)
		}
		// The two methods agree to a few minutes; a handful of near-polar fixtures sit on
		// the edge of the method difference, so the bar is a rate, not zero.
		if rate := float64(v.wrong) / float64(v.checked); rate > 0.002 {
			t.Errorf("%s: %d of %d fixtures dimmed more than 5 minutes from their own dusk (%.3f%%)",
				day, v.wrong, v.checked, rate*100)
		}
	}
}

// The negative control: with the UTC-day defect planted the same judgement must fail loudly,
// on a large share of the fleet. If this ever passes, the rig above proves nothing.
func TestRigSolarCatchesAPlantedUTCDayDefect(t *testing.T) {
	if testing.Short() {
		t.Skip("the 50k-fixture rig is skipped in -short")
	}
	fleet := rigFixtures()
	tr := sunsetTrigger(t)
	v := judgeSolar(t, tr, "2026-06-21", fleet, true)
	if rate := float64(v.wrong) / float64(max(v.checked, 1)); rate < 0.15 {
		t.Fatalf("the planted UTC-day defect was caught on only %d of %d fixtures (%.1f%%): the rig cannot see it",
			v.wrong, v.checked, rate*100)
	}
}

// ---------------------------------------------------------------------------------------
// Zones and daylight saving
// ---------------------------------------------------------------------------------------

// oracleWallTimes finds every instant in [from, to) at which the zone's own clock reads
// hh:mm, by scanning UTC minutes against the standard library's conversion, and applies the
// platform's stated rules: the first occurrence of a repeated time, and for a time that does
// not exist the instant the clock jumps over it.
func oracleWallTimes(zone *time.Location, hh, mm int, from, to time.Time) []time.Time {
	var out []time.Time
	prevDay := -1
	reads := map[[3]int]bool{}
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		l := t.In(zone)
		key := [3]int{l.Year(), l.YearDay(), 0}
		if l.Hour() == hh && l.Minute() == mm && !reads[key] {
			reads[key] = true
			out = append(out, t)
		}
		if l.YearDay() != prevDay {
			prevDay = l.YearDay()
		}
	}
	// A wall time that never occurred on a local day (the gap) fires at the jump: the first
	// instant of that local day whose clock reads at-or-after hh:mm.
	days := map[[2]int]bool{}
	for t := from; t.Before(to); t = t.Add(time.Hour) {
		l := t.In(zone)
		days[[2]int{l.Year(), l.YearDay()}] = true
	}
	have := map[[2]int]bool{}
	for _, t := range out {
		l := t.In(zone)
		have[[2]int{l.Year(), l.YearDay()}] = true
	}
	for day := range days {
		if have[day] {
			continue
		}
		var first time.Time
		for t := from; t.Before(to); t = t.Add(time.Minute) {
			l := t.In(zone)
			if [2]int{l.Year(), l.YearDay()} != day {
				continue
			}
			if l.Hour()*60+l.Minute() >= hh*60+mm {
				first = t
				break
			}
		}
		if !first.IsZero() {
			out = append(out, first)
		}
	}
	return out
}

func judgeZone(t *testing.T, zoneName, cron string, hh, mm int, planted bool) (checked, wrong int) {
	t.Helper()
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	want := map[time.Time]bool{}
	// Scan a padded range so no local day at the edge is judged on a part of itself, then keep
	// what falls in [from, to).
	for _, w := range oracleWallTimes(zone, hh, mm, from.AddDate(0, 0, -3), to.AddDate(0, 0, 3)) {
		if !w.Before(from) && w.Before(to) {
			want[w.UTC()] = true
		}
	}
	specZone := zoneName
	if planted {
		specZone = "UTC" // the wrong-time-zone defect: the wall time read in the wrong zone
	}
	tr := must(t, Spec{Kind: KindCron, Zone: specZone, Cron: cron})
	cycles, err := tr.Cycles(from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := map[time.Time]bool{}
	for _, c := range cycles {
		got[c.At.UTC()] = true
	}
	for w := range want {
		checked++
		if !got[w] {
			wrong++
		}
	}
	for g := range got {
		if !want[g] {
			wrong++
		}
	}
	return checked, wrong
}

// Zones with daylight-saving behaviour that breaks naive arithmetic: the common 1-hour
// spring-forward and fall-back, a half-hour shift, a southern-hemisphere reversal, a zone
// that skipped a whole calendar day, and a zone with no daylight saving at all.
var rigZones = []string{
	"America/New_York", "Europe/Berlin", "Australia/Lord_Howe", "Australia/Sydney",
	"Pacific/Apia", "Asia/Kolkata", "America/Sao_Paulo", "Africa/Casablanca", "Europe/London",
}

func TestRigEveryWallTimeEveryDayOfAYear(t *testing.T) {
	if testing.Short() {
		t.Skip("the year-long zone rig is skipped in -short")
	}
	for _, z := range rigZones {
		// 02:30 lands in a spring-forward gap in several; 01:30 in a fall-back overlap in
		// several; 20:00 is the dusk-dimming hour.
		for _, c := range []struct {
			cron   string
			hh, mm int
		}{{"30 2 * * *", 2, 30}, {"30 1 * * *", 1, 30}, {"0 20 * * *", 20, 0}, {"15 0 * * *", 0, 15}} {
			checked, wrong := judgeZone(t, z, c.cron, c.hh, c.mm, false)
			if checked < 300 {
				t.Fatalf("%s %q: only %d firings judged", z, c.cron, checked)
			}
			if wrong != 0 {
				t.Errorf("%s %q: %d disagreements with the oracle over %d firings", z, c.cron, wrong, checked)
			}
		}
	}
}

func TestRigCatchesAPlantedWrongTimeZone(t *testing.T) {
	if testing.Short() {
		t.Skip("the year-long zone rig is skipped in -short")
	}
	for _, z := range []string{"America/New_York", "Europe/Berlin", "Australia/Lord_Howe"} {
		_, wrong := judgeZone(t, z, "0 20 * * *", 20, 0, true)
		if wrong == 0 {
			t.Errorf("%s: the planted wrong-time-zone defect went unnoticed", z)
		}
	}
	// And a DST-only defect: reading the wall time at the zone's STANDARD offset all year
	// (the classic "forgot daylight saving") must be caught on the summer half of the year.
	zone, _ := time.LoadLocation("America/New_York")
	std := time.FixedZone("EST", -5*3600)
	want := oracleWallTimes(zone, 20, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	disagree := 0
	have := map[time.Time]bool{}
	for _, w := range want {
		have[w.UTC()] = true
	}
	for d := time.Date(2026, 1, 1, 0, 0, 0, 0, std); d.Year() == 2026; d = d.AddDate(0, 0, 1) {
		if !have[time.Date(d.Year(), d.Month(), d.Day(), 20, 0, 0, 0, std).UTC()] {
			disagree++
		}
	}
	if disagree < 100 {
		t.Fatalf("a standard-offset-all-year defect disagreed on only %d days; the oracle cannot see DST", disagree)
	}
}
