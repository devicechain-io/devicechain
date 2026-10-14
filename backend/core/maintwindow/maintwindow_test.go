// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package maintwindow

import (
	"errors"
	"testing"
	"time"
)

func mustSchedule(t *testing.T, tz string, slots ...Slot) *Schedule {
	t.Helper()
	s, err := New(tz, slots)
	if err != nil {
		t.Fatalf("New(%q): %v", tz, err)
	}
	return s
}

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// Sunday 02:00-06:00 Europe/Berlin: the plant's window from the design.
var sundayNight = Slot{Day: "SUNDAY", Start: "02:00", DurationMinutes: 240}

func TestClosedBeforeWindowOpensAtNextStart(t *testing.T) {
	s := mustSchedule(t, "Europe/Berlin", sundayNight)
	// Wednesday 2026-01-14 12:00 UTC; the next Sunday is the 18th, 02:00 CET = 01:00 UTC.
	at := utc(2026, 1, 14, 12, 0)
	if s.OpenAt(at) {
		t.Fatal("open on a Wednesday")
	}
	if got, want := s.DispatchAfter(at), utc(2026, 1, 18, 1, 0); !got.Equal(want) {
		t.Fatalf("DispatchAfter = %v, want %v", got, want)
	}
}

func TestInsideWindowDispatchesNow(t *testing.T) {
	s := mustSchedule(t, "Europe/Berlin", sundayNight)
	at := utc(2026, 1, 18, 3, 30) // 04:30 CET, inside 02:00-06:00
	if !s.OpenAt(at) {
		t.Fatal("closed inside the window")
	}
	if got := s.DispatchAfter(at); !got.Equal(at) {
		t.Fatalf("DispatchAfter = %v, want now (%v)", got, at)
	}
}

func TestWindowEndIsExclusive(t *testing.T) {
	s := mustSchedule(t, "Europe/Berlin", sundayNight)
	closeAt := utc(2026, 1, 18, 5, 0) // 06:00 CET
	if s.OpenAt(closeAt) {
		t.Fatal("still open at the exact close instant")
	}
	if !s.OpenAt(closeAt.Add(-time.Second)) {
		t.Fatal("closed one second before the close")
	}
	if got, want := s.DispatchAfter(closeAt), utc(2026, 1, 25, 1, 0); !got.Equal(want) {
		t.Fatalf("after the close, next opening = %v, want %v", got, want)
	}
}

func TestWindowSpanningMidnight(t *testing.T) {
	// Saturday 22:00 for 6 hours runs to Sunday 04:00 local.
	s := mustSchedule(t, "America/New_York", Slot{Day: "SATURDAY", Start: "22:00", DurationMinutes: 360})
	// Sunday 2026-01-18 02:00 EST = 07:00 UTC: inside, though the slot began on Saturday.
	if !s.OpenAt(utc(2026, 1, 18, 7, 0)) {
		t.Fatal("closed after midnight inside a slot that began the previous day")
	}
	// Sunday 04:00 EST = 09:00 UTC: closed again.
	if s.OpenAt(utc(2026, 1, 18, 9, 0)) {
		t.Fatal("open past the end of a midnight-spanning slot")
	}
}

func TestWindowWrappingTheWeek(t *testing.T) {
	// Sunday 23:00 for 4 hours runs into Monday 03:00: the slot wraps the week boundary.
	s := mustSchedule(t, "UTC", Slot{Day: "SUNDAY", Start: "23:00", DurationMinutes: 240})
	if !s.OpenAt(utc(2026, 1, 19, 1, 0)) { // Monday 01:00
		t.Fatal("closed on Monday inside a slot that began Sunday night")
	}
	if s.OpenAt(utc(2026, 1, 19, 3, 0)) {
		t.Fatal("open at the wrapped slot's close")
	}
	if got, want := s.DispatchAfter(utc(2026, 1, 19, 3, 0)), utc(2026, 1, 25, 23, 0); !got.Equal(want) {
		t.Fatalf("next opening = %v, want %v", got, want)
	}
}

func TestMultipleSlotsPickTheEarliest(t *testing.T) {
	s := mustSchedule(t, "UTC",
		Slot{Day: "FRIDAY", Start: "20:00", DurationMinutes: 60},
		Slot{Day: "TUESDAY", Start: "03:00", DurationMinutes: 60})
	at := utc(2026, 1, 14, 12, 0) // Wednesday
	if got, want := s.DispatchAfter(at), utc(2026, 1, 16, 20, 0); !got.Equal(want) {
		t.Fatalf("DispatchAfter = %v, want the Friday slot %v", got, want)
	}
}

// Slots that touch or overlap are one opening, so its close is the close of the last one.
// Reporting the first slot's close would make a caller that refuses work at End see the
// window shut and reopen at the seam.
func TestTouchingAndOverlappingSlotsAreOneOpening(t *testing.T) {
	s := mustSchedule(t, "UTC",
		Slot{Day: "SATURDAY", Start: "22:00", DurationMinutes: 120},
		Slot{Day: "SUNDAY", Start: "00:00", DurationMinutes: 120},
		Slot{Day: "SUNDAY", Start: "01:00", DurationMinutes: 120}) // overlaps the second
	for _, at := range []time.Time{
		utc(2026, 1, 17, 12, 0), // Saturday noon: the next opening
		utc(2026, 1, 17, 23, 0), // inside the first slot
		utc(2026, 1, 18, 0, 30), // inside the second
		utc(2026, 1, 18, 2, 30), // inside only the third
	} {
		iv := s.Next(at)
		if want := utc(2026, 1, 18, 3, 0); !iv.End.Equal(want) {
			t.Fatalf("at %v: End = %v, want the merged close %v", at, iv.End, want)
		}
	}
	if iv := s.Next(utc(2026, 1, 17, 23, 0)); !iv.Start.Equal(utc(2026, 1, 17, 22, 0)) {
		t.Fatalf("Start = %v, want the first slot's start", iv.Start)
	}
	// A gap of a minute is a gap.
	g := mustSchedule(t, "UTC",
		Slot{Day: "SATURDAY", Start: "22:00", DurationMinutes: 119},
		Slot{Day: "SUNDAY", Start: "00:00", DurationMinutes: 120})
	if iv := g.Next(utc(2026, 1, 17, 23, 0)); !iv.End.Equal(utc(2026, 1, 17, 23, 59)) {
		t.Fatalf("End = %v; slots a minute apart were merged", iv.End)
	}
}

// Spring forward in Berlin: on 2026-03-29 the clock jumps 02:00 -> 03:00 (01:00 UTC), so
// 02:30 that Sunday does not exist.
func TestSpringForwardGapStartOpensAtTheJump(t *testing.T) {
	s := mustSchedule(t, "Europe/Berlin", Slot{Day: "SUNDAY", Start: "02:30", DurationMinutes: 120})
	before := utc(2026, 3, 28, 12, 0)
	got := s.DispatchAfter(before)
	if want := utc(2026, 3, 29, 1, 0); !got.Equal(want) {
		t.Fatalf("gap start opened at %v, want the jump %v (03:00 CEST)", got, want)
	}
	// The duration is elapsed: two real hours from the jump.
	if iv := s.Next(before); !iv.End.Equal(utc(2026, 3, 29, 3, 0)) {
		t.Fatalf("gap-start interval ends %v, want %v", iv.End, utc(2026, 3, 29, 3, 0))
	}
}

func TestSpringForwardDoesNotShiftAnOrdinaryStart(t *testing.T) {
	// 03:30 exists (as CEST) on the day of the jump: it is 01:30 UTC.
	s := mustSchedule(t, "Europe/Berlin", Slot{Day: "SUNDAY", Start: "03:30", DurationMinutes: 60})
	if got, want := s.DispatchAfter(utc(2026, 3, 28, 12, 0)), utc(2026, 3, 29, 1, 30); !got.Equal(want) {
		t.Fatalf("DispatchAfter = %v, want %v", got, want)
	}
}

// Fall back in Berlin: on 2026-10-25 the clock goes 03:00 CEST -> 02:00 CET, so 02:30 occurs
// twice (00:30 UTC and 01:30 UTC).
func TestFallBackOverlapStartUsesTheFirstOccurrence(t *testing.T) {
	s := mustSchedule(t, "Europe/Berlin", Slot{Day: "SUNDAY", Start: "02:30", DurationMinutes: 60})
	if got, want := s.DispatchAfter(utc(2026, 10, 24, 12, 0)), utc(2026, 10, 25, 0, 30); !got.Equal(want) {
		t.Fatalf("ambiguous start opened at %v, want the first occurrence %v", got, want)
	}
}

func TestFallBackDurationIsElapsedTime(t *testing.T) {
	// 01:00 for 4 hours on the fall-back day is 4 REAL hours (23:00 UTC .. 03:00 UTC), which
	// is 05:00 on the wall clock afterwards, not 05:00 - 01:00 = 4 wall hours.
	s := mustSchedule(t, "Europe/Berlin", Slot{Day: "SUNDAY", Start: "01:00", DurationMinutes: 240})
	iv := s.Next(utc(2026, 10, 24, 12, 0))
	if !iv.Start.Equal(utc(2026, 10, 24, 23, 0)) || !iv.End.Equal(utc(2026, 10, 25, 3, 0)) {
		t.Fatalf("interval = %v..%v, want 23:00..03:00 UTC", iv.Start, iv.End)
	}
}

func TestStartOnDstDayAfterTheChangeIsUnaffected(t *testing.T) {
	// The Sunday after the jump uses the new offset: 02:00 CEST = 00:00 UTC.
	s := mustSchedule(t, "Europe/Berlin", sundayNight)
	if got, want := s.DispatchAfter(utc(2026, 3, 29, 12, 0)), utc(2026, 4, 5, 0, 0); !got.Equal(want) {
		t.Fatalf("DispatchAfter = %v, want %v", got, want)
	}
}

func TestSouthernHemisphereAndHalfHourZones(t *testing.T) {
	// Adelaide is UTC+10:30 in summer and +9:30 in winter.
	s := mustSchedule(t, "Australia/Adelaide", Slot{Day: "SATURDAY", Start: "01:00", DurationMinutes: 60})
	// 2026-01-10 is a Saturday in southern summer: 01:00 ACDT = 14:30 UTC the day before.
	if got, want := s.DispatchAfter(utc(2026, 1, 8, 0, 0)), utc(2026, 1, 9, 14, 30); !got.Equal(want) {
		t.Fatalf("DispatchAfter = %v, want %v", got, want)
	}
}

func TestIntervalsListsUpcomingOpenings(t *testing.T) {
	s := mustSchedule(t, "UTC", Slot{Day: "MONDAY", Start: "01:00", DurationMinutes: 60})
	ivs := s.Intervals(utc(2026, 1, 14, 0, 0), 3)
	if len(ivs) != 3 {
		t.Fatalf("got %d intervals, want 3", len(ivs))
	}
	if !ivs[0].Start.Equal(utc(2026, 1, 19, 1, 0)) || !ivs[2].Start.Equal(utc(2026, 2, 2, 1, 0)) {
		t.Fatalf("intervals = %v", ivs)
	}
}

func TestValidation(t *testing.T) {
	good := []Slot{sundayNight}
	cases := map[string]struct {
		tz    string
		slots []Slot
	}{
		"no zone":       {"", good},
		"Local":         {"Local", good},
		"unknown zone":  {"Mars/Olympus", good},
		"no slots":      {"UTC", nil},
		"bad day":       {"UTC", []Slot{{Day: "Sunday", Start: "02:00", DurationMinutes: 60}}},
		"bad start":     {"UTC", []Slot{{Day: "SUNDAY", Start: "2:00", DurationMinutes: 60}}},
		"hour 24":       {"UTC", []Slot{{Day: "SUNDAY", Start: "24:00", DurationMinutes: 60}}},
		"zero duration": {"UTC", []Slot{{Day: "SUNDAY", Start: "02:00", DurationMinutes: 0}}},
		"over a week":   {"UTC", []Slot{{Day: "SUNDAY", Start: "02:00", DurationMinutes: MaxDurationMinutes + 1}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(c.tz, c.slots)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	tooMany := make([]Slot, MaxSlots+1)
	for i := range tooMany {
		tooMany[i] = sundayNight
	}
	if _, err := New("UTC", tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many slots: err = %v", err)
	}
}
