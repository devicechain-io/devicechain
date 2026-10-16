// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"gorm.io/datatypes"
)

// quietPolicy builds a policy carrying the given quiet hours, as a stored row would.
func quietPolicy(t *testing.T, tz, slots, bypass string) *NotificationPolicy {
	t.Helper()
	p := &NotificationPolicy{
		QuietHoursTimezone: sql.NullString{String: tz, Valid: true},
	}
	js := datatypes.JSON(slots)
	p.QuietHoursSlots = &js
	if bypass != "" {
		p.QuietHoursBypassSeverity = sql.NullString{String: bypass, Valid: true}
	}
	return p
}

func mustUTC(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// requireInvalid asserts err is a quiet-hours refusal that answers INVALID_VALUE on the
// wire, and that its sentence says what is wrong.
func requireInvalid(t *testing.T, err error, mentions string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an INVALID_VALUE refusal mentioning %q, got success", mentions)
	}
	class, ok := integrity.Refused(err)
	if !ok {
		t.Fatalf("err %q carries no refusal class; the wire would serve it with no code", err)
	}
	if code, _, _ := integrity.Answer(class); code != "INVALID_VALUE" {
		t.Fatalf("code = %s, want INVALID_VALUE (%v)", code, err)
	}
	if !errors.Is(err, ErrInvalidQuietHours) {
		t.Fatalf("err %v does not wrap ErrInvalidQuietHours", err)
	}
	if !strings.Contains(err.Error(), mentions) {
		t.Fatalf("err %q does not mention %q", err, mentions)
	}
	if strings.Contains(err.Error(), "maintenance window") {
		t.Fatalf("err %q calls quiet hours a maintenance window", err)
	}
}

func TestQuietHoursValidation(t *testing.T) {
	s := func(v string) *string { return &v }
	berlin := s("Europe/Berlin")
	night := s(`[{"day":"WEDNESDAY","start":"22:00","durationMinutes":480}]`)

	for _, tc := range []struct {
		name              string
		tz, slots, bypass *string
		mentions          string
	}{
		{"zone without slots", berlin, nil, nil, "set together"},
		{"slots without zone", nil, night, nil, "set together"},
		{"local zone", s("Local"), night, nil, "not an IANA zone"},
		{"empty zone", s(""), night, nil, "timezone is required"},
		{"unknown zone", s("Mars/Olympus"), night, nil, "not a known IANA zone"},
		{"zero-length slot", berlin, s(`[{"day":"MONDAY","start":"01:00","durationMinutes":0}]`), nil, "duration 0"},
		{"no slots", berlin, s(`[]`), nil, "at least one"},
		{"bad day", berlin, s(`[{"day":"monday","start":"01:00","durationMinutes":60}]`), nil, "not one of"},
		{"bad clock", berlin, s(`[{"day":"MONDAY","start":"24:00","durationMinutes":60}]`), nil, "HH:MM"},
		{"unknown key", berlin, s(`[{"day":"MONDAY","start":"01:00","durationMinutes":60,"end":"02:00"}]`), nil, "unknown field"},
		{"not an array", berlin, s(`{"day":"MONDAY"}`), nil, "JSON array"},
		{"overlap", berlin, s(`[{"day":"MONDAY","start":"01:00","durationMinutes":120},` +
			`{"day":"MONDAY","start":"02:30","durationMinutes":60}]`), nil, "slots 1 and 2 overlap"},
		{"overlap across midnight", berlin, s(`[{"day":"TUESDAY","start":"00:30","durationMinutes":30},` +
			`{"day":"MONDAY","start":"23:00","durationMinutes":120}]`), nil, "slots 1 and 2 overlap"},
		{"overlap across the week's end", berlin, s(`[{"day":"MONDAY","start":"00:30","durationMinutes":30},` +
			`{"day":"SUNDAY","start":"23:00","durationMinutes":120}]`), nil, "slots 1 and 2 overlap"},
		{"same slot twice", berlin, s(`[{"day":"FRIDAY","start":"20:00","durationMinutes":60},` +
			`{"day":"FRIDAY","start":"20:00","durationMinutes":60}]`), nil, "overlap"},
		{"whole week", berlin, s(`[{"day":"MONDAY","start":"00:00","durationMinutes":10080}]`), nil, "whole week"},
		{"whole week in pieces", berlin, s(`[{"day":"MONDAY","start":"00:00","durationMinutes":5040},` +
			`{"day":"THURSDAY","start":"12:00","durationMinutes":5040}]`), nil, "whole week"},
		{"wildcard bypass", berlin, night, s("*"), "not an alarm tier"},
		{"lowercase bypass", berlin, night, s("critical"), "not an alarm tier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateQuietHours(tc.tz, tc.slots, tc.bypass)
			requireInvalid(t, err, tc.mentions)
		})
	}

	for _, tc := range []struct {
		name              string
		tz, slots, bypass *string
	}{
		{"none", nil, nil, nil},
		{"bypass alone is inert", nil, nil, s("CRITICAL")},
		{"one night", berlin, night, nil},
		{"with bypass", berlin, night, s("MAJOR")},
		{"touching slots", berlin, s(`[{"day":"MONDAY","start":"22:00","durationMinutes":120},` +
			`{"day":"TUESDAY","start":"00:00","durationMinutes":360}]`), nil},
		{"sunday into monday", berlin, s(`[{"day":"SUNDAY","start":"22:00","durationMinutes":480},` +
			`{"day":"MONDAY","start":"06:00","durationMinutes":60}]`), nil},
		{"one minute short of the week", berlin, s(`[{"day":"MONDAY","start":"00:00","durationMinutes":10079}]`), nil},
	} {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := validateQuietHours(tc.tz, tc.slots, tc.bypass); err != nil {
				t.Fatalf("refused a valid configuration: %v", err)
			}
		})
	}
}

// The stored slots are the canonical encoding, whatever the caller sent.
func TestQuietHoursAreStoredCanonically(t *testing.T) {
	tz, slots := " Europe/Berlin ", "[ {\"durationMinutes\": 60, \"start\":\"01:00\", \"day\":\"MONDAY\"} ]"
	cols, err := validateQuietHours(&tz, &slots, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(*cols.slots); got != `[{"day":"MONDAY","start":"01:00","durationMinutes":60}]` {
		t.Fatalf("stored slots = %s", got)
	}
	if *cols.timezone != "Europe/Berlin" {
		t.Fatalf("stored zone = %q", *cols.timezone)
	}
}

func TestHoldsByWallClockInTheZone(t *testing.T) {
	// Wednesday 22:00 for eight hours, Berlin time: 20:00Z Wednesday to 04:00Z Thursday in
	// October 2026 (CEST, UTC+2).
	p := quietPolicy(t, "Europe/Berlin", `[{"day":"WEDNESDAY","start":"22:00","durationMinutes":480}]`, "CRITICAL")
	q, err := p.QuietHours()
	if err != nil {
		t.Fatal(err)
	}
	end := mustUTC(t, "2026-10-15T04:00:00Z")
	for _, tc := range []struct {
		at       string
		severity string
		held     bool
	}{
		{"2026-10-14T19:59:00Z", "MAJOR", false},  // 21:59 local, before
		{"2026-10-14T20:00:00Z", "MAJOR", true},   // 22:00 local, the start is inside
		{"2026-10-14T23:30:00Z", "MAJOR", true},   // past midnight local
		{"2026-10-15T03:59:00Z", "WARNING", true}, // the last minute
		{"2026-10-15T04:00:00Z", "MAJOR", false},  // the end is outside
		{"2026-10-14T23:30:00Z", "CRITICAL", false},
		{"2026-10-14T23:30:00Z", "INDETERMINATE", true},
		// A severity this service cannot rank against the bypass is sent.
		{"2026-10-14T23:30:00Z", "SEVERE", false},
		// UTC 22:00 Wednesday is NOT in quiet hours: the zone is Berlin's, not the server's.
		{"2026-10-14T19:30:00Z", "MAJOR", false},
	} {
		held, until := q.Holds(tc.severity, mustUTC(t, tc.at))
		if held != tc.held {
			t.Errorf("Holds(%s, %s) = %v, want %v", tc.severity, tc.at, held, tc.held)
		}
		if held && !until.Equal(end) {
			t.Errorf("Holds(%s, %s) until %s, want %s", tc.severity, tc.at, until.UTC(), end)
		}
	}
}

func TestBypassIsAtOrAbove(t *testing.T) {
	p := quietPolicy(t, "UTC", `[{"day":"MONDAY","start":"00:00","durationMinutes":600}]`, "MAJOR")
	q, err := p.QuietHours()
	if err != nil {
		t.Fatal(err)
	}
	at := mustUTC(t, "2026-10-12T01:00:00Z") // a Monday
	for sev, held := range map[string]bool{
		"CRITICAL": false, "MAJOR": false, "MINOR": true, "WARNING": true, "INDETERMINATE": true,
	} {
		if got, _ := q.Holds(sev, at); got != held {
			t.Errorf("bypass MAJOR, severity %s: held = %v, want %v", sev, got, held)
		}
	}
	// No bypass holds everything.
	p.QuietHoursBypassSeverity = sql.NullString{}
	q, _ = p.QuietHours()
	if got, _ := q.Holds("CRITICAL", at); !got {
		t.Error("with no bypass, CRITICAL must be held")
	}
}

// Daylight saving, at both of New York's 2026 transitions. The rules are the maintenance
// window's: a start in a fall-back overlap opens at its FIRST occurrence, a start in a
// spring-forward gap opens when the clock jumps, and a duration is elapsed time.
func TestQuietHoursAcrossDaylightSaving(t *testing.T) {
	t.Run("fall back", func(t *testing.T) {
		// Sunday 2026-11-01: 02:00 EDT becomes 01:00 EST. 01:00 happens twice; the first
		// is 05:00Z. Two elapsed hours end at 07:00Z (02:00 EST), not at 08:00Z.
		p := quietPolicy(t, "America/New_York", `[{"day":"SUNDAY","start":"01:00","durationMinutes":120}]`, "")
		q, err := p.QuietHours()
		if err != nil {
			t.Fatal(err)
		}
		end := mustUTC(t, "2026-11-01T07:00:00Z")
		for at, want := range map[string]bool{
			"2026-11-01T04:59:00Z": false, // 00:59 EDT
			"2026-11-01T05:00:00Z": true,  // 01:00 EDT, the first 01:00
			"2026-11-01T06:30:00Z": true,  // 01:30 EST, the second 01:30, still inside
			"2026-11-01T06:59:00Z": true,
			"2026-11-01T07:00:00Z": false, // 02:00 EST
		} {
			held, until := q.Holds("MAJOR", mustUTC(t, at))
			if held != want {
				t.Errorf("at %s held = %v, want %v", at, held, want)
			}
			if held && !until.Equal(end) {
				t.Errorf("at %s until %s, want %s", at, until.UTC(), end)
			}
		}
	})
	t.Run("spring forward", func(t *testing.T) {
		// Sunday 2026-03-08: 02:00 EST jumps to 03:00 EDT. 02:30 does not exist; the slot
		// opens at the jump (07:00Z) and runs one elapsed hour to 08:00Z.
		p := quietPolicy(t, "America/New_York", `[{"day":"SUNDAY","start":"02:30","durationMinutes":60}]`, "")
		q, err := p.QuietHours()
		if err != nil {
			t.Fatal(err)
		}
		end := mustUTC(t, "2026-03-08T08:00:00Z")
		for at, want := range map[string]bool{
			"2026-03-08T06:59:00Z": false, // 01:59 EST
			"2026-03-08T07:00:00Z": true,  // 03:00 EDT, the instant the clock jumps
			"2026-03-08T07:59:00Z": true,
			"2026-03-08T08:00:00Z": false,
		} {
			held, until := q.Holds("MAJOR", mustUTC(t, at))
			if held != want {
				t.Errorf("at %s held = %v, want %v", at, held, want)
			}
			if held && !until.Equal(end) {
				t.Errorf("at %s until %s, want %s", at, until.UTC(), end)
			}
		}
	})
}

func TestStoredQuietHoursThatDoNotValidateAreAnError(t *testing.T) {
	p := quietPolicy(t, "Local", `[{"day":"MONDAY","start":"01:00","durationMinutes":60}]`, "")
	if q, err := p.QuietHours(); err == nil || q != nil {
		t.Fatalf("a stored zone the write would refuse must not read as quiet hours: q=%v err=%v", q, err)
	}
	half := &NotificationPolicy{QuietHoursTimezone: sql.NullString{String: "UTC", Valid: true}}
	if _, err := half.QuietHours(); err == nil {
		t.Fatal("a stored zone without slots must be an error, not 'no quiet hours'")
	}
	none := &NotificationPolicy{}
	if q, err := none.QuietHours(); err != nil || q != nil {
		t.Fatalf("no quiet hours: q=%v err=%v", q, err)
	}
}

// The API: create and update validate and store the three columns, an update validates
// the FOLDED values, and policy edits and deletes reach the notifications it holds.
func TestPolicyQuietHoursThroughTheApi(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("acme")
	slots := `[{"day":"WEDNESDAY","start":"22:00","durationMinutes":480}]`

	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "bad", Enabled: true, QuietHoursTimezone: strPtr("Europe/Berlin"),
	}); err == nil {
		t.Fatal("create accepted a zone with no slots")
	}
	created, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "p", Enabled: true,
		QuietHoursTimezone:       strPtr("Europe/Berlin"),
		QuietHoursSlots:          strPtr(slots),
		QuietHoursBypassSeverity: strPtr("CRITICAL"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.QuietHoursTimezone.String != "Europe/Berlin" || string(*created.QuietHoursSlots) != slots ||
		created.QuietHoursBypassSeverity.String != "CRITICAL" {
		t.Fatalf("created quiet hours = %v %v %v", created.QuietHoursTimezone,
			created.QuietHoursSlots, created.QuietHoursBypassSeverity)
	}

	// Clearing only the zone leaves slots with no zone: refused, nothing written.
	req := &NotificationPolicyUpdateRequest{}
	req.QuietHoursTimezone.Set = true
	_, err = api.UpdateNotificationPolicy(ctx, "p", req, nil)
	requireInvalid(t, err, "set together")

	// An overlapping set is refused on update too.
	req = &NotificationPolicyUpdateRequest{}
	overlap := `[{"day":"MONDAY","start":"01:00","durationMinutes":120},{"day":"MONDAY","start":"02:00","durationMinutes":60}]`
	req.QuietHoursSlots.Set, req.QuietHoursSlots.Value = true, &overlap
	_, err = api.UpdateNotificationPolicy(ctx, "p", req, nil)
	requireInvalid(t, err, "overlap")

	// A held notification, due tomorrow morning.
	tomorrow := time.Now().Add(24 * time.Hour).UTC()
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{{
		AlarmToken: "a1", PolicyId: created.ID, AlarmKey: "k", Severity: "MAJOR",
		OccurredTime: time.Now().UTC(), ReleaseAt: tomorrow, Event: datatypes.JSON(`{}`),
	}}); err != nil {
		t.Fatal(err)
	}
	if due, _ := api.DueNotificationDeferrals(ctx, time.Now(), 10); len(due) != 0 {
		t.Fatalf("a deferral due tomorrow is due now: %d", len(due))
	}

	// Editing the quiet hours brings it due now, so the next pass re-evaluates it.
	req = &NotificationPolicyUpdateRequest{}
	other := `[{"day":"MONDAY","start":"01:00","durationMinutes":60}]`
	req.QuietHoursSlots.Set, req.QuietHoursSlots.Value = true, &other
	updated, err := api.UpdateNotificationPolicy(ctx, "p", req, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if string(*updated.QuietHoursSlots) != other || updated.QuietHoursTimezone.String != "Europe/Berlin" {
		t.Fatalf("updated quiet hours = %v %v", updated.QuietHoursTimezone, updated.QuietHoursSlots)
	}
	if due, _ := api.DueNotificationDeferrals(ctx, time.Now().Add(time.Second), 10); len(due) != 1 {
		t.Fatalf("editing quiet hours must bring what the policy holds due now; due = %d", len(due))
	}

	// An edit that does not touch quiet hours or enabled leaves the release alone.
	if err := api.RescheduleNotificationDeferral(ctx, mustOneDeferral(t, api), tomorrow); err != nil {
		t.Fatal(err)
	}
	req = &NotificationPolicyUpdateRequest{}
	req.Name.Set, req.Name.Value = true, strPtr("renamed")
	if _, err := api.UpdateNotificationPolicy(ctx, "p", req, nil); err != nil {
		t.Fatal(err)
	}
	if due, _ := api.DueNotificationDeferrals(ctx, time.Now().Add(time.Second), 10); len(due) != 0 {
		t.Fatalf("a rename must not release held notifications early; due = %d", len(due))
	}

	// Clearing the pair (bypass left as it was) is accepted.
	req = &NotificationPolicyUpdateRequest{}
	req.QuietHoursTimezone.Set, req.QuietHoursSlots.Set = true, true
	cleared, err := api.UpdateNotificationPolicy(ctx, "p", req, nil)
	if err != nil {
		t.Fatalf("clearing quiet hours: %v", err)
	}
	if cleared.QuietHoursTimezone.Valid || cleared.QuietHoursSlots != nil {
		t.Fatalf("quiet hours not cleared: %v %v", cleared.QuietHoursTimezone, cleared.QuietHoursSlots)
	}
	if q, err := cleared.QuietHours(); err != nil || q != nil {
		t.Fatalf("cleared policy reads quiet hours %v, %v", q, err)
	}

	// Deleting the policy deletes what it holds.
	if _, err := api.DeleteNotificationPolicy(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if held, _ := api.NotificationDeferralsByAlarmToken(ctx, []string{"a1"}); len(held) != 0 {
		t.Fatalf("a deleted policy still holds %d notification(s)", len(held))
	}
}

func mustOneDeferral(t *testing.T, api *Api) *NotificationDeferral {
	t.Helper()
	held, err := api.NotificationDeferralsByAlarmToken(tenantCtx("acme"), []string{"a1"})
	if err != nil || len(held) != 1 {
		t.Fatalf("held = %d, err = %v", len(held), err)
	}
	return held[0]
}

// One row per (alarm, policy); a newer event replaces the held one, an OLDER one (an
// out-of-order redelivery) does not, and the claim is a compare-and-swap that a
// replacement defeats.
func TestDeferralUpsertAndClaim(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("acme")
	t0 := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	release := t0.Add(7 * time.Hour)
	held := func(sev string, occurred time.Time) *NotificationDeferral {
		return &NotificationDeferral{AlarmToken: "a1", PolicyId: 7, AlarmKey: "k", Severity: sev,
			OccurredTime: occurred, ReleaseAt: release, Event: datatypes.JSON(`{"Severity":"` + sev + `"}`)}
	}
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MINOR", t0)}); err != nil {
		t.Fatal(err)
	}
	first := mustOneDeferral(t, api)

	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MAJOR", t0.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	got := mustOneDeferral(t, api)
	if got.Severity != "MAJOR" || string(got.Event) != `{"Severity":"MAJOR"}` || !got.OccurredTime.Equal(t0.Add(time.Minute)) {
		t.Fatalf("a newer event did not replace the held one: %s %s %s", got.Severity, got.Event, got.OccurredTime)
	}
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("WARNING", t0.Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
	got = mustOneDeferral(t, api)
	if got.Severity != "MAJOR" {
		t.Fatalf("an OLDER event overwrote the held one: %s", got.Severity)
	}

	// The copy read before the replacement cannot be claimed.
	if won, err := api.ClaimNotificationDeferral(ctx, first); err != nil || won {
		t.Fatalf("claim of a replaced copy: won=%v err=%v", won, err)
	}
	if won, err := api.ClaimNotificationDeferral(ctx, got); err != nil || !won {
		t.Fatalf("claim of the current copy: won=%v err=%v", won, err)
	}
	if won, _ := api.ClaimNotificationDeferral(ctx, got); won {
		t.Fatal("a second claim of the same row won")
	}

	// Another tenant never sees it.
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MINOR", t0)}); err != nil {
		t.Fatal(err)
	}
	if other, _ := api.DueNotificationDeferrals(tenantCtx("other"), release, 10); len(other) != 0 {
		t.Fatalf("tenant isolation: other tenant sees %d held notification(s)", len(other))
	}
	if due, _ := api.DueNotificationDeferrals(ctx, release.Add(-time.Second), 10); len(due) != 0 {
		t.Fatal("due before its release time")
	}
	if due, _ := api.DueNotificationDeferrals(ctx, release, 10); len(due) != 1 {
		t.Fatal("not due at its release time")
	}
}
