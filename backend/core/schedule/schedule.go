// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package schedule decides WHEN a campaign fires. It is the trigger half of the scheduler:
// pure functions over a trigger definition and an instant, with no clock, database or
// service, so every replica agrees and a test can hold time still.
//
// # Trigger kinds
//
//   - CRON: a five-field expression read in the trigger's time zone.
//   - CALENDAR: an explicit list of local dates, each fired at each of a list of local times.
//   - SOLAR: sunrise or sunset plus an offset, computed PER DEVICE from that device's own
//     installed location (core/solar). The same trigger fires at a different instant for
//     every fixture, which is the whole point of it.
//
// Any trigger may carry a list of HOLIDAYS: local dates in the trigger's zone on which it
// does not fire. A holiday suppresses; it never moves a fire to another day.
//
// # Zones and daylight saving
//
// A CRON or CALENDAR time is wall-clock time in the trigger's IANA zone, and the zone is
// required: an empty or "Local" zone is refused, because a schedule that follows the host
// would move when the pod does. The two ambiguous days a year follow core/maintwindow's
// rules, which are the platform's single statement of them: a time inside a spring-forward
// gap fires at the instant the clock jumps, and a time that occurs twice fires at its FIRST
// occurrence only. A SOLAR event involves no zone at all (the sun does not know it); the zone
// there only names which civil calendar the day and the holidays belong to.
package schedule

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/maintwindow"
	"github.com/devicechain-io/dc-microservice/solar"
)

// Kind is a trigger's type.
type Kind string

const (
	KindCron     Kind = "CRON"
	KindCalendar Kind = "CALENDAR"
	KindSolar    Kind = "SOLAR"
)

// SolarEvent names which solar event a SOLAR trigger is anchored to.
type SolarEvent string

const (
	Sunrise SolarEvent = "SUNRISE"
	Sunset  SolarEvent = "SUNSET"
)

// Bounds. They keep a stored trigger small and a planning range finite.
const (
	MaxCalendarDates = 400
	MaxCalendarTimes = 24
	MaxHolidays      = 400
	MaxOffsetMinutes = 12 * 60
	// MaxRange is the longest span Cycles will enumerate in one call.
	MaxRange = 400 * 24 * time.Hour
)

// Spec is the stored form of a trigger.
type Spec struct {
	Kind Kind   `json:"kind"`
	Zone string `json:"zone"`

	// Cron is the five-field expression of a CRON trigger.
	Cron string `json:"cron,omitempty"`

	// Dates and Times are a CALENDAR trigger's local dates (YYYY-MM-DD) and local times (HH:MM).
	Dates []string `json:"dates,omitempty"`
	Times []string `json:"times,omitempty"`

	// Event and OffsetMinutes define a SOLAR trigger. A negative offset is before the event.
	Event         SolarEvent `json:"event,omitempty"`
	OffsetMinutes int        `json:"offsetMinutes,omitempty"`

	// Holidays are local dates (YYYY-MM-DD, in Zone) on which the trigger does not fire.
	Holidays []string `json:"holidays,omitempty"`
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid schedule")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Trigger is a validated Spec.
type Trigger struct {
	spec     Spec
	zone     *time.Location
	cron     *cronExpr
	dates    []civil
	times    [][2]int
	holidays map[civil]struct{}
}

// civil is a calendar date with no zone.
type civil struct {
	Y int
	M time.Month
	D int
}

func (c civil) String() string { return fmt.Sprintf("%04d-%02d-%02d", c.Y, int(c.M), c.D) }

func (c civil) addDays(n int) civil {
	t := time.Date(c.Y, c.M, c.D+n, 12, 0, 0, 0, time.UTC)
	return civil{t.Year(), t.Month(), t.Day()}
}

func parseCivil(s string) (civil, error) {
	if len(s) != 10 {
		return civil{}, fmt.Errorf("date %q is not YYYY-MM-DD", s)
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return civil{}, fmt.Errorf("date %q is not a real YYYY-MM-DD date", s)
	}
	return civil{t.Year(), t.Month(), t.Day()}, nil
}

func parseClock(s string) (int, int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("time %q is not a 24-hour HH:MM time", s)
	}
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("time %q is not a 24-hour HH:MM time", s)
	}
	return t.Hour(), t.Minute(), nil
}

// New validates a Spec.
func New(spec Spec) (*Trigger, error) {
	zone, err := maintwindow.LoadZone(spec.Zone)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	t := &Trigger{spec: spec, zone: zone, holidays: map[civil]struct{}{}}
	switch spec.Kind {
	case KindCron:
		if len(spec.Dates)+len(spec.Times) > 0 || spec.Event != "" || spec.OffsetMinutes != 0 {
			return nil, invalidf("a CRON trigger carries only a cron expression")
		}
		if t.cron, err = parseCron(spec.Cron); err != nil {
			return nil, err
		}
	case KindCalendar:
		if spec.Cron != "" || spec.Event != "" || spec.OffsetMinutes != 0 {
			return nil, invalidf("a CALENDAR trigger carries only dates and times")
		}
		if len(spec.Dates) == 0 || len(spec.Dates) > MaxCalendarDates {
			return nil, invalidf("a CALENDAR trigger needs 1..%d dates", MaxCalendarDates)
		}
		if len(spec.Times) == 0 || len(spec.Times) > MaxCalendarTimes {
			return nil, invalidf("a CALENDAR trigger needs 1..%d times", MaxCalendarTimes)
		}
		for _, d := range spec.Dates {
			c, err := parseCivil(d)
			if err != nil {
				return nil, invalidf("%v", err)
			}
			t.dates = append(t.dates, c)
		}
		for _, s := range spec.Times {
			h, m, err := parseClock(s)
			if err != nil {
				return nil, invalidf("%v", err)
			}
			t.times = append(t.times, [2]int{h, m})
		}
	case KindSolar:
		if spec.Cron != "" || len(spec.Dates)+len(spec.Times) > 0 {
			return nil, invalidf("a SOLAR trigger carries only an event and an offset")
		}
		if spec.Event != Sunrise && spec.Event != Sunset {
			return nil, invalidf("solar event %q is not SUNRISE or SUNSET", spec.Event)
		}
		if spec.OffsetMinutes < -MaxOffsetMinutes || spec.OffsetMinutes > MaxOffsetMinutes {
			return nil, invalidf("offset %d minutes is outside +/-%d", spec.OffsetMinutes, MaxOffsetMinutes)
		}
	default:
		return nil, invalidf("trigger kind %q is not CRON, CALENDAR or SOLAR", spec.Kind)
	}
	if len(spec.Holidays) > MaxHolidays {
		return nil, invalidf("%d holidays is more than the %d allowed", len(spec.Holidays), MaxHolidays)
	}
	for _, h := range spec.Holidays {
		c, err := parseCivil(h)
		if err != nil {
			return nil, invalidf("holiday: %v", err)
		}
		t.holidays[c] = struct{}{}
	}
	return t, nil
}

// Spec returns the definition the trigger was built from.
func (t *Trigger) Spec() Spec { return t.spec }

// Zone is the trigger's time zone.
func (t *Trigger) Zone() *time.Location { return t.zone }

// PerDevice reports whether the fire instant depends on the device (a SOLAR trigger).
func (t *Trigger) PerDevice() bool { return t.spec.Kind == KindSolar }

// Cycle is one firing of a trigger.
type Cycle struct {
	// Key identifies the cycle for good: the same trigger always yields the same key for the
	// same cycle, which is what lets a second replica planning it find it already planned.
	Key string
	// At is the fire instant of a CRON or CALENDAR cycle; zero for a SOLAR cycle, whose
	// instant is per device.
	At time.Time
	// Day is the civil date (in the trigger's zone) a SOLAR cycle belongs to.
	Day string
}

// IsHoliday reports whether the civil date is one of the trigger's holidays.
func (t *Trigger) isHoliday(c civil) bool {
	_, ok := t.holidays[c]
	return ok
}

// Cycles returns the cycles whose fire time (CRON, CALENDAR) or civil day (SOLAR) lies in
// [from, to), earliest first, holidays removed. For a SOLAR trigger a civil day is included
// when its local midnight falls in [from, to) - the planner plans a day once, ahead of it.
func (t *Trigger) Cycles(from, to time.Time) ([]Cycle, error) {
	if !to.After(from) {
		return nil, nil
	}
	if to.Sub(from) > MaxRange {
		return nil, invalidf("range %s is longer than the %s one call may enumerate", to.Sub(from), MaxRange)
	}
	first := localDay(from.In(t.zone)).addDays(-1)
	last := localDay(to.In(t.zone)).addDays(1)

	var out []Cycle
	for d := first; !last.before(d); d = d.addDays(1) {
		if t.isHoliday(d) {
			continue
		}
		switch t.spec.Kind {
		case KindSolar:
			mid := maintwindow.ResolveLocal(t.zone, d.Y, d.M, d.D, 0, 0)
			if !mid.Before(from) && mid.Before(to) {
				out = append(out, Cycle{Key: "d" + d.String(), Day: d.String()})
			}
		case KindCalendar:
			if !t.hasDate(d) {
				continue
			}
			for _, hm := range t.times {
				out = appendIn(out, maintwindow.ResolveLocal(t.zone, d.Y, d.M, d.D, hm[0], hm[1]), from, to)
			}
		case KindCron:
			if !t.cron.dayMatches(int(d.M), d.D, int(weekday(d))) {
				continue
			}
			for h := 0; h < 24; h++ {
				if t.cron.hour&(1<<uint(h)) == 0 {
					continue
				}
				for m := 0; m < 60; m++ {
					if t.cron.minute&(1<<uint(m)) == 0 {
						continue
					}
					out = appendIn(out, maintwindow.ResolveLocal(t.zone, d.Y, d.M, d.D, h, m), from, to)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Key < out[j].Key
		}
		return out[i].At.Before(out[j].At)
	})
	// Two wall times can resolve to one instant (both inside a spring-forward gap); that is
	// one firing.
	dedup := out[:0]
	for i, c := range out {
		if i > 0 && c.Key == out[i-1].Key {
			continue
		}
		dedup = append(dedup, c)
	}
	return dedup, nil
}

func appendIn(out []Cycle, at, from, to time.Time) []Cycle {
	if at.Before(from) || !at.Before(to) {
		return out
	}
	return append(out, Cycle{Key: "t" + at.UTC().Format(time.RFC3339), At: at.UTC()})
}

func (c civil) before(o civil) bool {
	if c.Y != o.Y {
		return c.Y < o.Y
	}
	if c.M != o.M {
		return c.M < o.M
	}
	return c.D < o.D
}

func localDay(t time.Time) civil { return civil{t.Year(), t.Month(), t.Day()} }

func weekday(c civil) time.Weekday {
	return time.Date(c.Y, c.M, c.D, 12, 0, 0, 0, time.UTC).Weekday()
}

func (t *Trigger) hasDate(c civil) bool {
	for _, d := range t.dates {
		if d == c {
			return true
		}
	}
	return false
}

// Fire is the instant a SOLAR cycle fires at one place, or the reason it does not.
type Fire struct {
	At time.Time
	// Skipped is empty when At is valid; otherwise it says why this place has no event today
	// (POLAR_DAY, POLAR_NIGHT) and At is zero.
	Skipped string
}

// SolarFire computes a SOLAR cycle's instant for a place at latitude lat and longitude lon.
func (t *Trigger) SolarFire(c Cycle, lat, lon float64) (Fire, error) {
	if t.spec.Kind != KindSolar {
		return Fire{}, invalidf("%s trigger has no per-place fire time", t.spec.Kind)
	}
	d, err := parseCivil(strings.TrimSpace(c.Day))
	if err != nil {
		return Fire{}, invalidf("%v", err)
	}
	ev, err := solar.Compute(d.Y, d.M, d.D, lat, lon)
	if err != nil {
		return Fire{}, err
	}
	if ev.Status != solar.Normal {
		return Fire{Skipped: ev.Status.String()}, nil
	}
	at := ev.Set
	if t.spec.Event == Sunrise {
		at = ev.Rise
	}
	return Fire{At: at.Add(time.Duration(t.spec.OffsetMinutes) * time.Minute)}, nil
}
