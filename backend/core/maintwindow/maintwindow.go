// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package maintwindow answers one question for a weekly maintenance schedule: given an
// instant, when may a disruptive operation START?
//
// It is a pure function over a schedule value (weekly slots plus an IANA time zone) and an
// instant. It reads no clock, no database and no service, so every consumer that has to
// agree on the answer — the command dispatch gate today, an over-the-air update gate
// later — calls this and cannot drift from the others.
//
// # What a window means
//
// A window gates the START of an operation. An operation claimed inside a window finishes
// whenever it finishes; nothing here ends it. The close of an interval is reported only so
// a caller can say "this window closes at …", and so that an instant just after the close
// is correctly "closed".
//
// # Time zones and daylight saving
//
// A slot is wall-clock time in the schedule's zone ("Sunday 02:00 for 240 minutes"). Two
// days a year that wall-clock time does not name exactly one instant, and the rule is
// stated rather than left to the standard library, which documents its choice as
// unspecified:
//
//   - A start that falls in a spring-forward GAP (the local time does not exist) opens at
//     the first instant after the gap, which is when that day's local clock jumps over it.
//   - A start that falls in a fall-back OVERLAP (the local time occurs twice) opens at the
//     FIRST occurrence.
//   - The duration is ELAPSED time, not wall-clock time, so a four-hour window is four real
//     hours long on a day the clocks change.
//
// The zone database is embedded (time/tzdata) so a minimal or distroless image that ships
// no zoneinfo still resolves every zone name instead of failing the first lookup.
package maintwindow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // embed the zone database: a distroless image has none on disk
)

// Bounds on a schedule. They keep the calculator's lookup window finite and a stored
// document small; none is a limit anyone running a plant will meet.
const (
	// MaxSlots is the most weekly slots one schedule may carry.
	MaxSlots = 56
	// MinDurationMinutes / MaxDurationMinutes bound one slot's length. The maximum is one
	// week, which is also what bounds how far back the calculator must look for a slot that
	// began before the instant asked about and is still open.
	MinDurationMinutes = 1
	MaxDurationMinutes = 7 * 24 * 60
)

// Slot is one recurring weekly opening: on Day, from Start (local wall-clock "HH:MM",
// 24-hour) for DurationMinutes. A slot may run past midnight, and past the end of the week
// into the next one.
type Slot struct {
	// Day is the weekday the slot STARTS on, upper-case: MONDAY .. SUNDAY.
	Day string `json:"day"`
	// Start is the local start time, "HH:MM" on a 24-hour clock.
	Start string `json:"start"`
	// DurationMinutes is how long the slot stays open, in elapsed minutes.
	DurationMinutes int `json:"durationMinutes"`
}

// Days is the accepted spelling of a slot's Day, in week order starting at Monday.
var Days = []string{"MONDAY", "TUESDAY", "WEDNESDAY", "THURSDAY", "FRIDAY", "SATURDAY", "SUNDAY"}

var weekdayOf = map[string]time.Weekday{
	"MONDAY": time.Monday, "TUESDAY": time.Tuesday, "WEDNESDAY": time.Wednesday,
	"THURSDAY": time.Thursday, "FRIDAY": time.Friday, "SATURDAY": time.Saturday,
	"SUNDAY": time.Sunday,
}

// Schedule is a validated weekly schedule. Build it with New; the zero value is not usable.
type Schedule struct {
	zone  *time.Location
	slots []parsedSlot
}

type parsedSlot struct {
	day      time.Weekday
	hour     int
	minute   int
	duration time.Duration
}

// ErrInvalid wraps every validation failure, so a caller can tell "this schedule is not
// acceptable" from an infrastructure failure with errors.Is.
var ErrInvalid = errors.New("invalid maintenance window")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// LoadZone resolves an IANA zone name. The empty string and "Local" are refused: the first
// would silently mean UTC and the second would mean whatever zone the server happens to run
// in, and a maintenance window that moves with the host is worse than none.
func LoadZone(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, invalidf("timezone is required (an IANA name such as Europe/Berlin)")
	}
	if strings.EqualFold(name, "Local") {
		return nil, invalidf("timezone %q is not an IANA zone name", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalidf("timezone %q is not a known IANA zone", name)
	}
	return loc, nil
}

// New validates a zone name and its slots and returns the schedule.
func New(timezone string, slots []Slot) (*Schedule, error) {
	zone, err := LoadZone(timezone)
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 {
		return nil, invalidf("at least one weekly slot is required")
	}
	if len(slots) > MaxSlots {
		return nil, invalidf("%d slots is more than the %d allowed", len(slots), MaxSlots)
	}
	parsed := make([]parsedSlot, 0, len(slots))
	for i, s := range slots {
		day, ok := weekdayOf[s.Day]
		if !ok {
			return nil, invalidf("slot %d: day %q is not one of MONDAY..SUNDAY", i+1, s.Day)
		}
		hour, minute, err := parseClock(s.Start)
		if err != nil {
			return nil, invalidf("slot %d: %v", i+1, err)
		}
		if s.DurationMinutes < MinDurationMinutes || s.DurationMinutes > MaxDurationMinutes {
			return nil, invalidf("slot %d: duration %d minutes is outside %d..%d", i+1,
				s.DurationMinutes, MinDurationMinutes, MaxDurationMinutes)
		}
		parsed = append(parsed, parsedSlot{day: day, hour: hour, minute: minute,
			duration: time.Duration(s.DurationMinutes) * time.Minute})
	}
	return &Schedule{zone: zone, slots: parsed}, nil
}

// parseClock reads a strict 24-hour "HH:MM". Two digits each, so "2:00" and "24:00" are
// refused rather than guessed at.
func parseClock(s string) (int, int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("start %q is not a 24-hour HH:MM time", s)
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("start %q is not a 24-hour HH:MM time", s)
	}
	return t.Hour(), t.Minute(), nil
}

// Zone is the schedule's time zone.
func (s *Schedule) Zone() *time.Location { return s.zone }

// Interval is one open stretch: [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// Next returns the earliest opening that has not yet closed at `at`: the one open now
// (Start <= at < End) if there is one, otherwise the next to open (Start > at).
//
// Slots that overlap or touch are ONE opening: Saturday 22:00 for two hours followed by
// Sunday 00:00 for two hours is open from 22:00 to 02:00, and End reports 02:00, not
// midnight. A caller that bounds an operation by End (the command dispatch gate refuses a
// claim at or after it) would otherwise see the window close and reopen at the seam.
//
// The interval is never empty: a schedule always has a slot, and slots recur weekly.
func (s *Schedule) Next(at time.Time) Interval {
	var best Interval
	found := false
	var all []Interval
	consider := func(iv Interval) {
		all = append(all, iv)
		if !iv.End.After(at) {
			return // already closed
		}
		if !found || iv.Start.Before(best.Start) ||
			(iv.Start.Equal(best.Start) && iv.End.After(best.End)) {
			best, found = iv, true
		}
	}
	// A slot can start up to MaxDuration before `at` and still be open, and the soonest
	// future occurrence of any slot is within a week; one extra day each side absorbs the
	// zone's offset against the date arithmetic below.
	local := at.In(s.zone)
	first := time.Date(local.Year(), local.Month(), local.Day()-7-1, 0, 0, 0, 0, s.zone)
	for d := 0; d <= 7+1+7+1; d++ {
		day := first.AddDate(0, 0, d)
		for _, sl := range s.slots {
			if day.Weekday() != sl.day {
				continue
			}
			start := resolveLocal(s.zone, day.Year(), day.Month(), day.Day(), sl.hour, sl.minute)
			consider(Interval{Start: start, End: start.Add(sl.duration)})
		}
	}
	// Extend the opening through every interval that overlaps or touches it. Bounded by the
	// scan: a schedule open all week long ends at the scan's horizon, at least a week out.
	for extended := true; extended; {
		extended = false
		for _, iv := range all {
			if !iv.Start.After(best.End) && iv.End.After(best.End) {
				best.End, extended = iv.End, true
			}
		}
	}
	return best
}

// OpenAt reports whether `at` falls inside any interval.
func (s *Schedule) OpenAt(at time.Time) bool {
	iv := s.Next(at)
	return !iv.Start.After(at)
}

// DispatchAfter is the earliest instant an operation asked for at `at` may START: `at`
// itself when a window is open, otherwise the next opening.
func (s *Schedule) DispatchAfter(at time.Time) time.Time {
	iv := s.Next(at)
	if iv.Start.After(at) {
		return iv.Start
	}
	return at
}

// Intervals returns the first n intervals that have not closed at `at`, earliest first.
// It exists for display (a console preview of the coming openings); the gate itself only
// needs Next.
func (s *Schedule) Intervals(at time.Time, n int) []Interval {
	var all []Interval
	local := at.In(s.zone)
	first := time.Date(local.Year(), local.Month(), local.Day()-7-1, 0, 0, 0, 0, s.zone)
	horizon := 7 + 1 + 7*(n/len(s.slots)+2)
	for d := 0; d <= horizon; d++ {
		day := first.AddDate(0, 0, d)
		for _, sl := range s.slots {
			if day.Weekday() != sl.day {
				continue
			}
			start := resolveLocal(s.zone, day.Year(), day.Month(), day.Day(), sl.hour, sl.minute)
			iv := Interval{Start: start, End: start.Add(sl.duration)}
			if iv.End.After(at) {
				all = append(all, iv)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// resolveLocal turns a wall-clock time in zone into an instant, applying the package's DST
// rules (see the package comment) instead of the standard library's unspecified choice.
func resolveLocal(zone *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
	// The wall time read as if it were UTC, then shifted by the zone's offset on either
	// side of it. A transition is never closer than a day to a neighbour in any real zone,
	// so the offsets one day either side are the "before" and "after" offsets.
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	_, offBefore := wall.Add(-24 * time.Hour).In(zone).Zone()
	_, offAfter := wall.Add(24 * time.Hour).In(zone).Zone()
	// The offsets are those at the shifted INSTANTS, which approximate the zone's offsets
	// around the wall time well enough to build the two candidates; each is then verified.
	candidates := []time.Time{
		wall.Add(-time.Duration(offBefore) * time.Second),
		wall.Add(-time.Duration(offAfter) * time.Second),
	}
	matches := make([]time.Time, 0, 2)
	for _, c := range candidates {
		l := c.In(zone)
		if l.Year() == year && l.Month() == month && l.Day() == day && l.Hour() == hour && l.Minute() == minute {
			if len(matches) == 0 || !matches[0].Equal(c) {
				matches = append(matches, c)
			}
		}
	}
	switch len(matches) {
	case 0:
		// A gap. The candidate built from the AFTER offset lies before the jump, so the
		// end of its zone period is the instant the clock jumps.
		_, end := candidates[1].In(zone).ZoneBounds()
		if end.IsZero() {
			return candidates[0]
		}
		return end
	case 1:
		return matches[0]
	default:
		if matches[0].Before(matches[1]) {
			return matches[0]
		}
		return matches[1]
	}
}
