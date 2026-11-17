// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"
)

func TestCampaignRechecksMembershipBeforeDelayedStep(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &pagingResolver{members: []string{"a", "b"}}
	api.GroupTargetResolver = r
	req := cronRequest("two-steps", "0 20 * * *")
	delay := int32(3600)
	req.Steps = append(req.Steps, CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &delay})
	if _, err := api.CreateCampaign(tenantCtx(), req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:00Z"), "step-1")
	for _, device := range []string{"a", "b"} {
		if n := commandsFor(t, api, device); n != 1 {
			t.Fatalf("step 1: %s has %d commands, want 1", device, n)
		}
	}
	r.members = []string{"a"}
	tick(t, api, at("2026-06-22T01:00:00Z"), "step-2")
	if a, b := commandsFor(t, api, "a"), commandsFor(t, api, "b"); a != 2 || b != 1 {
		t.Fatalf("after step 2: commands a=%d b=%d, want 2 and 1", a, b)
	}
	var e CampaignElement
	if err := api.RDB.DB(tenantCtx()).Where("device_token = ? AND step = ?", "b", 1).Order("due_at ASC").First(&e).Error; err != nil {
		t.Fatal(err)
	}
	if e.Status != string(ElementSkipped) || e.Reason.String != SkipNotInGroup {
		t.Fatalf("step 2: b = %s/%s, want SKIPPED/NOT_IN_GROUP", e.Status, e.Reason.String)
	}
}

func TestSolarCampaignRechecksMembershipBetweenDeviceInstants(t *testing.T) {
	api := newCampaignTestApi(t)
	r := &pagingResolver{members: []string{"berlin", "london"}}
	api.GroupTargetResolver = r
	api.SiteResolver = fakeSites{sites: map[string]Site{"berlin": {52.52, 13.40}, "london": {51.51, -0.13}}}
	setClock(api, at("2026-06-21T12:00:00Z"))
	c, err := api.CreateCampaign(tenantCtx(), solarRequest("sunset", "lamps"))
	if err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T12:00:00Z"), "plan")
	var first, later CampaignElement
	for device, dest := range map[string]*CampaignElement{"berlin": &first, "london": &later} {
		if err := api.RDB.DB(tenantCtx()).Where("campaign_id = ? AND device_token = ? AND status = ?", c.ID, device, string(ElementPending)).Order("due_at ASC").First(dest).Error; err != nil {
			t.Fatal(err)
		}
	}
	if first.CycleId != later.CycleId || !later.DueAt.After(first.DueAt.Add(time.Minute)) {
		t.Fatal("fixture must have distinct solar instants in the same cycle")
	}
	tick(t, api, first.DueAt.Add(time.Second), "berlin")
	if n := commandsFor(t, api, "berlin"); n != 1 {
		t.Fatalf("berlin has %d commands, want 1", n)
	}
	r.members = []string{"berlin"}
	tick(t, api, later.DueAt.Add(time.Second), "london")
	if n := commandsFor(t, api, "london"); n != 0 {
		t.Fatalf("london left between solar instants but got %d commands", n)
	}
	if err := api.RDB.DB(tenantCtx()).First(&later, later.ID).Error; err != nil {
		t.Fatal(err)
	}
	if later.Status != string(ElementSkipped) || later.Reason.String != SkipNotInGroup {
		t.Fatalf("london = %s/%s, want SKIPPED/NOT_IN_GROUP", later.Status, later.Reason.String)
	}
}

// Exercise several cycles together without planning resolutions, and a trickle of due
// elements on consecutive passes. Resolution is shared and the freshness clock advances.
func TestMembershipRecheckFreshnessAndSharedResolution(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	r := &pagingResolver{members: []string{"a"}}
	api.GroupTargetResolver = r
	start := at("2026-06-22T00:00:00Z")
	c := &Campaign{}
	c.Token = "freshness"
	if err := api.RDB.DB(ctx).Create(c).Error; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two"} {
		cycle := &CampaignCycle{CampaignId: c.ID, CycleKey: key}
		if err := api.RDB.DB(ctx).Create(cycle).Error; err != nil {
			t.Fatal(err)
		}
		for i, seconds := range []int{0, 15, 60, 61, 76, 121, 122} {
			e := &CampaignElement{CampaignId: c.ID, CycleId: cycle.ID, Step: i, DeviceToken: "a", Status: string(ElementPending), DueAt: start.Add(time.Duration(seconds) * time.Second)}
			if err := api.RDB.DB(ctx).Create(e).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, seconds := range []int{0, 15, 60, 61, 76, 121, 122} {
		now := start.Add(time.Duration(seconds) * time.Second)
		setClock(api, now)
		blocked, err := api.recheckDueMembership(ctx, c, now)
		if err != nil || len(blocked) != 0 {
			t.Fatalf("pass %d: blocked=%v err=%v", seconds, blocked, err)
		}
		wantCalls := []int{1, 1, 1, 2, 2, 2, 3}[i]
		if r.pages != wantCalls {
			t.Fatalf("pass %d: resolutions=%d, want %d", seconds, r.pages, wantCalls)
		}
		var cycles []CampaignCycle
		if err := api.RDB.DB(ctx).Find(&cycles).Error; err != nil {
			t.Fatal(err)
		}
		last := []int{0, 0, 0, 61, 61, 61, 122}[i]
		for _, cycle := range cycles {
			if !cycle.MembershipCheckedAt.Valid || !cycle.MembershipCheckedAt.Time.Equal(start.Add(time.Duration(last)*time.Second)) {
				t.Fatalf("pass %d: cycle %d checked at %v, want +%ds", seconds, cycle.ID, cycle.MembershipCheckedAt, last)
			}
		}
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("due_at <= ?", now).Update("status", string(ElementDispatched)).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A due element left pending by a claim-page limit is still covered by its check,
	// even when the next pass is much later.
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("step = ?", 6).Update("status", string(ElementPending)).Error; err != nil {
		t.Fatal(err)
	}
	setClock(api, start.Add(5*time.Minute))
	if blocked, err := api.recheckDueMembership(ctx, c, start.Add(5*time.Minute)); err != nil || len(blocked) != 0 || r.pages != 3 {
		t.Fatalf("already checked backlog: blocked=%v err=%v resolutions=%d, want 3", blocked, err, r.pages)
	}
	// An unresolvable group blocks every stale cycle and never stamps a successful check.
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("step = ?", 6).Update("due_at", start.Add(6*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	r.rejectWith = "group unavailable"
	now := start.Add(6 * time.Minute)
	setClock(api, now)
	if blocked, err := api.recheckDueMembership(ctx, c, now); err == nil || len(blocked) != 2 || r.pages != 4 {
		t.Fatalf("failed recheck: blocked=%v err=%v resolutions=%d", blocked, err, r.pages)
	}
	var cycles []CampaignCycle
	if err := api.RDB.DB(ctx).Find(&cycles).Error; err != nil {
		t.Fatal(err)
	}
	for _, cycle := range cycles {
		if !cycle.MembershipCheckedAt.Time.Equal(start.Add(122 * time.Second)) {
			t.Fatalf("failed resolution advanced timestamp to %v", cycle.MembershipCheckedAt)
		}
	}
	r.rejectWith = ""
	if blocked, err := api.recheckDueMembership(ctx, c, now); err != nil || len(blocked) != 0 || r.pages != 5 {
		t.Fatalf("retry: blocked=%v err=%v resolutions=%d", blocked, err, r.pages)
	}
}
