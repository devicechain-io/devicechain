// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// The three creation timestamps the ordering fixture is built from. Two rows share
// stateTied on purpose — see seedStatesForOrdering.
var (
	stateOldest = time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	stateTied   = time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	stateNewest = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
)

// seedStatesForOrdering writes four projection rows ANTI-SORTED and returns the api +
// tenant context.
//
// 🔴 THE INSERTION ORDER IS DELIBERATELY NOT THE EXPECTED OUTPUT ORDER. SQLite hands
// rows back in insertion order when a query names no ORDER BY, so a fixture seeded in
// its expected order passes whether or not the read orders anything at all — which is
// exactly how unordered list endpoints reach production with a green suite. Rows go in
// as dev-c, dev-d, dev-a, dev-b; the declared order reads them back as dev-d, dev-b,
// dev-c, dev-a.
//
// dev-c and dev-b carry the SAME created_at, which is the only thing that exercises the
// tiebreak half of the clause. The tiebreak here is `id DESC` rather than a token (the
// projection carries no registry token), so the pair is inserted LOW id first — dev-c
// before dev-b — which means a correct read has to hand back the LATER-inserted row
// first. Insertion order and expected order therefore disagree inside the tie group as
// well as across it.
//
// created_at is stamped explicitly rather than left to the clock: gorm writes now() on
// create, and four merges inside one test do not reliably share a tick (or reliably
// differ), so the tie has to be asserted into existence.
func seedStatesForOrdering(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	occurred := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	lastID := uint(0)
	for _, row := range []struct {
		token     string
		createdAt time.Time
	}{
		{"dev-c", stateTied},
		{"dev-d", stateNewest},
		{"dev-a", stateOldest},
		{"dev-b", stateTied},
	} {
		ds, err := api.MergeDeviceState(ctx, row.token, occurred, nil, DeviceIdentity{})
		if err != nil {
			t.Fatalf("seeding %s: %v", row.token, err)
		}
		// The expected sequence below is written in terms of ids ascending with
		// insertion order. Assert that rather than trust it: if the projection ever
		// stopped handing out monotonic ids, the fixture would silently stop being
		// anti-sorted inside the tie group and the tiebreak would go unexercised.
		if ds.ID <= lastID {
			t.Fatalf("seeding %s: id %d did not advance past %d", row.token, ds.ID, lastID)
		}
		lastID = ds.ID

		result := api.RDB.DB(ctx).Model(&DeviceState{}).Where("device_token = ?", row.token).
			UpdateColumn("created_at", row.createdAt)
		if result.Error != nil {
			t.Fatalf("stamping created_at on %s: %v", row.token, result.Error)
		}
		if result.RowsAffected != 1 {
			t.Fatalf("stamping created_at on %s touched %d rows", row.token, result.RowsAffected)
		}
	}
	return api, ctx
}

// stateTokens renders a page as a comma-joined device-token sequence so a mismatch
// reports the whole ordering rather than the first differing element.
func stateTokens(rows []DeviceState) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.DeviceToken)
	}
	return strings.Join(out, ",")
}

// The paged device-state search reads newest-first, and two rows created in the same
// tick come back on the id tiebreak rather than in the order they were written.
func TestDeviceStatesReadNewestFirstWithIdTiebreak(t *testing.T) {
	api, ctx := seedStatesForOrdering(t)

	results, err := api.DeviceStates(ctx, DeviceStateSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("DeviceStates: %v", err)
	}

	const want = "dev-d,dev-b,dev-c,dev-a"
	if got := stateTokens(results.Results); got != want {
		t.Fatalf("device-state order = %s, want %s (insertion order was dev-c,dev-d,dev-a,dev-b)", got, want)
	}
}

// Walking the search two rows at a time yields every row exactly once — the
// repeat-and-skip property a missing ORDER BY actually breaks, which a single-page
// order assertion does not imply.
//
// This is the PAGED search only. AssertedDeviceStates walks its own keyset cursor with
// `id ASC` and does not go through ListOf; its coverage guarantee is pinned separately
// in asserted_paging_test.go.
func TestDeviceStatesPageAcrossBoundaryWithoutDuplicatesOrGaps(t *testing.T) {
	api, ctx := seedStatesForOrdering(t)

	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results, err := api.DeviceStates(ctx, DeviceStateSearchCriteria{
			Pagination: rdb.Pagination{PageNumber: page, PageSize: 2},
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(results.Results) != 2 {
			t.Fatalf("page %d returned %d rows, want 2", page, len(results.Results))
		}
		seen = append(seen, strings.Split(stateTokens(results.Results), ",")...)
	}

	const want = "dev-d,dev-b,dev-c,dev-a"
	if got := strings.Join(seen, ","); got != want {
		t.Fatalf("paged walk = %s, want %s (each row exactly once)", got, want)
	}
}
