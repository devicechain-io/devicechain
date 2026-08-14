// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// TestExpiryWalksPastTheFirstPage is the size fix on the expiry sweep.
//
// 🔴 THE SET IS AS LARGE AS THE OUTAGE WAS LONG. Every non-terminal command whose TTL has
// elapsed, across every tenant — an instance coming back after a weekend with a held
// backlog per tenant can have a very large one, and the unpaged read loaded all of it into
// the pod at once.
//
// 🔑 THE ASSERTION IS THAT NOTHING SURVIVES THE WALK, which is what makes it a paging test
// rather than an expiry test: with more rows than fit in one page, a walk that stopped at
// the first page would leave the rest QUEUED forever, and the sweep would look like it was
// working every single tick.
func TestExpiryWalksPastTheFirstPage(t *testing.T) {
	api := newTestApi(t)
	sysctx := core.WithSystemContext(core.WithTenant(context.Background(), "A"))
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)

	const total = expirePageSize + 17
	for i := range total {
		if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
			Token: fmt.Sprintf("cmd-%04d", i), DeviceToken: "d", Name: "x", ExpiresAt: &past,
		}); err != nil {
			t.Fatalf("create %d failed: %v", i, err)
		}
	}

	count, byFromStatus, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("ExpireStale failed: %v", err)
	}
	if count != total {
		t.Fatalf("expected all %d stale commands to expire, got %d — the walk stopped at a page boundary",
			total, count)
	}
	if byFromStatus[CommandQueued.String()] != total {
		t.Fatalf("the breakdown must account for every expired row, got %v", byFromStatus)
	}

	// And a second sweep finds nothing: the rows really did move, rather than being
	// counted repeatedly by a cursor that never advanced.
	again, _, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("second ExpireStale failed: %v", err)
	}
	if again != 0 {
		t.Fatalf("a second sweep expired %d more commands; the first one did not finish", again)
	}
}

// TestTheCursorAdvancesPastARowThatREFUSEDToMove is the rule the walk's termination rests
// on, staged for real.
//
// 🔴 THE CURSOR ADVANCES PAST THE LAST ROW SEEN, NOT THE LAST ROW EXPIRED. Expiry is a
// conditional update predicated on the status the SCAN observed, so a row that moved on in
// between is not expired by us — and it is still in the scan's result set. Advancing from
// the last row EXPIRED would hand the next page that same row again.
//
// 🔑 STAGING THIS NEEDS THE RACE, NOT A LOOKALIKE. The first version of this test filled
// the page with SENT rows and asserted only that the walk terminated. That is vacuous
// twice over: SENT rows expire (to TIMEOUT), so nothing actually refused to move — and a
// test whose failure mode is "hangs" is not a control, it is a timeout. Measured: the
// mutation this test names survived the whole package. So the race is staged deterministically
// by flipping the last row of the first page AFTER the page has been read but BEFORE its
// conditional update runs, which is precisely the interleaving production hits.
func TestTheCursorAdvancesPastARowThatREFUSEDToMove(t *testing.T) {
	api := newTestApi(t)
	sysctx := core.WithSystemContext(core.WithTenant(context.Background(), "A"))
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)

	const total = expirePageSize + 5
	for i := range total {
		if _, err := api.CreateCommand(sysctx, &CommandCreateRequest{
			Token: fmt.Sprintf("stuck-%04d", i), DeviceToken: "d", Name: "x", ExpiresAt: &past,
		}); err != nil {
			t.Fatalf("create %d failed: %v", i, err)
		}
	}

	// The row that will refuse to move: the LAST of the first page, because that is the
	// one the cursor is taken from.
	stolen := stealLastRowOfTheFirstPage(t, api)

	count, _, err := api.ExpireStale(sysctx, time.Now())
	if err != nil {
		t.Fatalf("ExpireStale failed: %v", err)
	}

	// 🔑 THE ASSERTION IS THAT THE STOLEN ROW WAS LEFT ALONE. Under the mutation the walk
	// rewinds to the last row it expired, re-reads the stolen row on the next page — by
	// then scanning it in its NEW status, so the conditional update matches — and expires
	// it after all. Correct code never revisits it: the cursor is already past.
	var after Command
	if err := api.RDB.DB(sysctx).Where("id = ?", *stolen).First(&after).Error; err != nil {
		t.Fatalf("re-reading the stolen row: %v", err)
	}
	if after.Status != CommandSent.String() {
		t.Fatalf("the row that moved on between the scan and the write was expired anyway (status %s); "+
			"the cursor rewound to the last row EXPIRED and re-read it", after.Status)
	}
	if count != total-1 {
		t.Fatalf("expected %d expiries (every row but the one that moved on), got %d", total-1, count)
	}
}

// stealLastRowOfTheFirstPage arms a one-shot hook that moves the last row of the first
// full page into a different non-terminal status the instant that page has been read.
// It returns a pointer that is filled with the stolen row's id when the hook fires.
//
// 🔑 IT WRITES THROUGH THE RAW *sql.DB, NOT THROUGH GORM. Going back through gorm from
// inside a gorm callback would re-enter the tenant-scoping and token-grammar callbacks
// this database has registered — and the write has to land regardless of what the
// walk's own statement is doing. The status chosen is non-terminal on purpose: a
// terminal one would simply drop out of the scan's predicate, which is a different
// (and much more forgiving) story than the row the conditional update misses.
func stealLastRowOfTheFirstPage(t *testing.T, api *Api) *uint {
	t.Helper()
	gormDB := api.RDB.Database
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatalf("reaching the raw database handle: %v", err)
	}
	stolen := new(uint)
	err = gormDB.Callback().Query().After("gorm:query").Register("test:steal_last_row", func(tx *gorm.DB) {
		if *stolen != 0 {
			return // one shot: later pages must run untouched
		}
		rows, ok := tx.Statement.Dest.(*[]*Command)
		if !ok || rows == nil || len(*rows) != expirePageSize {
			return // not the walk's page read
		}
		last := (*rows)[len(*rows)-1]
		if _, err := sqlDB.Exec(
			"UPDATE commands SET status = ? WHERE id = ?", CommandSent.String(), last.ID); err != nil {
			t.Errorf("staging the lost race: %v", err)
			return
		}
		*stolen = last.ID
	})
	if err != nil {
		t.Fatalf("registering the steal hook: %v", err)
	}
	t.Cleanup(func() { _ = gormDB.Callback().Query().Remove("test:steal_last_row") })
	return stolen
}
