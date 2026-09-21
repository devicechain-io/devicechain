// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// The two paged reads together return the distinct entity references a tenant's anchors
// hold: each (anchor_type, anchor_token) target from one, each source device from the
// other, each deduped by the DISTINCT in its own query.
//
// They are asserted TOGETHER because neither is the answer on its own, and a sweep that
// called only one of them would still look like it was working — it would reconcile half
// the refs and report a clean pass over the other half.
func TestTheTwoAnchorReadsTogetherCoverEveryDistinctRef(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)
	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), []*EventAnchor{
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"},
		{EventId: anchorEventId("device-4", occurred), DeviceToken: "device-4", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "area", AnchorToken: "area-9"},
		{EventId: anchorEventId("device-7", occurred), DeviceToken: "device-7", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-3"}, // dup target
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	targets, err := api.DistinctAnchorTargetsAfter(ctx, "", "")
	assert.NoError(t, err)
	devices, err := api.DistinctAnchorDeviceTokensAfter(ctx, "")
	assert.NoError(t, err)

	set := make(map[string]bool)
	for _, r := range append(append([]AnchorRef{}, targets...), devices...) {
		set[r.Type+"|"+r.Token] = true
	}
	assert.True(t, set["customer|cust-3"], "customer 3 target")
	assert.True(t, set["area|area-9"], "area 9 target")
	assert.True(t, set["device|device-4"], "device 4 source")
	assert.True(t, set["device|device-7"], "device 7 source")
	assert.Len(t, set, 4, "customer 3 appears once despite two anchors")
}

// 🔴 THE PAGING TEST, AND IT NEEDS MORE ROWS THAN A PAGE OR IT ASSERTS NOTHING. Every
// other test in this file fits in one page, which means every one of them passes against
// a read that ignores its cursor entirely and returns the first page forever. This is
// the only test here that can tell the difference, so it seeds past the boundary on
// purpose and walks the cursor to exhaustion.
//
// What it checks is the pair of failures a keyset gets wrong, and they are opposites: a
// cursor that does not advance repeats rows (and never terminates), one that advances too
// far skips them. Collecting the union and comparing it to the seeded set catches both,
// and counting total rows returned catches the repeat even when the union looks right.
func TestAnchorTargetsPageThroughEveryRefExactlyOnce(t *testing.T) {
	api := newPersistenceTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)

	const seeded = anchorRefPageSize + 7
	anchors := make([]*EventAnchor, 0, seeded)
	want := make(map[string]bool, seeded)
	for i := 0; i < seeded; i++ {
		// Zero-padded so lexical order (which is what the keyset compares) and numeric
		// order agree; otherwise "cust-10" sorts before "cust-2" and a reader of this
		// test would have to work out whether that mattered.
		token := fmt.Sprintf("cust-%04d", i)
		anchors = append(anchors, &EventAnchor{
			EventId: anchorEventId(token, occurred), DeviceToken: "device-1",
			EventType: esmodel.Measurement, OccurredTime: occurred,
			AnchorType: "customer", AnchorToken: token,
		})
		want["customer|"+token] = true
	}
	if err := api.CreateEventAnchors(ctx, api.RDB.DB(ctx), anchors); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := make(map[string]bool, seeded)
	total, pages := 0, 0
	var lastType, lastToken string
	for {
		page, err := api.DistinctAnchorTargetsAfter(ctx, lastType, lastToken)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if len(page) == 0 {
			break
		}
		pages++
		if pages > 10 {
			t.Fatalf("the cursor is not advancing: still reading pages after %d of them", pages)
		}
		if len(page) > anchorRefPageSize {
			t.Fatalf("page %d returned %d refs, more than the page size %d", pages, len(page), anchorRefPageSize)
		}
		for _, r := range page {
			got[r.Type+"|"+r.Token] = true
			total++
		}
		last := page[len(page)-1]
		lastType, lastToken = last.Type, last.Token
	}

	if pages != 2 {
		t.Errorf("expected %d refs to take 2 pages at a page size of %d, took %d", seeded, anchorRefPageSize, pages)
	}
	if total != seeded {
		t.Errorf("the walk returned %d refs for %d distinct ones — a ref was repeated or skipped", total, seeded)
	}
	if len(got) != len(want) {
		t.Errorf("the walk covered %d distinct refs, want %d", len(got), len(want))
	}
	for key := range want {
		if !got[key] {
			t.Errorf("the walk never returned %s", key)
		}
	}
}

// DistinctAnchorTenants is a cross-tenant read: given a system context it returns
// every tenant with anchors.
func TestDistinctAnchorTenants(t *testing.T) {
	api := newPersistenceTestApi(t)
	occurred := time.Date(2026, 7, 6, 20, 0, 0, 0, time.UTC)
	for _, tenant := range []string{"acme", "globex"} {
		tctx := core.WithTenant(context.Background(), tenant)
		if err := api.CreateEventAnchors(tctx, api.RDB.DB(tctx), []*EventAnchor{
			{EventId: anchorEventId("device-1", occurred), DeviceToken: "device-1", EventType: esmodel.Measurement, OccurredTime: occurred, AnchorType: "customer", AnchorToken: "cust-1"},
		}); err != nil {
			t.Fatalf("seed %s: %v", tenant, err)
		}
	}

	tenants, err := api.DistinctAnchorTenants(core.WithSystemContext(context.Background()))
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"acme", "globex"}, tenants)
}
