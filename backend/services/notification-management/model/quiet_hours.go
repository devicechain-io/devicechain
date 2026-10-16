// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/maintwindow"
	"gorm.io/datatypes"
)

// Quiet hours on a notification policy.
//
// A policy's quiet hours are recurring weekly stretches, in an IANA time zone, during which
// the policy does not page anyone about an alarm below its bypass severity. A notification
// that falls inside quiet hours is not dropped: it is DEFERRED (see NotificationDeferral)
// and delivered when the quiet stretch ends, unless the alarm was acknowledged or cleared
// in the meantime.
//
// # Not a maintenance window, on purpose
//
// device-management has maintenance windows: weekly stretches during which a DISRUPTIVE
// command may START. Quiet hours read the opposite way — the stretch is when something is
// HELD — and they belong to a different noun (a routing policy, not a device type). They
// share the calculator (core/maintwindow) and the slot document shape, so an operator
// writes `{day, start, durationMinutes}` the same way in both places and the two agree on
// time zones and daylight saving, but they are separate fields with separate names. A
// maintenance window is never consulted here and a quiet-hours slot never gates a command.
//
// # The rules, all enforced at the write (INVALID_VALUE)
//
//   - quietHoursTimezone and quietHoursSlots are set together or not at all. A zone with no
//     slots, or slots with no zone, is a half-configured policy that means nothing.
//   - The zone is an IANA name. The empty string and "Local" are refused: quiet hours that
//     move with whatever zone the server runs in are worse than none.
//   - Slots are 1..56 weekly entries of 1..10080 minutes each, exactly as a maintenance
//     window's are.
//   - No two slots OVERLAP in the week as written (touching is fine). The calculator would
//     merge them, so an overlap is not unsafe — but it is almost always a typo, and a typo
//     in quiet hours is a night nobody is paged.
//   - The slots may not cover the whole week. A policy that is always quiet pages nothing
//     below its bypass severity, ever; that is a disabled policy wearing a schedule, and
//     the honest way to say it is enabled=false.
//   - quietHoursBypassSeverity is an alarm tier (CRITICAL..INDETERMINATE; not "*"). It
//     may be stored while the policy has no quiet hours, where it has nothing to bypass.

// ErrInvalidQuietHours wraps every quiet-hours validation failure. It is an INVALID_VALUE
// refusal on the wire.
var ErrInvalidQuietHours = integrity.NewRefusal(integrity.ClassInvalid, "invalid quiet hours")

// quietHoursError is one quiet-hours refusal: the sentence an operator reads, carrying
// ErrInvalidQuietHours (and so its INVALID_VALUE code) beneath it.
type quietHoursError struct{ msg string }

func (e *quietHoursError) Error() string { return "invalid quiet hours: " + e.msg }
func (e *quietHoursError) Unwrap() error { return ErrInvalidQuietHours }

func quietHoursInvalid(format string, args ...any) error {
	return &quietHoursError{msg: fmt.Sprintf(format, args...)}
}

// minutesPerWeek is the length of the week the slots are laid out on.
const minutesPerWeek = 7 * 24 * 60

// QuietHours is a policy's validated quiet-hours configuration.
type QuietHours struct {
	schedule *maintwindow.Schedule
	// bypass is the alarm tier at or above which quiet hours do not apply; "" means
	// nothing bypasses.
	bypass string
}

// QuietHours returns the policy's quiet hours, or nil when the policy has none.
//
// An error means the stored configuration no longer validates (a hand-edited row, or rules
// tightened since it was written). The caller must not read that as "no quiet hours" or as
// "always quiet" without saying so: see the dispatcher, which pages (the safe direction for
// an alerting service) and logs.
func (p *NotificationPolicy) QuietHours() (*QuietHours, error) {
	if !p.QuietHoursTimezone.Valid && p.QuietHoursSlots == nil {
		return nil, nil
	}
	if !p.QuietHoursTimezone.Valid || p.QuietHoursSlots == nil {
		return nil, quietHoursInvalid("stored timezone and slots are not both set")
	}
	slots, err := decodeQuietSlots(string(*p.QuietHoursSlots))
	if err != nil {
		return nil, err
	}
	schedule, err := quietSchedule(p.QuietHoursTimezone.String, slots)
	if err != nil {
		return nil, err
	}
	return &QuietHours{schedule: schedule, bypass: p.QuietHoursBypassSeverity.String}, nil
}

// Holds reports whether a notification of the given alarm severity is held at `at`, and if
// so when the quiet stretch it falls in ends (the instant it may be delivered).
//
// A severity at or above the bypass is never held. An alarm severity outside the tier
// vocabulary is never held either: it cannot be ranked against the bypass, and the safe
// reading for a service that pages humans is to page.
func (q *QuietHours) Holds(severity string, at time.Time) (bool, time.Time) {
	if q == nil {
		return false, time.Time{}
	}
	if q.bypass != "" && SeverityAtOrAbove(severity, q.bypass) {
		return false, time.Time{}
	}
	if !slices.Contains(ruleSeverities, severity) {
		return false, time.Time{}
	}
	iv := q.schedule.Next(at)
	if iv.Start.After(at) {
		return false, time.Time{}
	}
	return true, iv.End
}

// SeverityAtOrAbove reports whether severity is at least as severe as threshold in the
// alarm tier order (CRITICAL is the most severe). Either value outside the vocabulary
// answers false.
func SeverityAtOrAbove(severity, threshold string) bool {
	s := slices.Index(ruleSeverities, severity)
	t := slices.Index(ruleSeverities, threshold)
	if s < 0 || t < 0 {
		return false
	}
	return s <= t
}

// decodeQuietSlots reads a quiet-hours slot document STRICTLY: an unknown key is refused
// rather than dropped, so a misspelt field cannot save quiet hours that mean something else.
func decodeQuietSlots(raw string) ([]maintwindow.Slot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, quietHoursInvalid("quietHoursSlots is empty")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var slots []maintwindow.Slot
	if err := dec.Decode(&slots); err != nil {
		return nil, quietHoursInvalid("quietHoursSlots is not a JSON array of {day, start, durationMinutes}: %v", err)
	}
	if dec.More() {
		return nil, quietHoursInvalid("quietHoursSlots has trailing content after the array")
	}
	return slots, nil
}

// quietSchedule builds the calculator and applies the quiet-hours rules the maintenance
// window does not have: no overlapping slots, and not the whole week.
func quietSchedule(timezone string, slots []maintwindow.Slot) (*maintwindow.Schedule, error) {
	schedule, err := maintwindow.New(timezone, slots)
	if err != nil {
		// The calculator's sentence names a "maintenance window"; say what it is here.
		msg := err.Error()
		if errors.Is(err, maintwindow.ErrInvalid) {
			msg = strings.TrimPrefix(msg, maintwindow.ErrInvalid.Error()+": ")
		}
		return nil, quietHoursInvalid("%s", msg)
	}
	if err := checkNoOverlap(slots); err != nil {
		return nil, err
	}
	return schedule, nil
}

// checkNoOverlap refuses slots that overlap in the week as written, and slots that cover
// the whole week. Positions are wall-clock minutes from Monday 00:00, wrapping at the end
// of the week, so a Sunday-night slot that runs into Monday is checked against Monday's.
// maintwindow.New has already validated every day, start and duration.
func checkNoOverlap(slots []maintwindow.Slot) error {
	type span struct{ start, end, index int }
	spans := make([]span, 0, len(slots))
	total := 0
	for i, s := range slots {
		day := slices.Index(maintwindow.Days, s.Day)
		t, _ := time.Parse("15:04", s.Start)
		start := day*24*60 + t.Hour()*60 + t.Minute()
		spans = append(spans, span{start: start, end: start + s.DurationMinutes, index: i + 1})
		total += s.DurationMinutes
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := range spans {
		next := spans[(i+1)%len(spans)]
		nextStart := next.start
		if i == len(spans)-1 {
			// The first slot again, a week later: what the last one may not run into.
			nextStart += minutesPerWeek
		}
		if len(spans) > 1 && spans[i].end > nextStart {
			a, b := spans[i].index, next.index
			if a > b {
				a, b = b, a
			}
			return quietHoursInvalid("slots %d and %d overlap", a, b)
		}
	}
	if total >= minutesPerWeek {
		return quietHoursInvalid("the slots cover the whole week, so the policy would never page; " +
			"disable the policy instead")
	}
	return nil
}

// quietHoursColumns is the validated, canonical form of a quiet-hours triple, ready to
// store.
type quietHoursColumns struct {
	timezone *string
	slots    *datatypes.JSON
	bypass   *string
}

// validateQuietHours checks a (timezone, slots, bypass) triple as it will be STORED — the
// create request's values, or an update's values folded over the stored ones — and returns
// the canonical columns. All three nil is "no quiet hours".
func validateQuietHours(timezone, slots, bypass *string) (quietHoursColumns, error) {
	var out quietHoursColumns
	if bypass != nil {
		if *bypass == SeverityAny || !slices.Contains(ruleSeverities, *bypass) {
			return out, quietHoursInvalid("quietHoursBypassSeverity %q is not an alarm tier (known: %s)",
				*bypass, strings.Join(ruleSeverities, ", "))
		}
	}
	if timezone == nil && slots == nil {
		// A bypass with no quiet hours bypasses nothing, and is kept rather than refused:
		// turning quiet hours off and on again is one edit of the pair, not two writes in
		// a particular order, and the stored bypass is what the policy had.
		out.bypass = bypass
		return out, nil
	}
	if timezone == nil || slots == nil {
		return out, quietHoursInvalid("quietHoursTimezone and quietHoursSlots are set together, or both cleared")
	}
	decoded, err := decodeQuietSlots(*slots)
	if err != nil {
		return out, err
	}
	if _, err := quietSchedule(*timezone, decoded); err != nil {
		return out, err
	}
	// Store the canonical form, whatever whitespace or key order the caller used.
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return out, err
	}
	js := datatypes.JSON(canonical)
	tz := strings.TrimSpace(*timezone)
	out.timezone = &tz
	out.slots = &js
	out.bypass = bypass
	return out, nil
}
