// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestCampaignMembershipPreservesFutureSteps(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	r := &pagingResolver{members: []string{"a"}}
	api.GroupTargetResolver = r
	req := cronRequest("rejoin", "0 20 * * *")
	for i := 0; i < 2; i++ {
		delay := int32((i + 1) * 3600)
		req.Steps = append(req.Steps, CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &delay})
	}
	c, err := api.CreateCampaign(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	var elements []CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).Order("due_at ASC").Limit(3).Find(&elements).Error; err != nil {
		t.Fatal(err)
	}
	if len(elements) != 3 || !elements[1].DueAt.After(elements[0].DueAt.Add(campaignMembershipFreshness)) || !elements[2].DueAt.After(elements[1].DueAt.Add(campaignMembershipFreshness)) {
		t.Fatal("fixture needs three separated steps")
	}
	now := elements[0].DueAt
	// The scan itself must remain bounded, even though the UPDATE also guards due_at.
	if err := api.RDB.Database.Callback().Query().After("gorm:query").Register("test:membership-due-scan", func(tx *gorm.DB) {
		if rows, ok := tx.Statement.Dest.(*[]CampaignElement); ok && len(tx.Statement.Selects) == 2 && tx.Statement.Selects[0] == "id" && tx.Statement.Selects[1] == "device_token" {
			for _, row := range *rows {
				for _, e := range elements {
					if row.ID == e.ID && e.DueAt.After(now) {
						t.Errorf("membership scan fetched future step %d", e.Step)
					}
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	check := func(index int, status, reason string) {
		t.Helper()
		var e CampaignElement
		if err := api.RDB.DB(ctx).First(&e, elements[index].ID).Error; err != nil {
			t.Fatal(err)
		}
		if e.Status != status || e.Reason.String != reason {
			t.Fatalf("step %d = %s/%s, want %s/%s", index+1, e.Status, e.Reason.String, status, reason)
		}
	}
	r.members = nil
	tick(t, api, now, "step-1-out")
	check(0, string(ElementSkipped), SkipNotInGroup)
	check(1, string(ElementPending), "")
	check(2, string(ElementPending), "")
	if n := commandsFor(t, api, "a"); n != 0 {
		t.Fatalf("out-of-group device received %d commands", n)
	}
	now = elements[1].DueAt
	tick(t, api, now, "step-2-out")
	check(1, string(ElementSkipped), SkipNotInGroup)
	check(2, string(ElementPending), "")
	r.members = []string{"a"}
	pages := r.pages
	now = elements[2].DueAt
	tick(t, api, now, "step-3-rejoined")
	check(2, string(ElementDispatched), "")
	if n := commandsFor(t, api, "a"); n != 1 || r.pages <= pages {
		t.Fatalf("rejoined device: commands=%d resolutions=%d (previously %d)", n, r.pages, pages)
	}
	var cycle CampaignCycle
	if err := api.RDB.DB(ctx).First(&cycle, elements[0].CycleId).Error; err != nil {
		t.Fatal(err)
	}
	if !cycle.MembershipCheckedAt.Valid || !cycle.MembershipCheckedAt.Time.Equal(now) {
		t.Fatalf("membership check timestamp = %v, want %v", cycle.MembershipCheckedAt, now)
	}
}

func TestMembershipSkipUpdateRechecksDueTime(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{}
	now := at("2026-06-22T00:00:00Z")
	setClock(api, now)
	c := &Campaign{}
	c.Token = "rescheduled"
	if err := api.RDB.DB(ctx).Create(c).Error; err != nil {
		t.Fatal(err)
	}
	cycle := &CampaignCycle{CampaignId: c.ID, CycleKey: "one"}
	if err := api.RDB.DB(ctx).Create(cycle).Error; err != nil {
		t.Fatal(err)
	}
	e := &CampaignElement{CampaignId: c.ID, CycleId: cycle.ID, DeviceToken: "a", Status: string(ElementPending), DueAt: now}
	if err := api.RDB.DB(ctx).Create(e).Error; err != nil {
		t.Fatal(err)
	}
	moved := false
	if err := api.RDB.Database.Callback().Query().After("gorm:query").Register("test:move-due-after-scan", func(tx *gorm.DB) {
		if rows, ok := tx.Statement.Dest.(*[]CampaignElement); ok && len(*rows) == 1 && len(tx.Statement.Selects) == 2 && tx.Statement.Selects[1] == "device_token" && !moved {
			moved = true
			if err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE campaign_elements SET due_at = ? WHERE tenant_id = ? AND id = ?", now.Add(time.Hour), "acme", e.ID).Error; err != nil {
				t.Error(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if blocked, err := api.recheckDueMembership(ctx, c, now); err != nil || len(blocked) != 0 {
		t.Fatalf("blocked=%v err=%v", blocked, err)
	}
	if err := api.RDB.DB(ctx).First(e, e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !moved || e.Status != string(ElementPending) || e.Reason.Valid || !e.DueAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("rescheduled element: moved=%v status=%s reason=%v due=%v", moved, e.Status, e.Reason, e.DueAt)
	}
}

// A step due inside the freshness window of a check is never re-checked by the trigger, so the
// check that saw its device leave must answer for it: keeping it PENDING would send it to a
// device known to be outside the group.
func TestMembershipCheckJudgesStepsInsideFreshnessWindow(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	r := &pagingResolver{members: []string{"a"}}
	api.GroupTargetResolver = r
	req := cronRequest("close-steps", "0 20 * * *")
	near := int32(campaignMembershipFreshness / time.Second / 2)
	far := int32(3600)
	req.Steps = append(req.Steps,
		CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &near},
		CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &far})
	c, err := api.CreateCampaign(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	var elements []CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).Order("due_at ASC").Limit(3).Find(&elements).Error; err != nil {
		t.Fatal(err)
	}
	if len(elements) != 3 || elements[1].DueAt.After(elements[0].DueAt.Add(campaignMembershipFreshness)) {
		t.Fatal("fixture needs a second step inside the freshness window")
	}
	r.members = nil
	tick(t, api, elements[0].DueAt, "step-1-out")
	r.members = []string{"a"}
	tick(t, api, elements[1].DueAt, "step-2-rejoined-within-window")
	for i, want := range []string{string(ElementSkipped), string(ElementSkipped), string(ElementPending)} {
		var e CampaignElement
		if err := api.RDB.DB(ctx).First(&e, elements[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if e.Status != want {
			t.Fatalf("step %d = %s, want %s", i+1, e.Status, want)
		}
	}
	if n := commandsFor(t, api, "a"); n != 0 {
		t.Fatalf("device outside the group at the last check received %d commands", n)
	}
	tick(t, api, elements[2].DueAt, "step-3-rejoined")
	if n := commandsFor(t, api, "a"); n != 1 {
		t.Fatalf("rejoined device received %d commands at step 3, want 1", n)
	}
}

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
