// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/solar"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// fakeSites serves fixed installed locations; a device not in the map has none.
type fakeSites struct {
	sites map[string]Site
	err   error
}

func (f fakeSites) DeviceSites(_ context.Context, tokens []string) (map[string]Site, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]Site{}
	for _, t := range tokens {
		if s, ok := f.sites[t]; ok {
			out[t] = s
		}
	}
	return out, nil
}

// newCampaignTestApi builds an Api over ONE connection, so goroutines racing a pass
// interleave at statement granularity (which is where a claim race lives) without the
// separate in-memory databases a pooled SQLite would hand each connection.
func newCampaignTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Command{}, &CommandBatch{}, &DeviceGate{}, &DeviceGateSync{},
		&Campaign{}, &CampaignCycle{}, &CampaignElement{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, m := range []any{&Command{}, &CommandBatch{}, &Campaign{}} {
		if err := rdb.CreateTenantTokenIndex(db, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, ddl := range []string{
		`CREATE UNIQUE INDEX uix_campaign_cycles_key ON campaign_cycles (tenant_id, campaign_id, cycle_key)`,
		`CREATE UNIQUE INDEX uix_campaign_elements_device ON campaign_elements (tenant_id, cycle_id, step, device_token)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	// Campaigns are created before the first tick of a test unless the test says otherwise.
	created := at("2026-06-01T00:00:00Z")
	api.campaignNow = func() time.Time { return created }
	return api
}

func strp(s string) *string { return &s }

func tenantCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

func solarRequest(token, group string) *CampaignCreateRequest {
	return &CampaignCreateRequest{
		Token:      token,
		Trigger:    `{"kind":"SOLAR","zone":"Europe/London","event":"SUNSET"}`,
		Steps:      []CampaignStepInput{{Type: StepCommand, Name: strp("dim"), Payload: strp(`{"level":30}`)}},
		GroupToken: group,
	}
}

func commandsFor(t *testing.T, api *Api, device string) int {
	t.Helper()
	var n int64
	if err := api.RDB.DB(tenantCtx()).Model(&Command{}).Where("device_token = ?", device).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func tick(t *testing.T, api *Api, now time.Time, pass string) {
	t.Helper()
	api.campaignNow = func() time.Time { return now }
	if err := api.TickCampaigns(context.Background(), now, pass); err != nil {
		t.Fatalf("tick %s: %v", pass, err)
	}
}

func TestCreateCampaignValidation(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	cases := []struct {
		name string
		edit func(*CampaignCreateRequest)
		code RejectionCode
	}{
		{"bad trigger", func(r *CampaignCreateRequest) { r.Trigger = `{"kind":"CRON","zone":"UTC","cron":"61 * * * *"}` }, RejectCampaignTriggerInvalid},
		{"unknown trigger field", func(r *CampaignCreateRequest) { r.Trigger = `{"kind":"CRON","zone":"UTC","cron":"* * * * *","x":1}` }, RejectCampaignTriggerInvalid},
		{"no zone", func(r *CampaignCreateRequest) { r.Trigger = `{"kind":"CRON","cron":"* * * * *"}` }, RejectCampaignTriggerInvalid},
		{"no steps", func(r *CampaignCreateRequest) { r.Steps = nil }, RejectCampaignStepsInvalid},
		{"artifact step is recognised and refused", func(r *CampaignCreateRequest) {
			r.Steps = []CampaignStepInput{{Type: StepArtifact}}
		}, RejectCampaignStepUnsupported},
		{"unknown step type", func(r *CampaignCreateRequest) { r.Steps[0].Type = "SHELL" }, RejectCampaignStepsInvalid},
		{"payload not json", func(r *CampaignCreateRequest) { r.Steps[0].Payload = strp("{") }, RejectPayloadNotJSON},
		{"negative delay", func(r *CampaignCreateRequest) { d := int32(-1); r.Steps[0].DelaySeconds = &d }, RejectCampaignStepsInvalid},
		{"no group", func(r *CampaignCreateRequest) { r.GroupToken = "" }, RejectCampaignTargetInvalid},
		{"area target", func(r *CampaignCreateRequest) { r.TargetKind = strp("AREA") }, RejectCampaignTargetInvalid},
		{"lateness range", func(r *CampaignCreateRequest) { v := int32(0); r.MaxLatenessSeconds = &v }, RejectCampaignFieldInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := solarRequest("c-"+fmt.Sprint(time.Now().UnixNano()), "lamps")
			c.edit(req)
			_, err := api.CreateCampaign(ctx, req)
			var rej *EnqueueRejected
			if !errors.As(err, &rej) || rej.Code != c.code {
				t.Fatalf("err = %v, want rejection %s", err, c.code)
			}
		})
	}
	if _, err := api.CreateCampaign(ctx, solarRequest("dup", "lamps")); err != nil {
		t.Fatal(err)
	}
	_, err := api.CreateCampaign(ctx, solarRequest("dup", "lamps"))
	var rej *EnqueueRejected
	if !errors.As(err, &rej) || rej.Code != RejectTokenInUse {
		t.Fatalf("duplicate token err = %v", err)
	}
}

// The central behaviour: every fixture is commanded at ITS OWN sunset, from its own
// installed location, and not a moment before.
func TestSolarCampaignFiresAtEachFixturesOwnDusk(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	sites := map[string]Site{
		"lisbon": {38.72, -9.14}, "london": {51.51, -0.13}, "berlin": {52.52, 13.40}, "helsinki": {60.17, 24.94},
	}
	api.GroupTargetResolver = &pagingResolver{members: []string{"lisbon", "london", "berlin", "helsinki", "unlocated"}}
	api.SiteResolver = fakeSites{sites: sites}
	if _, err := api.CreateCampaign(ctx, solarRequest("dusk-dim", "lamps")); err != nil {
		t.Fatal(err)
	}

	tick(t, api, at("2026-06-21T12:00:00Z"), "p0")
	for d := range sites {
		if commandsFor(t, api, d) != 0 {
			t.Fatalf("%s was commanded at noon", d)
		}
	}
	// Walk the fixtures in the order the sun reaches them, as time itself does: a pass at a
	// later fixture's dusk would otherwise (correctly) mark an earlier one MISSED.
	type dusk struct {
		device string
		set    time.Time
	}
	var order []dusk
	for d, s := range sites {
		ev, err := solar.Compute(2026, time.June, 21, s.Latitude, s.Longitude)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, dusk{d, ev.Set})
	}
	sort.Slice(order, func(i, j int) bool { return order[i].set.Before(order[j].set) })
	for _, o := range order {
		d, ev := o.device, solar.Events{Set: o.set}
		before, after := ev.Set.Add(-time.Minute), ev.Set.Add(time.Minute)
		tick(t, api, before, "pb-"+d)
		if n := commandsFor(t, api, d); n != 0 {
			t.Fatalf("%s commanded a minute before its own sunset (%s)", d, ev.Set)
		}
		tick(t, api, after, "pa-"+d)
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands a minute after its own sunset, want 1", d, n)
		}
	}
	// Re-ticking late changes nothing: still one command each.
	tick(t, api, at("2026-06-21T23:00:00Z"), "late")
	for d := range sites {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands after a late re-tick, want 1", d, n)
		}
	}
	// The device with no installed location is recorded as skipped, not guessed at.
	c, _ := api.campaignByToken(ctx, "dusk-dim")
	var e CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND device_token = ?", c.ID, "unlocated").First(&e).Error; err != nil {
		t.Fatal(err)
	}
	if e.Status != string(ElementSkipped) || e.Reason.String != SkipNoSite {
		t.Fatalf("unlocated element = %s/%s, want SKIPPED/%s", e.Status, e.Reason.String, SkipNoSite)
	}
}

func TestPolarFixtureIsSkippedWithAReason(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"svalbard"}}
	api.SiteResolver = fakeSites{sites: map[string]Site{"svalbard": {78.2, 15.6}}}
	if _, err := api.CreateCampaign(ctx, solarRequest("polar", "lamps")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T12:00:00Z"), "p")
	c, _ := api.campaignByToken(ctx, "polar")
	var e CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).First(&e).Error; err != nil {
		t.Fatal(err)
	}
	if e.Reason.String != SkipPolarDay {
		t.Fatalf("reason = %q, want %s", e.Reason.String, SkipPolarDay)
	}
}

func TestSolarWithoutASiteSourceFailsLoudly(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, solarRequest("nosrc", "lamps")); err != nil {
		t.Fatal(err)
	}
	if err := api.TickCampaigns(context.Background(), at("2026-06-21T12:00:00Z"), "p"); err == nil {
		t.Fatal("a solar campaign with no location source planned without error")
	}
	c, _ := api.campaignByToken(ctx, "nosrc")
	if !c.LastError.Valid {
		t.Fatal("the failure was not recorded on the campaign")
	}
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a command was created without a location")
	}
}

func cronRequest(token, cron string) *CampaignCreateRequest {
	return &CampaignCreateRequest{
		Token:      token,
		Trigger:    fmt.Sprintf(`{"kind":"CRON","zone":"America/New_York","cron":%q}`, cron),
		Steps:      []CampaignStepInput{{Type: StepCommand, Name: strp("lights-on")}},
		GroupToken: "lamps",
	}
}

// Two schedulers ticking at once must command each device once. The pass runs in every
// replica with no leader, so the claim is what stands between a light and a double send.
func TestConcurrentPassesCommandEachDeviceOnce(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	members := make([]string, 60)
	for i := range members {
		members[i] = fmt.Sprintf("lamp-%02d", i)
	}
	api.GroupTargetResolver = &pagingResolver{members: members}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	// 20:00 EDT is 00:00Z.
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := api.TickCampaigns(context.Background(), at("2026-06-22T00:00:30Z"), fmt.Sprintf("replica-%d", i)); err != nil {
				t.Errorf("replica %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	for _, d := range members {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want exactly 1", d, n)
		}
	}
	// And a later pass re-sends nothing.
	tick(t, api, at("2026-06-22T00:05:00Z"), "after")
	for _, d := range members {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands after a later pass, want 1", d, n)
		}
	}
}

// A replica that dies between claiming and creating the batch must not strand the lights or
// double-send them: another replica finishes the batch under the same token.
func TestDeadReplicasClaimIsFinishedWithoutDuplicates(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b", "c"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	// A pass claims everything and dies before creating the batch.
	c, _ := api.campaignByToken(ctx, "nightly")
	dead := at("2026-06-22T00:00:10Z")
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("campaign_id = ? AND due_at <= ?", c.ID, dead).
		Updates(map[string]any{"status": string(ElementClaimed), "claimed_by": "dead", "claimed_at": dead}).Error; err != nil {
		t.Fatal(err)
	}
	// Within the stale bound the survivor leaves it alone...
	tick(t, api, dead.Add(30*time.Second), "survivor")
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a live claim was taken over before it went stale")
	}
	// ...and past it, finishes the work exactly once, even when asked twice.
	tick(t, api, dead.Add(campaignClaimStale+time.Second), "survivor-2")
	tick(t, api, dead.Add(campaignClaimStale+2*time.Second), "survivor-3")
	for _, d := range []string{"a", "b", "c"} {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want 1", d, n)
		}
	}
}

// Firing a dusk command at midnight is worse than not firing it.
func TestAnOutageDoesNotFireLateCommands(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T04:00:00Z"), "back-from-outage") // four hours after 00:00Z
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a four-hour-late command was sent")
	}
	c, _ := api.campaignByToken(ctx, "nightly")
	var e CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).First(&e).Error; err != nil {
		t.Fatal(err)
	}
	if e.Status != string(ElementSkipped) || e.Reason.String != SkipMissed {
		t.Fatalf("element = %s/%s, want SKIPPED/MISSED", e.Status, e.Reason.String)
	}
}

func TestDisabledCampaignPlansAndFiresNothing(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	off := false
	req := cronRequest("nightly", "0 20 * * *")
	req.Enabled = &off
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "p1")
	tick(t, api, at("2026-06-22T00:00:30Z"), "p2")
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a disabled campaign fired")
	}
	var cycles int64
	api.RDB.DB(ctx).Model(&CampaignCycle{}).Count(&cycles)
	if cycles != 0 {
		t.Fatalf("a disabled campaign planned %d cycles", cycles)
	}
}

func TestMultiStepDelaysAreOrdered(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	req := cronRequest("two-step", "0 20 * * *")
	d := int32(600)
	req.Steps = append(req.Steps, CampaignStepInput{Type: StepCommand, Name: strp("dim"), DelaySeconds: &d})
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "step1")
	if n := commandsFor(t, api, "a"); n != 1 {
		t.Fatalf("after the trigger: %d commands, want 1 (the delayed step is not due)", n)
	}
	tick(t, api, at("2026-06-22T00:10:30Z"), "step2")
	if n := commandsFor(t, api, "a"); n != 2 {
		t.Fatalf("after the delay: %d commands, want 2", n)
	}
}

func TestHolidaySkipsTheFiring(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	req := cronRequest("nightly", "0 20 * * *")
	req.Trigger = `{"kind":"CRON","zone":"America/New_York","cron":"0 20 * * *","holidays":["2026-06-21"]}`
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire") // 20:00 on the holiday, local
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("the campaign fired on its holiday")
	}
	tick(t, api, at("2026-06-23T00:00:30Z"), "next-day") // 20:00 the day after
	if commandsFor(t, api, "a") != 1 {
		t.Fatal("the campaign did not fire the day after its holiday")
	}
}

func TestRetryFromFailedResendsOnlyWhatFailed(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"ok", "dead", "slow", "unsure"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	c, _ := api.campaignByToken(ctx, "nightly")
	cycles, _ := api.CampaignCycles(ctx, "nightly", 5)
	if len(cycles) != 2 { // tonight's, and tomorrow's planned ahead
		t.Fatalf("cycles = %d", len(cycles))
	}
	key := cycles[len(cycles)-1].CycleKey // newest first: the last is tonight's
	_ = c

	set := func(device, status, outcome string) {
		upd := map[string]any{"status": status}
		if outcome != "" {
			upd["outcome"] = outcome
		}
		if err := api.RDB.DB(ctx).Model(&Command{}).Where("device_token = ?", device).Updates(upd).Error; err != nil {
			t.Fatal(err)
		}
	}
	set("ok", CommandSuccessful.String(), OutcomeSucceeded.String())
	set("dead", CommandFailed.String(), OutcomeRejected.String())
	set("unsure", CommandTimeout.String(), OutcomeUnknown.String())
	// "slow" stays in flight.

	sum, err := api.CampaignCycleSummary(ctx, "nightly", key)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dispatched != 4 || sum.Succeeded != 1 || sum.Failed != 1 || sum.Unknown != 1 || sum.InFlight != 1 {
		t.Fatalf("summary = %+v", *sum)
	}

	res, err := api.RetryCampaignFailed(ctx, "nightly", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Retried != 1 || res.Held != 2 || len(res.Batches) != 1 {
		t.Fatalf("retry = %+v, want 1 retried and 2 held", res)
	}
	if n := commandsFor(t, api, "dead"); n != 2 {
		t.Fatalf("dead has %d commands, want original + retry", n)
	}
	for _, d := range []string{"ok", "slow", "unsure"} {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s was re-sent (%d commands)", d, n)
		}
	}
	// Asking again offers nothing new: the retry's command is in flight.
	again, err := api.RetryCampaignFailed(ctx, "nightly", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Retried != 0 {
		t.Fatalf("second retry re-sent %d elements", again.Retried)
	}
	// The summary now counts the retry's command for "dead", not the original's.
	sum, _ = api.CampaignCycleSummary(ctx, "nightly", key)
	if sum.Failed != 0 || sum.InFlight != 2 || sum.Succeeded != 1 {
		t.Fatalf("summary after retry = %+v", *sum)
	}
	// With the unknown outcome acknowledged, that one is retried too.
	ack, err := api.RetryCampaignFailed(ctx, "nightly", key, true)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Retried != 1 || commandsFor(t, api, "unsure") != 2 {
		t.Fatalf("acknowledged retry = %+v", ack)
	}
}

func TestDeleteCancelsWhatWasPlanned(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	var cancelled, pending int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ? AND reason = ?", string(ElementSkipped), SkipCancelled).Count(&cancelled)
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ?", string(ElementPending)).Count(&pending)
	if cancelled == 0 || pending != 0 {
		t.Fatalf("after delete: %d cancelled, %d still pending; the plan must be cancelled, not left behind", cancelled, pending)
	}
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a deleted campaign fired")
	}
}

func TestOneTenantsFailureDoesNotStopAnother(t *testing.T) {
	api := newCampaignTestApi(t)
	acme, globex := tenantCtx(), core.WithTenant(context.Background(), "globex")
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	// acme's solar campaign cannot plan (no site source for the first pass); globex's cron can.
	if _, err := api.CreateCampaign(acme, solarRequest("acme-solar", "lamps")); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateCampaign(globex, cronRequest("globex-cron", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	_ = api.TickCampaigns(context.Background(), at("2026-06-21T22:00:00Z"), "p")
	_ = api.TickCampaigns(context.Background(), at("2026-06-22T00:00:30Z"), "q")
	var n int64
	api.RDB.DB(globex).Model(&Command{}).Count(&n)
	if n != 1 {
		t.Fatalf("globex has %d commands, want 1 despite acme's failure", n)
	}
}

func TestHistoryIsPrunedButWorkNeverIs(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	old := at("2026-06-22T00:00:30Z").Add(CampaignHistoryRetention + 48*time.Hour)

	// The rig plans on a simulated clock while rows are stamped with the wall clock; age the
	// cycles to the simulated planning time so pruning sees them as the service would.
	if err := api.RDB.DB(ctx).Model(&CampaignCycle{}).Where("1 = 1").
		Update("created_at", at("2026-06-21T22:00:00Z")).Error; err != nil {
		t.Fatal(err)
	}
	// The still-pending second night is old enough by the clock but is work, not history.
	if err := api.PruneCampaignHistory(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	var left []CampaignElement
	api.RDB.DB(ctx).Find(&left)
	statuses := map[string]int{}
	for _, e := range left {
		statuses[e.Status]++
	}
	if statuses[string(ElementDispatched)] != 0 || statuses[string(ElementPending)] != 2 {
		t.Fatalf("after pruning: %v, want the dispatched history gone and the 2 pending kept", statuses)
	}
	var cycles int64
	api.RDB.DB(ctx).Model(&CampaignCycle{}).Count(&cycles)
	if cycles != 1 {
		t.Fatalf("%d cycles remain, want only the one still holding pending work", cycles)
	}
}

// Two steps that fall due together are two batches, so two commands per device. A batch
// token that forgot the step would replay the first batch for the second and send one.
func TestStepsDueTogetherAreSeparateBatches(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b"}}
	req := cronRequest("two-now", "0 20 * * *")
	req.Steps = append(req.Steps, CampaignStepInput{Type: StepCommand, Name: strp("dim")})
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	for _, d := range []string{"a", "b"} {
		if n := commandsFor(t, api, d); n != 2 {
			t.Fatalf("%s has %d commands, want one per step (2)", d, n)
		}
	}
}

// Another replica claiming some of what this pass read must not be overwritten: the claim is
// conditional on the element still being PENDING.
func TestAClaimNeverTakesWhatAnotherReplicaAlreadyHas(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b", "c"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")

	// Between this pass reading the due elements and claiming them, another replica takes "a".
	campaignClaimHook = func() {
		campaignClaimHook = nil
		c, _ := api.campaignByToken(ctx, "nightly")
		err := api.RDB.DB(ctx).Model(&CampaignElement{}).
			Where("campaign_id = ? AND device_token = ? AND due_at <= ?", c.ID, "a", at("2026-06-22T00:00:30Z")).
			Updates(map[string]any{"status": string(ElementClaimed), "claimed_by": "other-replica",
				"claimed_at": at("2026-06-22T00:00:30Z")}).Error
		if err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignClaimHook = nil }()
	tick(t, api, at("2026-06-22T00:00:30Z"), "this-replica")

	c, _ := api.campaignByToken(ctx, "nightly")
	var a CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND device_token = ? AND due_at <= ?", c.ID, "a",
		at("2026-06-22T00:00:30Z")).First(&a).Error; err != nil {
		t.Fatal(err)
	}
	if a.ClaimedBy.String != "other-replica" {
		t.Fatalf("this pass took an element another replica had claimed (claimed_by=%q)", a.ClaimedBy.String)
	}
	if n := commandsFor(t, api, "a"); n != 0 {
		t.Fatalf("this pass commanded the device the other replica owns (%d commands)", n)
	}
	for _, d := range []string{"b", "c"} {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want 1", d, n)
		}
	}
}

// A tenant with one campaign on and one off ticks only the one that is on.
func TestOnlyTheEnabledCampaignOfATenantFires(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("on", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	off := false
	req := cronRequest("off", "0 20 * * *")
	req.Enabled = &off
	req.Steps[0].Name = strp("never")
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	var never int64
	api.RDB.DB(ctx).Model(&Command{}).Where("name = ?", "never").Count(&never)
	if never != 0 || commandsFor(t, api, "a") != 1 {
		t.Fatalf("the disabled campaign fired (%d) or the enabled one did not (%d)", never, commandsFor(t, api, "a"))
	}
}

// ---- Regressions for the review round ------------------------------------------------

// seedStaleClaims plans one night for the given devices and leaves every element of the
// firing claimed by a replica that died, with no batch token.
func seedStaleClaims(t *testing.T, api *Api, members []string) (claimedAt time.Time) {
	t.Helper()
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: members}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	c, _ := api.campaignByToken(ctx, "nightly")
	claimedAt = at("2026-06-22T00:00:10Z")
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).Where("campaign_id = ? AND due_at <= ?", c.ID, claimedAt).
		Updates(map[string]any{"status": string(ElementClaimed), "claimed_by": "dead", "claimed_at": claimedAt}).Error; err != nil {
		t.Fatal(err)
	}
	return claimedAt
}

// Recovery must use the tokens the database holds. Another replica re-tokening some of the
// claims between this pass loading them and tokening them must not make this pass create a
// batch for devices that replica is also sending.
func TestRecoveryUsesTheDatabasesTokensNotItsOwnCopy(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a", "b", "c", "d"})
	c, _ := api.campaignByToken(ctx, "nightly")

	campaignClaimedHook = func() {
		campaignClaimedHook = nil
		// The other replica gives "a" and "b" a token and creates their batch.
		if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
			Where("campaign_id = ? AND device_token IN ? AND due_at <= ?", c.ID, []string{"a", "b"}, claimedAt).
			Update("batch_token", "cmp-other-replica").Error; err != nil {
			t.Error(err)
		}
		devs := []string{"a", "b"}
		if _, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{Token: "cmp-other-replica",
			Name: "lights-on", DeviceTokens: &devs, AllowPartial: true}); err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignClaimedHook = nil }()
	tick(t, api, claimedAt.Add(campaignClaimStale+time.Second), "survivor")
	for _, d := range []string{"a", "b", "c", "d"} {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want exactly 1", d, n)
		}
	}
}

// A claim nobody finished until it was far too late is MISSED, not sent.
func TestARecoveredClaimPastItsLatenessIsMissedNotSent(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a"})
	tick(t, api, claimedAt.Add(6*time.Hour), "midnight-survivor")
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a claim six hours past its lateness bound was sent")
	}
	c, _ := api.campaignByToken(ctx, "nightly")
	var e CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND due_at <= ?", c.ID, claimedAt).First(&e).Error; err != nil {
		t.Fatal(err)
	}
	if e.Status != string(ElementSkipped) || e.Reason.String != SkipMissed {
		t.Fatalf("element = %s/%s, want SKIPPED/MISSED", e.Status, e.Reason.String)
	}
}

// Deleting a campaign while a pass holds its claims must stop the pass, not just the next one.
func TestDeleteFencesAPassAlreadyHoldingClaims(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a", "b"})
	campaignClaimedHook = func() {
		campaignClaimedHook = nil
		if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignClaimedHook = nil }()
	// The pass loaded the campaign before the delete; it must notice.
	if err := api.TickCampaigns(context.Background(), claimedAt.Add(campaignClaimStale+time.Second), "p"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "b"} {
		if commandsFor(t, api, d) != 0 {
			t.Fatalf("%s was commanded by a campaign deleted mid-pass", d)
		}
	}
}

// Recovery of more than one batch's worth of tokenless claims is several batches, none over
// the batch size limit.
func TestRecoveryChunksALargeTokenlessSet(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	n := campaignClaimPage + 700
	members := make([]string, n)
	for i := range members {
		members[i] = fmt.Sprintf("m-%05d", i)
	}
	claimedAt := seedStaleClaims(t, api, members)
	tick(t, api, claimedAt.Add(campaignClaimStale+time.Second), "survivor")
	var batches []string
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("batch_token IS NOT NULL AND status = ?", string(ElementDispatched)).
		Distinct().Pluck("batch_token", &batches)
	if len(batches) != 2 {
		t.Fatalf("%d batches, want 2 (a full page and the remainder)", len(batches))
	}
	var dispatched int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ?", string(ElementDispatched)).Count(&dispatched)
	if int(dispatched) != n {
		t.Fatalf("%d dispatched, want %d", dispatched, n)
	}
}

// A solar campaign created just after midnight UTC for a fixture far west still gets the
// sunset of the PREVIOUS date, which has not happened yet there.
func TestFirstSolarPlanIncludesThePreviousDatesStillComingEvents(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"honolulu"}}
	api.SiteResolver = fakeSites{sites: map[string]Site{"honolulu": {21.31, -157.86}}}
	req := solarRequest("hnl", "lamps")
	req.Trigger = `{"kind":"SOLAR","zone":"UTC","event":"SUNSET"}`
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-22T01:00:00Z"), "plan") // Honolulu's June 21st sunset is ~05:15Z on the 22nd
	tick(t, api, at("2026-06-22T05:20:00Z"), "dusk")
	if n := commandsFor(t, api, "honolulu"); n != 1 {
		t.Fatalf("honolulu has %d commands after its own sunset, want 1", n)
	}
}

// A trigger that fires like a stream is refused where it is defined.
func TestAStreamOfFiringsIsRefusedAtCreate(t *testing.T) {
	api := newCampaignTestApi(t)
	for i, cron := range []string{"* * * * *", "*/5 * * * *", "0,30 * * * *"} {
		_, err := api.CreateCampaign(tenantCtx(), cronRequest(fmt.Sprintf("fast-%d", i), cron))
		var rej *EnqueueRejected
		if cron == "0,30 * * * *" {
			if err != nil {
				t.Fatalf("%q fires 48 times a day and is the limit, got %v", cron, err)
			}
			continue
		}
		if !errors.As(err, &rej) || rej.Code != RejectCampaignTriggerInvalid {
			t.Fatalf("%q: err = %v, want CAMPAIGN_TRIGGER_INVALID", cron, err)
		}
	}
}

// Switching the last campaign off must not leave its pending work, or its history, behind.
func TestADisabledCampaignsPendingWorkStillGoesStaleAndItsHistoryIsPruned(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	if _, err := api.SetCampaignEnabled(ctx, "nightly", false); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-22T06:00:00Z"), "later")
	var missed int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ? AND reason = ?", string(ElementSkipped), SkipMissed).Count(&missed)
	if missed == 0 {
		t.Fatal("a switched-off campaign's overdue pending element was never expired")
	}
	if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	api.RDB.DB(ctx).Model(&CampaignCycle{}).Where("1 = 1").Update("created_at", at("2026-06-21T22:00:00Z"))
	if err := api.PruneCampaignHistory(context.Background(), at("2026-06-22T06:00:00Z").Add(CampaignHistoryRetention+48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left int64
	api.RDB.DB(ctx).Model(&CampaignCycle{}).Count(&left)
	if left != 0 {
		t.Fatalf("%d cycles of a deleted campaign survive past retention", left)
	}
	// And the deleted campaign itself is gone for good, so its tenant leaves the tick.
	var rows int64
	api.RDB.DB(ctx).Unscoped().Model(&Campaign{}).Count(&rows)
	if rows != 0 {
		t.Fatalf("%d deleted campaign rows survive past retention", rows)
	}
}

// A retry that created its batch and died before recording it must be adopted, not repeated.
func TestARetryThatDiedAfterCreatingItsBatchIsNotRepeated(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"dead"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	cycles, _ := api.CampaignCycles(ctx, "nightly", 5)
	key := cycles[len(cycles)-1].CycleKey
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("device_token = ?", "dead").
		Updates(map[string]any{"status": CommandFailed.String(), "outcome": OutcomeNotDelivered.String()}).Error; err != nil {
		t.Fatal(err)
	}
	var el CampaignElement
	api.RDB.DB(ctx).Where("device_token = ? AND status = ?", "dead", string(ElementDispatched)).First(&el)

	// The retry runs to the point of creating its batch, then the process dies.
	if _, err := api.RetryCommandBatch(ctx, &CommandBatchRetryRequest{Token: el.BatchToken.String + "-r1",
		Of: el.BatchToken.String, DeviceTokens: []string{"dead"}, AllowPartial: true}); err != nil {
		t.Fatal(err)
	}
	// It actuated and finished with an outcome nobody can vouch for.
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("batch_token = ?", el.BatchToken.String+"-r1").
		Updates(map[string]any{"status": CommandTimeout.String(), "outcome": OutcomeUnknown.String()}).Error; err != nil {
		t.Fatal(err)
	}
	// Run again without acknowledging unknowns: nothing may be sent.
	res, err := api.RetryCampaignFailed(ctx, "nightly", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Retried != 0 || commandsFor(t, api, "dead") != 2 {
		t.Fatalf("retry = %+v, %d commands: the dead run's retry was repeated", res, commandsFor(t, api, "dead"))
	}
}

// Switching a campaign off while a pass holds its claims stops the pass; the claims are left
// to go MISSED rather than being sent by a campaign that is no longer on.
func TestDisableFencesAPassAlreadyHoldingClaims(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a"})
	campaignClaimedHook = func() {
		campaignClaimedHook = nil
		if _, err := api.SetCampaignEnabled(ctx, "nightly", false); err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignClaimedHook = nil }()
	tick(t, api, claimedAt.Add(campaignClaimStale+time.Second), "p")
	if commandsFor(t, api, "a") != 0 {
		t.Fatal("a campaign switched off mid-pass still sent")
	}
}

// The fire-rate bound looks at every day of a year, not a sample week: a cron restricted to a
// month, a day of the month or a leap day the sample misses would otherwise be accepted and then
// fire every minute on the days it names.
func TestAStreamRestrictedToDaysTheSampleMissesIsStillRefused(t *testing.T) {
	api := newCampaignTestApi(t)
	for i, cron := range []string{"* * * 2 *", "* * 29 2 *", "* * 31 * *", "*/10 * * 12 *"} {
		_, err := api.CreateCampaign(tenantCtx(), cronRequest(fmt.Sprintf("hidden-%d", i), cron))
		var rej *EnqueueRejected
		if !errors.As(err, &rej) || rej.Code != RejectCampaignTriggerInvalid {
			t.Fatalf("%q: err = %v, want CAMPAIGN_TRIGGER_INVALID", cron, err)
		}
	}
	// The counterweight: a sparse schedule restricted the same way is accepted.
	if _, err := api.CreateCampaign(tenantCtx(), cronRequest("feb-dusk", "0 18 * 2 *")); err != nil {
		t.Fatalf("a once-a-day February schedule was refused: %v", err)
	}
}

// Deleting a campaign cancels the claims a dead replica left as well as what is pending: no tick
// loads a deleted campaign again, so a claim left CLAIMED would stay so for good.
func TestDeleteCancelsAbandonedClaims(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	seedStaleClaims(t, api, []string{"a", "b"})
	if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	var claimed int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ?", string(ElementClaimed)).Count(&claimed)
	if claimed != 0 {
		t.Fatalf("%d elements are still CLAIMED after their campaign was deleted", claimed)
	}
}

// A switched-off campaign still expires a dead replica's tokenless claims once they are past
// their lateness bound: no batch can exist for them, and nothing else would ever settle them.
func TestADisabledCampaignExpiresAbandonedTokenlessClaims(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a"})
	if _, err := api.SetCampaignEnabled(ctx, "nightly", false); err != nil {
		t.Fatal(err)
	}
	tick(t, api, claimedAt.Add(6*time.Hour), "later")
	var claimed int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("status = ?", string(ElementClaimed)).Count(&claimed)
	if claimed != 0 {
		t.Fatalf("%d abandoned claims of a switched-off campaign were never expired", claimed)
	}
}

// A replica that created a claim's batch and died before settling it: recovery long after the
// lateness bound must NOT call those elements MISSED, because their commands were sent on time.
// The replay settles them from the batch, and nothing is sent twice.
func TestALateRecoveredClaimWhoseBatchExistsIsSettledNotMissed(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, []string{"a", "b"})
	c, _ := api.campaignByToken(ctx, "nightly")
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where("campaign_id = ? AND due_at <= ?", c.ID, claimedAt).Update("batch_token", "cmp-dead-replica").Error; err != nil {
		t.Fatal(err)
	}
	devs := []string{"a", "b"}
	if _, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{Token: "cmp-dead-replica",
		Name: "lights-on", DeviceTokens: &devs, AllowPartial: true}); err != nil {
		t.Fatal(err)
	}
	tick(t, api, claimedAt.Add(6*time.Hour), "survivor")
	for _, d := range devs {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want exactly the dead replica's 1", d, n)
		}
		var e CampaignElement
		if err := api.RDB.DB(ctx).Where("campaign_id = ? AND device_token = ? AND due_at <= ?", c.ID, d, claimedAt).
			First(&e).Error; err != nil {
			t.Fatal(err)
		}
		if e.Status != string(ElementDispatched) {
			t.Fatalf("%s: element = %s/%s, want DISPATCHED (its command was sent on time)", d, e.Status, e.Reason.String)
		}
	}
}
