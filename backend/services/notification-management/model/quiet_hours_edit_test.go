// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"gorm.io/datatypes"
)

// A policy whose STORED quiet hours no longer validate can still be edited — above all,
// disabled — without the request having to rewrite its quiet hours first. The dispatcher
// already ignores such quiet hours and pages; refusing every other edit would leave the
// policy impossible to switch off.
func TestAPolicyWithUnvalidatedQuietHoursCanStillBeDisabled(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("acme")
	slots := `[{"day":"WEDNESDAY","start":"22:00","durationMinutes":480}]`
	if _, err := api.CreateNotificationPolicy(ctx, &NotificationPolicyCreateRequest{
		Token: "p", Enabled: true,
		QuietHoursTimezone: strPtr("Europe/Berlin"), QuietHoursSlots: strPtr(slots),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A hand edit the write path would refuse.
	if err := api.RDB.DB(ctx).Model(&NotificationPolicy{}).Where("token = ?", "p").
		Update("quiet_hours_timezone", "Local").Error; err != nil {
		t.Fatal(err)
	}

	off := false
	req := &NotificationPolicyUpdateRequest{}
	req.Enabled.Set, req.Enabled.Value = true, &off
	updated, err := api.UpdateNotificationPolicy(ctx, "p", req, nil)
	if err != nil {
		t.Fatalf("disabling a policy with unvalidated quiet hours was refused: %v", err)
	}
	if updated.Enabled {
		t.Fatal("the policy is still enabled")
	}
	// Untouched: the stored triple is kept exactly as it was.
	if updated.QuietHoursTimezone.String != "Local" || string(*updated.QuietHoursSlots) != slots {
		t.Fatalf("the stored quiet hours moved: %v %s", updated.QuietHoursTimezone, *updated.QuietHoursSlots)
	}

	// Naming a quiet-hours field still validates the folded triple.
	bypass := "CRITICAL"
	req = &NotificationPolicyUpdateRequest{}
	req.QuietHoursBypassSeverity.Set, req.QuietHoursBypassSeverity.Value = true, &bypass
	if _, err := api.UpdateNotificationPolicy(ctx, "p", req, nil); err == nil {
		t.Fatal("an edit naming a quiet-hours field skipped validation of the stored zone")
	}
}

// The release's lease is a compare-and-swap like the drop's claim; finishing or retrying a
// leased row leaves alone a NEWER event that replaced it while it was being sent; and a
// resolution only answers what occurred at or before it.
func TestDeferralLeaseFinishRetryAndResolve(t *testing.T) {
	api := newTestApi(t)
	ctx := tenantCtx("acme")
	t0 := time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)
	release := t0.Add(7 * time.Hour)
	held := func(sev string, occurred time.Time) *NotificationDeferral {
		return &NotificationDeferral{AlarmToken: "a1", PolicyId: 7, AlarmKey: "k", Severity: sev,
			OccurredTime: occurred, ReleaseAt: release, Event: datatypes.JSON(`{}`)}
	}
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MINOR", t0)}); err != nil {
		t.Fatal(err)
	}
	read := mustOneDeferral(t, api)
	stale := *read

	if won, err := api.LeaseNotificationDeferral(ctx, read, release.Add(time.Minute)); err != nil || !won {
		t.Fatalf("first lease: won=%v err=%v", won, err)
	}
	if won, _ := api.LeaseNotificationDeferral(ctx, &stale, release.Add(time.Minute)); won {
		t.Fatal("a second replica's lease of the same read won")
	}
	if due, _ := api.DueNotificationDeferrals(ctx, release, 10); len(due) != 0 {
		t.Fatal("a leased row is still due before its lease ends")
	}

	// A newer event replaces the row mid-send: finishing the old one must keep it.
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MAJOR", t0.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if err := api.FinishNotificationDeferral(ctx, read); err != nil {
		t.Fatal(err)
	}
	if err := api.RetryNotificationDeferral(ctx, read, release); err != nil {
		t.Fatal(err)
	}
	got := mustOneDeferral(t, api)
	if got.Severity != "MAJOR" || got.Attempts != 0 {
		t.Fatalf("finishing/retrying the replaced copy touched the newer event: %+v", got)
	}

	// A resolution before the held event leaves it; one at or after it drops it.
	if n, err := api.ResolveNotificationDeferrals(ctx, "a1", t0); err != nil || n != 0 {
		t.Fatalf("an earlier resolution dropped %d row(s), err %v", n, err)
	}
	if n, err := api.ResolveNotificationDeferrals(ctx, "a1", t0.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("a resolution at the held event dropped %d row(s), err %v", n, err)
	}

	// Supersede: only the named policies, only events at or before the page.
	if err := api.DeferNotifications(ctx, []*NotificationDeferral{held("MAJOR", t0.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if n, _ := api.SupersedeNotificationDeferrals(ctx, "a1", []uint{8}, t0.Add(2*time.Hour)); n != 0 {
		t.Fatal("superseded another policy's held page")
	}
	if n, _ := api.SupersedeNotificationDeferrals(ctx, "a1", []uint{7}, t0.Add(30*time.Minute)); n != 0 {
		t.Fatal("an OLDER page superseded a newer held event")
	}
	if n, _ := api.SupersedeNotificationDeferrals(ctx, "a1", []uint{7}, t0.Add(time.Hour)); n != 1 {
		t.Fatal("a page about the same event did not supersede it")
	}
}
