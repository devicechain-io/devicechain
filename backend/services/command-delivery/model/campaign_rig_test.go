// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// The campaign rig, end to end: 50,000 simulated fixtures, each at its own installed
// location, a SOLAR sunset campaign, and a scheduler ticked through the evening exactly as
// the service would tick it. The judgement is per fixture: it must be commanded once, and
// not before its own dusk nor later than a tick after it.
//
// The rig carries its negative control. The same run is repeated with the location source
// planted with a defect (every fixture reported at the mirror-image longitude, the shape a
// swapped-sign import leaves behind), and the same judgement must then condemn it.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/solar"
)

const (
	rigFleetSize = 50000
	rigTick      = time.Minute
)

// requireRig gates the end-to-end rig. It takes minutes (it plans and ticks 50,000 fixtures
// through an evening on SQLite), so it is a manual validation like the repository's other
// rigs rather than part of every `go test`; the arithmetic it relies on has its own fast rig
// in core/schedule that does run everywhere. Run it with DC_SCHEDULE_RIG=1.
func requireRig(t *testing.T) {
	t.Helper()
	if os.Getenv("DC_SCHEDULE_RIG") == "" {
		t.Skip("set DC_SCHEDULE_RIG=1 to run the 50k-fixture campaign rig")
	}
}

type rigResult struct {
	commanded, early, late, missing, duplicated int
}

// runRig plans one evening for the fleet and ticks through it. mirror plants the defect.
func runRig(t *testing.T, mirror bool) rigResult {
	t.Helper()
	api := newCampaignTestApi(t)
	ctx := tenantCtx()
	rng := rand.New(rand.NewSource(2045))

	members := make([]string, rigFleetSize)
	truth := make(map[string]Site, rigFleetSize)
	reported := make(map[string]Site, rigFleetSize)
	for i := range members {
		d := fmt.Sprintf("light-%05d", i)
		members[i] = d
		s := Site{Latitude: 38 + rng.Float64()*24, Longitude: -10 + rng.Float64()*40}
		truth[d] = s
		reported[d] = s
		if mirror {
			reported[d] = Site{Latitude: s.Latitude, Longitude: -s.Longitude}
		}
	}
	api.GroupTargetResolver = &pagingResolver{members: members}
	api.SiteResolver = fakeSites{sites: reported}
	if _, err := api.CreateCampaign(ctx, solarRequest("dusk-dim", "streetlights")); err != nil {
		t.Fatal(err)
	}

	// The window of the evening: from before the earliest true dusk to after the latest.
	var first, last time.Time
	dusk := make(map[string]time.Time, rigFleetSize)
	for d, s := range truth {
		ev, err := solar.Compute(2026, time.June, 21, s.Latitude, s.Longitude)
		if err != nil || ev.Status != solar.Normal {
			t.Fatalf("fixture %s: %+v %v", d, ev, err)
		}
		dusk[d] = ev.Set
		if first.IsZero() || ev.Set.Before(first) {
			first = ev.Set
		}
		if ev.Set.After(last) {
			last = ev.Set
		}
	}
	start := first.Add(-30 * time.Minute).Truncate(time.Minute)
	if err := api.TickCampaigns(context.Background(), start, "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	}
	pass := 0
	for now := start; now.Before(last.Add(30 * time.Minute)); now = now.Add(rigTick) {
		pass++
		if err := api.TickCampaigns(context.Background(), now, fmt.Sprintf("p%d", pass)); err != nil {
			t.Fatalf("tick %s: %v", now, err)
		}
		// The dispatcher: whatever the scheduler queued has gone out, so the tenant's
		// undelivered ceiling is judged on what is genuinely waiting, not on the whole evening.
		if err := api.RDB.DB(ctx).Model(&Command{}).Where("status = ?", CommandQueued.String()).
			Update("status", CommandSent.String()).Error; err != nil {
			t.Fatal(err)
		}
	}

	c, _ := api.campaignByToken(ctx, "dusk-dim")
	var elems []CampaignElement
	if err := api.RDB.DB(ctx).Where("campaign_id = ?", c.ID).
		Where("due_at > ? AND due_at < ?", first.Add(-time.Hour), last.Add(time.Hour)).
		Find(&elems).Error; err != nil {
		t.Fatal(err)
	}
	var res rigResult
	seen := map[string]int{}
	for _, e := range elems {
		if e.Status != string(ElementDispatched) {
			continue
		}
		seen[e.DeviceToken]++
		res.commanded++
		at := e.ClaimedAt.Time
		want := dusk[e.DeviceToken]
		switch {
		case at.Before(want):
			res.early++
		case at.After(want.Add(2 * rigTick)):
			res.late++
		}
	}
	for _, d := range members {
		switch n := seen[d]; {
		case n == 0:
			res.missing++
		case n > 1:
			res.duplicated++
		}
	}
	return res
}

func TestRigFiftyThousandFixturesDimAtTheirOwnDusk(t *testing.T) {
	requireRig(t)
	r := runRig(t, false)
	t.Logf("rig: %+v", r)
	if r.commanded != rigFleetSize || r.early != 0 || r.late != 0 || r.missing != 0 || r.duplicated != 0 {
		t.Fatalf("fleet not dimmed at its own dusk: %+v", r)
	}
}

func TestRigCondemnsAPlantedWrongLocationSource(t *testing.T) {
	requireRig(t)
	r := runRig(t, true)
	t.Logf("planted: %+v", r)
	// With the longitudes mirrored most of the fleet is commanded far from its true dusk:
	// early, late (so MISSED and never commanded), or both. The judgement must see it.
	if bad := r.early + r.late + r.missing; bad < rigFleetSize/2 {
		t.Fatalf("the planted wrong-location source was condemned on only %d of %d fixtures: %+v", bad, rigFleetSize, r)
	}
}
