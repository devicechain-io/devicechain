// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// seedAsserted seeds n asserted devices for one source and returns the api + ctx.
func seedAsserted(t *testing.T, n int) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	for i := range n {
		_, err := api.MergeDeviceState(ctx, fmt.Sprintf("dev-%03d", i), at,
			&PresenceTransition{Connected: true, SessionId: uint64(i + 1), OccurredAt: at},
			DeviceIdentity{Source: "mqtt1", ExternalId: fmt.Sprintf("ext-%03d", i)})
		if err != nil {
			t.Fatalf("seeding device %d: %v", i, err)
		}
	}
	return api, ctx
}

// TestTheAssertedWalkCoversEveryDeviceAcrossPages is the read the reconcilers depend on.
//
// 🔑 THE CURSOR HAS TO BE KEYSET, NOT AN OFFSET. Rows are written underneath this walk by
// every connecting device, and an OFFSET page would skip one row for every insertion
// before the cursor — a device silently not reconciled, which is indistinguishable from a
// device that needed no repair.
func TestTheAssertedWalkCoversEveryDeviceAcrossPages(t *testing.T) {
	const devices = 25
	api, ctx := seedAsserted(t, devices)

	seen := map[string]bool{}
	var after uint64
	for pages := 0; pages < 20; pages++ {
		page, err := api.AssertedDeviceStates(ctx, "mqtt1", false, after, 10)
		if err != nil {
			t.Fatalf("page after %d: %v", after, err)
		}
		for _, ds := range page {
			if seen[ds.DeviceToken] {
				t.Fatalf("device %s was returned twice; the cursor is not advancing", ds.DeviceToken)
			}
			seen[ds.DeviceToken] = true
		}
		if len(page) < 10 {
			break
		}
		after = uint64(page[len(page)-1].ID)
	}
	if len(seen) != devices {
		t.Fatalf("the walk covered %d of %d devices", len(seen), devices)
	}
}

// TestAPageIsNeverLargerThanAsked is what makes "a short page means the end" safe for the
// caller's termination rule.
func TestAPageIsNeverLargerThanAsked(t *testing.T) {
	api, ctx := seedAsserted(t, 12)

	page, err := api.AssertedDeviceStates(ctx, "mqtt1", false, 0, 5)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if len(page) != 5 {
		t.Fatalf("asked for 5 rows and got %d", len(page))
	}
}

// TestAnOutOfRangePageSizeIsRefusedRatherThanClamped is the fail-safe, and the direction
// matters more than the check.
//
// 🔴 A CLAMP WOULD BREAK THE CALLER'S TERMINATION RULE, WHICH IS WORSE THAN THE UNBOUNDED
// READ IT REPLACES. Callers walk until a page is shorter than the one they asked for.
// Answer 1000 to a request for 2000 and the caller sees a short page on its FIRST call,
// concludes it has the whole set, and stops — with most of the fleet unread and every
// device in it about to be treated as though the projection had no row for it. A refusal
// is a bug the caller cannot miss; a clamp is one it cannot see.
//
// Zero gets the same treatment for the same reason, and because a zero that meant
// "unlimited" would quietly restore exactly the unbounded read this replaces.
func TestAnOutOfRangePageSizeIsRefusedRatherThanClamped(t *testing.T) {
	api, ctx := seedAsserted(t, 3)

	for _, pageSize := range []int{0, -1, MaxAssertedPageSize + 1} {
		rows, err := api.AssertedDeviceStates(ctx, "mqtt1", false, 0, pageSize)
		if err == nil {
			t.Fatalf("pageSize %d must be refused, got %d rows and no error", pageSize, len(rows))
		}
		if rows != nil {
			t.Fatalf("a refused read must return no rows, got %d for pageSize %d", len(rows), pageSize)
		}
	}

	// And the boundary itself is accepted — a check that also rejected the largest legal
	// page would be a different bug wearing the same clothes.
	if _, err := api.AssertedDeviceStates(ctx, "mqtt1", false, 0, MaxAssertedPageSize); err != nil {
		t.Fatalf("the largest legal page must be accepted, got %v", err)
	}
}
