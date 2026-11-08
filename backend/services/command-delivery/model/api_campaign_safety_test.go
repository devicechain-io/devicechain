// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/solar"
)

// Regressions for the actuation-safety review of the campaign scheduler. Each names the
// physical failure it stands against: a light commanded for an event that fell due before the
// campaign existed, a burst of stale commands after an outage, a device commanded after it left
// the group, a tracking record that says a command was not sent when it was.

func londonSunset(t *testing.T, y int, m time.Month, d int) time.Time {
	t.Helper()
	ev, err := solar.Compute(y, m, d, 51.51, -0.13)
	if err != nil {
		t.Fatal(err)
	}
	return ev.Set
}

func setClock(api *Api, now time.Time) { api.campaignNow = func() time.Time { return now } }

func elementOf(t *testing.T, api *Api, campaign, device string) CampaignElement {
	t.Helper()
	ctx := tenantCtx()
	var c Campaign
	if err := api.RDB.DB(ctx).Unscoped().Where("token = ?", campaign).Order("id DESC").First(&c).Error; err != nil {
		t.Fatal(err)
	}
	var e CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ? AND device_token = ?", c.ID, device).
		Order("due_at ASC").First(&e).Error; err != nil {
		t.Fatal(err)
	}
	return e
}

func countElements(t *testing.T, api *Api, status, reason string) int {
	t.Helper()
	q := api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Where("status = ?", status)
	if reason != "" {
		q = q.Where("reason = ?", reason)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

// B1. Deleting a solar campaign and creating it again inside the lateness window must not
// command today's dusk a second time: the event fell due before the new campaign existed.
func TestRecreatingASolarCampaignDoesNotRefireTodaysDusk(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"london"}}
	api.SiteResolver = fakeSites{sites: map[string]Site{"london": {51.51, -0.13}}}
	setClock(api, at("2026-06-21T12:00:00Z"))
	if _, err := api.CreateCampaign(ctx, solarRequest("dusk", "lamps")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T12:00:00Z"), "plan")
	set := londonSunset(t, 2026, time.June, 21)
	tick(t, api, set.Add(time.Minute), "dusk")
	if n := commandsFor(t, api, "london"); n != 1 {
		t.Fatalf("%d commands at dusk, want 1", n)
	}
	setClock(api, set.Add(2*time.Minute))
	if err := api.DeleteCampaign(ctx, "dusk"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateCampaign(ctx, solarRequest("dusk", "lamps")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, set.Add(3*time.Minute), "recreated")
	tick(t, api, set.Add(5*time.Minute), "recreated-2")
	if n := commandsFor(t, api, "london"); n != 1 {
		t.Fatalf("%d commands after delete-and-recreate inside the lateness window, want 1", n)
	}
	e := elementOf(t, api, "dusk", "london")
	_ = e
	var predates int64
	api.RDB.DB(ctx).Model(&CampaignElement{}).Where("reason = ?", "PREDATES_CAMPAIGN").Count(&predates)
	if predates == 0 {
		t.Fatal("the recreated campaign's past dusk was not recorded as PREDATES_CAMPAIGN")
	}
}

// B1. A fresh solar campaign with a day of lateness must not fire yesterday's sunset.
func TestAFreshSolarCampaignDoesNotFireYesterdaysDusk(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"london"}}
	api.SiteResolver = fakeSites{sites: map[string]Site{"london": {51.51, -0.13}}}
	setClock(api, at("2026-06-22T12:00:00Z"))
	req := solarRequest("dusk", "lamps")
	day := int32(86400)
	req.MaxLatenessSeconds = &day
	if _, err := api.CreateCampaign(ctx, req); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-22T12:00:00Z"), "plan")
	if n := commandsFor(t, api, "london"); n != 0 {
		t.Fatalf("a campaign created at noon fired %d commands for the previous evening", n)
	}
	// Tonight's still fires.
	tick(t, api, londonSunset(t, 2026, time.June, 22).Add(time.Minute), "tonight")
	if n := commandsFor(t, api, "london"); n != 1 {
		t.Fatalf("%d commands at tonight's dusk, want 1", n)
	}
}

// M1. A claim is stamped when it is taken, so a long pass does not look stale to other replicas.
func TestAClaimIsStampedWhenTakenNotWhenThePassBegan(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	passStart := at("2026-06-22T00:00:30Z")
	taken := passStart.Add(3 * time.Minute)
	setClock(api, taken)
	if err := api.TickCampaigns(context.Background(), passStart, "slow"); err != nil {
		t.Fatal(err)
	}
	e := elementOf(t, api, "nightly", "a")
	if !e.ClaimedAt.Valid || !e.ClaimedAt.Time.Equal(taken) {
		t.Fatalf("claimed_at = %v, want the instant of the claim %v", e.ClaimedAt.Time, taken)
	}
}

// M1. A campaign deleted after a pass fenced and tokened its claims, but before the batch was
// created, sends nothing, and its elements say so.
func TestDeleteJustBeforeTheBatchSendsNothing(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a", "b"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	campaignDispatchHook = func() {
		campaignDispatchHook = nil
		if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignDispatchHook = nil }()
	_ = api.TickCampaigns(context.Background(), at("2026-06-22T00:00:30Z"), "p")
	tick(t, api, at("2026-06-22T00:01:00Z"), "q")
	for _, d := range []string{"a", "b"} {
		if n := commandsFor(t, api, d); n != 0 {
			t.Fatalf("%s was commanded by a campaign deleted before its batch", d)
		}
		if e := elementOf(t, api, "nightly", d); e.Status != string(ElementSkipped) || e.Reason.String != SkipCancelled {
			t.Fatalf("%s: element = %s/%s, want SKIPPED/CANCELLED", d, e.Status, e.Reason.String)
		}
	}
}

// seedTokenedClaims leaves a firing claimed by a dead replica that had already given it a token,
// and, when sent, had created the batch too.
func seedTokenedClaims(t *testing.T, api *Api, members []string, sent bool) time.Time {
	t.Helper()
	ctx := tenantCtx()
	claimedAt := seedStaleClaims(t, api, members)
	c, _ := api.campaignByToken(ctx, "nightly")
	if err := api.RDB.DB(ctx).Model(&CampaignElement{}).
		Where("campaign_id = ? AND due_at <= ?", c.ID, claimedAt).Update("batch_token", "cmp-dead-replica").Error; err != nil {
		t.Fatal(err)
	}
	if sent {
		devs := append([]string(nil), members...)
		if _, err := api.CreateCommandBatch(ctx, &CommandBatchCreateRequest{Token: "cmp-dead-replica",
			Name: "lights-on", DeviceTokens: &devs, AllowPartial: true}); err != nil {
			t.Fatal(err)
		}
	}
	return claimedAt
}

// M1. Deleting a campaign whose commands were already sent must not record them as CANCELLED.
func TestDeleteAfterTheBatchWasSentSettlesFromTheBatch(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedTokenedClaims(t, api, []string{"a", "b"}, true)
	if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	tick(t, api, claimedAt.Add(campaignClaimStale+time.Second), "settle")
	for _, d := range []string{"a", "b"} {
		if n := commandsFor(t, api, d); n != 1 {
			t.Fatalf("%s has %d commands, want the dead replica's 1", d, n)
		}
		if e := elementOf(t, api, "nightly", d); e.Status != string(ElementDispatched) {
			t.Fatalf("%s: element = %s/%s, want DISPATCHED (its command was sent)", d, e.Status, e.Reason.String)
		}
	}
}

// M1. A deleted campaign's tokened claim whose batch never came to be is CANCELLED, not stranded.
func TestDeleteOfATokenedClaimWithNoBatchCancelsIt(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	claimedAt := seedTokenedClaims(t, api, []string{"a"}, false)
	if err := api.DeleteCampaign(ctx, "nightly"); err != nil {
		t.Fatal(err)
	}
	tick(t, api, claimedAt.Add(campaignClaimStale+time.Second), "settle")
	if n := commandsFor(t, api, "a"); n != 0 {
		t.Fatalf("a deleted campaign's unsent claim was sent (%d)", n)
	}
	if e := elementOf(t, api, "nightly", "a"); e.Status != string(ElementSkipped) || e.Reason.String != SkipCancelled {
		t.Fatalf("element = %s/%s, want SKIPPED/CANCELLED", e.Status, e.Reason.String)
	}
}

// M1. A switched-off campaign still settles tokened claims: from the batch when it exists, and as
// MISSED past the lateness bound when it does not.
func TestADisabledCampaignSettlesTokenedClaims(t *testing.T) {
	for _, sent := range []bool{true, false} {
		t.Run(fmt.Sprintf("sent=%v", sent), func(t *testing.T) {
			api := newCampaignTestApi(t)
			ctx := tenantCtx()
			claimedAt := seedTokenedClaims(t, api, []string{"a"}, sent)
			if _, err := api.SetCampaignEnabled(ctx, "nightly", false); err != nil {
				t.Fatal(err)
			}
			tick(t, api, claimedAt.Add(6*time.Hour), "later")
			e := elementOf(t, api, "nightly", "a")
			switch {
			case sent && e.Status != string(ElementDispatched):
				t.Fatalf("element = %s/%s, want DISPATCHED from its batch", e.Status, e.Reason.String)
			case !sent && (e.Status != string(ElementSkipped) || e.Reason.String != SkipMissed):
				t.Fatalf("element = %s/%s, want SKIPPED/MISSED", e.Status, e.Reason.String)
			}
			want := 0
			if sent {
				want = 1
			}
			if n := commandsFor(t, api, "a"); n != want {
				t.Fatalf("%d commands, want %d", n, want)
			}
		})
	}
}

func hourlyRequest(token string) *CampaignCreateRequest {
	req := cronRequest(token, "0 * * * *")
	day := int32(86400)
	req.MaxLatenessSeconds = &day
	return req
}

// M2. After an outage, the many firings that fell due collapse to the newest: one command per
// device per step, not one per missed hour.
func TestAnOutageDoesNotReleaseABurst(t *testing.T) {
	api := newCampaignTestApi(t)
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(tenantCtx(), hourlyRequest("hourly")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T21:59:00Z"), "plan")
	tick(t, api, at("2026-06-22T07:59:30Z"), "back") // 22:00Z .. 07:00Z: ten firings due
	if n := commandsFor(t, api, "a"); n != 1 {
		t.Fatalf("%d commands after a ten-hour outage, want 1", n)
	}
	if n := countElements(t, api, string(ElementSkipped), "SUPERSEDED"); n != 9 {
		t.Fatalf("%d elements SUPERSEDED, want 9", n)
	}
}

// M2. Re-enabling a campaign does not send what fell due while it was off.
func TestReEnablingDoesNotReleaseWhatFellDueWhileOff(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, hourlyRequest("hourly")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T21:59:00Z"), "plan")
	tick(t, api, at("2026-06-21T22:00:30Z"), "fire")
	setClock(api, at("2026-06-21T22:01:00Z"))
	if _, err := api.SetCampaignEnabled(ctx, "hourly", false); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-22T03:00:30Z"), "while-off")
	setClock(api, at("2026-06-22T08:10:00Z"))
	if _, err := api.SetCampaignEnabled(ctx, "hourly", true); err != nil {
		t.Fatal(err)
	}
	before := commandsFor(t, api, "a")
	tick(t, api, at("2026-06-22T08:10:30Z"), "re-enabled")
	tick(t, api, at("2026-06-22T09:00:30Z"), "next")
	if n := commandsFor(t, api, "a") - before; n != 1 {
		t.Fatalf("%d commands from re-enable through the next firing, want 1", n)
	}
}

// M3. A device removed from the group after its cycle was planned is not commanded.
func TestADeviceRemovedAfterPlanningIsNotCommanded(t *testing.T) {
	api := newCampaignTestApi(t)
	resolver := &pagingResolver{members: []string{"a", "b"}}
	api.GroupTargetResolver = resolver
	if _, err := api.CreateCampaign(tenantCtx(), cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	resolver.members = []string{"a"}
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	if n := commandsFor(t, api, "a"); n != 1 {
		t.Fatalf("a has %d commands, want 1", n)
	}
	if n := commandsFor(t, api, "b"); n != 0 {
		t.Fatalf("b left the group and still got %d commands", n)
	}
	if e := elementOf(t, api, "nightly", "b"); e.Status != string(ElementSkipped) {
		t.Fatalf("b: element = %s/%s, want SKIPPED/NOT_IN_GROUP", e.Status, e.Reason.String)
	}
	var notIn int64
	api.RDB.DB(tenantCtx()).Model(&CampaignElement{}).Where("device_token = ? AND reason = ?", "b", "NOT_IN_GROUP").Count(&notIn)
	if notIn == 0 {
		t.Fatal("b's element does not say NOT_IN_GROUP")
	}
}

// fireIntoACeiling fires one cycle at a fleet larger than the tenant's room for commands, so
// most devices are refused for the same code, beyond the batch record's 100-per-code sample.
func fireIntoACeiling(t *testing.T, api *Api, n int) (key string) {
	t.Helper()
	ctx := tenantCtx()
	members := make([]string, n)
	for i := range members {
		members[i] = fmt.Sprintf("m-%03d", i)
	}
	api.GroupTargetResolver = &pagingResolver{members: members}
	api.DefaultHeldCommandCeiling = 5
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	tick(t, api, at("2026-06-22T00:00:30Z"), "fire")
	cycles, _ := api.CampaignCycles(ctx, "nightly", 5)
	return cycles[len(cycles)-1].CycleKey
}

// M4(a). Every refused element records its exact refusal code, not just the sampled hundred.
func TestEveryRefusedElementRecordsItsCode(t *testing.T) {
	api := newCampaignTestApi(t)
	fireIntoACeiling(t, api, 130)
	refused := countElements(t, api, string(ElementRefused), "")
	if refused <= 100 {
		t.Fatalf("only %d refused; the fixture must exceed the sample", refused)
	}
	if exact := countElements(t, api, string(ElementRefused), string(RejectHeldCeilingExceeded)); exact != refused {
		t.Fatalf("%d of %d refused elements carry HELD_CEILING_EXCEEDED; the rest lost their code", exact, refused)
	}
}

// M4(b). A retry-from-failed re-offers every refused element, not only the sampled hundred.
func TestRetryReachesRefusalsBeyondTheSample(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	key := fireIntoACeiling(t, api, 130)
	refused := countElements(t, api, string(ElementRefused), "")
	if err := api.RDB.DB(ctx).Model(&Command{}).Where("1 = 1").
		Updates(map[string]any{"status": CommandSuccessful.String(), "outcome": OutcomeSucceeded.String()}).Error; err != nil {
		t.Fatal(err)
	}
	api.DefaultHeldCommandCeiling = 10000
	res, err := api.RetryCampaignFailed(ctx, "nightly", key, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Retried != refused {
		t.Fatalf("retry re-sent %d of %d refused elements", res.Retried, refused)
	}
}

// M5. A pass inside the planned window does not plan again.
func TestPlanningIsNotRepeatedEveryPass(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	first := at("2026-06-21T22:00:00Z")
	tick(t, api, first, "plan")
	tick(t, api, first.Add(30*time.Minute), "soon")
	c, _ := api.campaignByToken(ctx, "nightly")
	if want := first.Add(CampaignPlanAhead); !c.PlannedThrough.Time.Equal(want) {
		t.Fatalf("planned_through = %v, want it left at %v until the window runs low", c.PlannedThrough.Time, want)
	}
}

// L1. The fires-per-day bound counts the trigger zone's local days: a dense hour that straddles
// UTC midnight is still one local day.
func TestTheFireBoundCountsLocalDays(t *testing.T) {
	api := newCampaignTestApi(t)
	for i, trig := range []string{
		`{"kind":"CRON","zone":"Europe/Berlin","cron":"0-39 0,12 1 * *"}`,
		`{"kind":"CRON","zone":"Asia/Kolkata","cron":"* 5 1 * *"}`,
	} {
		req := cronRequest(fmt.Sprintf("local-%d", i), "0 20 * * *")
		req.Trigger = trig
		_, err := api.CreateCampaign(tenantCtx(), req)
		var rej *EnqueueRejected
		if !errors.As(err, &rej) || rej.Code != RejectCampaignTriggerInvalid {
			t.Fatalf("%s: err = %v, want CAMPAIGN_TRIGGER_INVALID", trig, err)
		}
	}
}

// API. A status filter outside the vocabulary is refused, not answered with an empty page.
func TestAnUnknownElementStatusIsRefused(t *testing.T) {
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	if _, err := api.CreateCampaign(ctx, cronRequest("nightly", "0 20 * * *")); err != nil {
		t.Fatal(err)
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	cycles, _ := api.CampaignCycles(ctx, "nightly", 5)
	if _, err := api.CampaignElements(ctx, "nightly", cycles[0].CycleKey, "DISPATCHD", 0, 10); err == nil {
		t.Fatal("a misspelt status filter was answered")
	}
	if _, err := api.CampaignElements(ctx, "nightly", cycles[0].CycleKey, "PENDING", 0, 10); err != nil {
		t.Fatalf("a valid status filter was refused: %v", err)
	}
}

// API. Over-long names and group tokens are refused as rejections, not stored or failed raw.
func TestCampaignFieldLengthsAreChecked(t *testing.T) {
	api := newCampaignTestApi(t)
	long := strings.Repeat("x", 129)
	req := cronRequest("named", "0 20 * * *")
	req.Name = &long
	var rej *EnqueueRejected
	if _, err := api.CreateCampaign(tenantCtx(), req); !errors.As(err, &rej) || rej.Code != RejectCampaignFieldInvalid {
		t.Fatalf("a 129-character name: err = %v, want CAMPAIGN_FIELD_INVALID", err)
	}
	req = cronRequest("grouped", "0 20 * * *")
	req.GroupToken = long
	if _, err := api.CreateCampaign(tenantCtx(), req); !errors.As(err, &rej) || rej.Code != RejectCampaignTargetInvalid {
		t.Fatalf("a 129-character group token: err = %v, want CAMPAIGN_TARGET_INVALID", err)
	}
}

// API. Two creates of one token racing past the probe: the loser is told TOKEN_IN_USE.
func TestACreateRaceIsTokenInUse(t *testing.T) {
	api := newCampaignTestApi(t)
	campaignCreateHook = func() {
		campaignCreateHook = nil
		if _, err := api.CreateCampaign(tenantCtx(), cronRequest("raced", "0 20 * * *")); err != nil {
			t.Error(err)
		}
	}
	defer func() { campaignCreateHook = nil }()
	_, err := api.CreateCampaign(tenantCtx(), cronRequest("raced", "0 20 * * *"))
	var rej *EnqueueRejected
	if !errors.As(err, &rej) || rej.Code != RejectTokenInUse {
		t.Fatalf("err = %v, want TOKEN_IN_USE", err)
	}
}

// M5. A pass bounds the time it spends on one tenant: past the budget it stops after the
// campaign in hand, and the next pass carries on with what is left.
func TestAPassStopsAtTheTenantBudgetAndTheNextCarriesOn(t *testing.T) {
	api := newCampaignTestApi(t)
	api.GroupTargetResolver = &pagingResolver{members: []string{"a"}}
	for _, token := range []string{"first", "second"} {
		if _, err := api.CreateCampaign(tenantCtx(), cronRequest(token, "0 20 * * *")); err != nil {
			t.Fatal(err)
		}
	}
	tick(t, api, at("2026-06-21T22:00:00Z"), "plan")
	saved := campaignTenantBudget
	campaignTenantBudget = -time.Second // already spent
	defer func() { campaignTenantBudget = saved }()
	tick(t, api, at("2026-06-22T00:00:30Z"), "short")
	if n := commandsFor(t, api, "a"); n != 1 {
		t.Fatalf("%d commands from a pass with no budget left, want the one campaign in hand", n)
	}
	tick(t, api, at("2026-06-22T00:00:45Z"), "next")
	if n := commandsFor(t, api, "a"); n != 2 {
		t.Fatalf("%d commands after the next pass, want both campaigns'", n)
	}
}
