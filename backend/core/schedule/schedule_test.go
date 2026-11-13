// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schedule

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, s Spec) *Trigger {
	t.Helper()
	tr, err := New(s)
	if err != nil {
		t.Fatalf("New(%+v): %v", s, err)
	}
	return tr
}

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func fires(t *testing.T, tr *Trigger, from, to string) []string {
	t.Helper()
	cs, err := tr.Cycles(at(from), at(to))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.At.Format(time.RFC3339))
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestCronInZone(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "Europe/Berlin", Cron: "30 18 * * *"})
	// 18:30 CET = 17:30Z in winter, 16:30Z in summer.
	eq(t, fires(t, tr, "2026-01-10T00:00:00Z", "2026-01-12T00:00:00Z"),
		[]string{"2026-01-10T17:30:00Z", "2026-01-11T17:30:00Z"})
	eq(t, fires(t, tr, "2026-07-10T00:00:00Z", "2026-07-11T00:00:00Z"),
		[]string{"2026-07-10T16:30:00Z"})
}

// The planted defect for the schedule rig reads a local wall time in UTC. A trigger set for
// 02:30 local must keep its wall-clock meaning across the changeover, not its UTC one.
func TestCronKeepsWallClockAcrossSpringForward(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "America/New_York", Cron: "0 20 * * *"})
	// US changeover 2026-03-08: 20:00 EST = 01:00Z, 20:00 EDT = 00:00Z next day.
	eq(t, fires(t, tr, "2026-03-07T12:00:00Z", "2026-03-10T00:00:00Z"),
		[]string{"2026-03-08T01:00:00Z", "2026-03-09T00:00:00Z"})
}

func TestSpringForwardGapFiresAtTheJumpOnce(t *testing.T) {
	// 02:30 does not exist on 2026-03-08 in New York; the clock jumps 02:00 -> 03:00 (07:00Z).
	tr := must(t, Spec{Kind: KindCron, Zone: "America/New_York", Cron: "30 2 * * *"})
	eq(t, fires(t, tr, "2026-03-08T00:00:00Z", "2026-03-09T00:00:00Z"), []string{"2026-03-08T07:00:00Z"})
	// Two wall times inside the gap are ONE firing.
	both := must(t, Spec{Kind: KindCron, Zone: "America/New_York", Cron: "15,45 2 * * *"})
	eq(t, fires(t, both, "2026-03-08T00:00:00Z", "2026-03-09T00:00:00Z"), []string{"2026-03-08T07:00:00Z"})
}

func TestFallBackOverlapFiresOnce(t *testing.T) {
	// 01:30 occurs twice on 2026-11-01 in New York; the schedule fires at the first (05:30Z).
	tr := must(t, Spec{Kind: KindCron, Zone: "America/New_York", Cron: "30 1 * * *"})
	eq(t, fires(t, tr, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"), []string{"2026-11-01T05:30:00Z"})
}

func TestCronDayFieldsAreOrWhenBothRestricted(t *testing.T) {
	// 1st of the month OR Monday.
	tr := must(t, Spec{Kind: KindCron, Zone: "UTC", Cron: "0 6 1 * 1"})
	got := fires(t, tr, "2026-11-01T00:00:00Z", "2026-11-10T00:00:00Z")
	eq(t, got, []string{"2026-11-01T06:00:00Z", "2026-11-02T06:00:00Z", "2026-11-09T06:00:00Z"})
}

func TestCronStepsRangesListsAndSundayAlias(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "UTC", Cron: "*/20 8-9 * * 7"})
	// 2026-11-08 is a Sunday.
	eq(t, fires(t, tr, "2026-11-08T00:00:00Z", "2026-11-09T00:00:00Z"), []string{
		"2026-11-08T08:00:00Z", "2026-11-08T08:20:00Z", "2026-11-08T08:40:00Z",
		"2026-11-08T09:00:00Z", "2026-11-08T09:20:00Z", "2026-11-08T09:40:00Z"})
	eq(t, fires(t, tr, "2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z"), nil)
}

func TestCalendarAndHolidays(t *testing.T) {
	tr := must(t, Spec{Kind: KindCalendar, Zone: "Europe/London",
		Dates: []string{"2026-12-24", "2026-12-25", "2026-12-26"}, Times: []string{"07:00", "17:30"},
		Holidays: []string{"2026-12-25"}})
	eq(t, fires(t, tr, "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"), []string{
		"2026-12-24T07:00:00Z", "2026-12-24T17:30:00Z", "2026-12-26T07:00:00Z", "2026-12-26T17:30:00Z"})
}

func TestHolidaySuppressesAndNeverMoves(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "UTC", Cron: "0 9 * * *", Holidays: []string{"2026-11-02"}})
	eq(t, fires(t, tr, "2026-11-01T00:00:00Z", "2026-11-04T00:00:00Z"),
		[]string{"2026-11-01T09:00:00Z", "2026-11-03T09:00:00Z"})
}

func TestHolidayIsALocalDate(t *testing.T) {
	// 23:30 local on the holiday in Sydney is 12:30Z the same UTC date; 00:30 local the
	// next morning is on the previous UTC date. Only the local date decides.
	tr := must(t, Spec{Kind: KindCron, Zone: "Australia/Sydney", Cron: "30 0,23 * * *",
		Holidays: []string{"2026-11-10"}})
	got := fires(t, tr, "2026-11-09T00:00:00Z", "2026-11-11T14:00:00Z")
	for _, g := range got {
		if g == "2026-11-09T13:30:00Z" || g == "2026-11-10T12:30:00Z" {
			t.Fatalf("fired on the local holiday: %s in %v", g, got)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected the non-holiday firings")
	}
}

func TestSolarCyclesAreDaysAndPerPlace(t *testing.T) {
	tr := must(t, Spec{Kind: KindSolar, Zone: "Europe/London", Event: Sunset, OffsetMinutes: -30,
		Holidays: []string{"2026-06-22"}})
	cs, err := tr.Cycles(at("2026-06-20T00:00:00Z"), at("2026-06-24T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	var days []string
	for _, c := range cs {
		days = append(days, c.Day)
	}
	// London midnight is 23:00Z the day before in summer: the local days 21st, 23rd and 24th
	// start inside the range; the 22nd is a holiday and the 20th began before it.
	if strings.Join(days, ",") != "2026-06-21,2026-06-23,2026-06-24" {
		t.Fatalf("days = %v", days)
	}
	f, err := tr.SolarFire(Cycle{Day: "2026-06-21"}, 51.5074, -0.1278)
	if err != nil || f.Skipped != "" {
		t.Fatalf("fire = %+v, %v", f, err)
	}
	// Sunset 20:21Z minus 30 minutes.
	want := at("2026-06-21T19:51:00Z")
	if d := f.At.Sub(want); d < -3*time.Minute || d > 3*time.Minute {
		t.Fatalf("fire = %s, want about %s", f.At, want)
	}
	// Further west fires later.
	w, _ := tr.SolarFire(Cycle{Day: "2026-06-21"}, 51.5074, -5)
	if !w.At.After(f.At) {
		t.Fatalf("west %s should fire after %s", w.At, f.At)
	}
}

func TestSolarPolarIsSkippedLoudly(t *testing.T) {
	tr := must(t, Spec{Kind: KindSolar, Zone: "UTC", Event: Sunset})
	f, err := tr.SolarFire(Cycle{Day: "2026-06-21"}, 78.2, 15.6)
	if err != nil || f.Skipped != "POLAR_DAY" || !f.At.IsZero() {
		t.Fatalf("fire = %+v, %v; want a POLAR_DAY skip and no instant", f, err)
	}
}

func TestSolarFireOnWrongKindIsRefused(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "UTC", Cron: "* * * * *"})
	if _, err := tr.SolarFire(Cycle{Day: "2026-06-21"}, 1, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidation(t *testing.T) {
	bad := []Spec{
		{Kind: KindCron, Zone: "", Cron: "* * * * *"},
		{Kind: KindCron, Zone: "Local", Cron: "* * * * *"},
		{Kind: KindCron, Zone: "Not/AZone", Cron: "* * * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "* * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "60 * * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "*/0 * * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "5-1 * * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "@daily"},
		{Kind: KindCron, Zone: "UTC", Cron: "* * * * MON"},
		{Kind: KindCron, Zone: "UTC", Cron: "* * * * *", Event: Sunset},
		{Kind: KindCalendar, Zone: "UTC", Times: []string{"07:00"}},
		{Kind: KindCalendar, Zone: "UTC", Dates: []string{"2026-02-30"}, Times: []string{"07:00"}},
		{Kind: KindCalendar, Zone: "UTC", Dates: []string{"2026-02-01"}, Times: []string{"7:00"}},
		{Kind: KindSolar, Zone: "UTC", Event: "NOON"},
		{Kind: KindSolar, Zone: "UTC", Event: Sunset, OffsetMinutes: 721},
		{Kind: KindSolar, Zone: "UTC", Event: Sunset, Cron: "* * * * *"},
		{Kind: KindCron, Zone: "UTC", Cron: "* * * * *", Holidays: []string{"26-12-25"}},
		{Kind: "WEEKLY", Zone: "UTC"},
	}
	for i, s := range bad {
		if _, err := New(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d (%+v): err = %v, want ErrInvalid", i, s, err)
		}
	}
}

func TestRangeIsBounded(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "UTC", Cron: "0 0 * * *"})
	if _, err := tr.Cycles(at("2026-01-01T00:00:00Z"), at("2028-01-01T00:00:00Z")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if cs, err := tr.Cycles(at("2026-01-02T00:00:00Z"), at("2026-01-01T00:00:00Z")); err != nil || cs != nil {
		t.Fatalf("empty range = %v, %v", cs, err)
	}
}

func TestCycleKeysAreStable(t *testing.T) {
	tr := must(t, Spec{Kind: KindCron, Zone: "Europe/Berlin", Cron: "0 6 * * *"})
	a, _ := tr.Cycles(at("2026-05-01T00:00:00Z"), at("2026-05-03T00:00:00Z"))
	b, _ := tr.Cycles(at("2026-04-30T00:00:00Z"), at("2026-05-04T00:00:00Z"))
	if len(a) != 2 || a[0].Key != "t2026-05-01T04:00:00Z" {
		t.Fatalf("keys = %+v", a)
	}
	if a[0].Key != b[1].Key || a[1].Key != b[2].Key {
		t.Fatalf("key moved with the query window: %+v vs %+v", a, b)
	}
}
