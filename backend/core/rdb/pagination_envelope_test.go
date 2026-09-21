// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"
)

// The envelope ListOf reports must describe the page it actually returned.
//
// 🔴 NOTHING IN THIS PACKAGE READ PageStart/PageEnd/TotalRecords BEFORE THIS TEST. Every
// paging test asserted the SQL (is there a LIMIT, is there an OFFSET) or the rows, so the
// arithmetic that tells a CLIENT where it is was pinned nowhere — a mutant that dropped
// the `total < last` clamp survived this package and device-management's graphql suite too.
//
// 🔑 THE CLAMP IS THE PART THAT MATTERS. A client pages until PageEnd == TotalRecords. If
// PageEnd is allowed to run past the total, that loop never terminates on a partial last
// page; if it stops short, the client quietly drops the tail.
func TestListOfReportsThePageItActuallyReturned(t *testing.T) {
	rdb := newSortableDB(t) // four rows
	const total = int32(4)

	for _, tc := range []struct {
		name               string
		page, size         int32
		wantRows           int
		wantStart, wantEnd int32
	}{
		{"a full first page", 1, 3, 3, 1, 3},
		// The partial last page: `last` would be 6, and the clamp brings it back to 4.
		{"a partial last page", 2, 3, 1, 4, 4},
		{"a page that exactly fits", 1, 4, 4, 1, 4},
		{"one row per page", 3, 1, 1, 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := make([]sortableRow, 0)
			db, pag := rdb.ListOf(context.Background(), &sortableRow{}, nil,
				Pagination{PageNumber: tc.page, PageSize: tc.size})
			if err := db.Find(&results).Error; err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(results) != tc.wantRows {
				t.Errorf("returned %d rows, want %d", len(results), tc.wantRows)
			}
			if pag.PageStart != tc.wantStart || pag.PageEnd != tc.wantEnd {
				t.Errorf("reported span %d-%d, want %d-%d",
					pag.PageStart, pag.PageEnd, tc.wantStart, tc.wantEnd)
			}
			if pag.TotalRecords != total {
				t.Errorf("reported %d total records, want %d", pag.TotalRecords, total)
			}
			// The invariant behind the clamp, stated on its own so a future page size
			// cannot satisfy the cases above while breaking the rule they stand for.
			if pag.PageEnd > pag.TotalRecords {
				t.Errorf("PageEnd %d runs past TotalRecords %d: a client paging until the "+
					"two are equal would never stop", pag.PageEnd, pag.TotalRecords)
			}
		})
	}
}

// ListAllOf's envelope spans the whole set, since there is no page to describe.
func TestListAllOfReportsTheWholeSpan(t *testing.T) {
	rdb := newSortableDB(t)
	results := make([]sortableRow, 0)
	db, pag := rdb.ListAllOf(context.Background(), &sortableRow{}, nil)
	if err := db.Find(&results).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("returned %d rows, want all 4", len(results))
	}
	if pag.PageStart != 1 || pag.PageEnd != 4 || pag.TotalRecords != 4 {
		t.Errorf("reported span %d-%d of %d, want 1-4 of 4",
			pag.PageStart, pag.PageEnd, pag.TotalRecords)
	}
}

// PageSlice applies the same contract to a collection already in memory.
//
// 🔴 IT HAD NO TEST ANYWHERE IN THE REPOSITORY. Its one caller is the frozen geofence-set
// snapshot resolver, whose own fixtures hold a SINGLE fence while asking for pages of two —
// so the page size never bit, and a mutant that ignored the size entirely and returned
// everything survived both this package and that one. A fixture smaller than the page it
// requests cannot observe paging at all.
//
// 🔑 IT LIVES BESIDE ListOf BECAUSE THE TWO MUST AGREE: a client paging a GraphQL field
// backed by this must see the same span arithmetic as one paging a SQL-backed list.
func TestPageSliceMatchesTheListOfContract(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	const total = int32(5)

	for _, tc := range []struct {
		name               string
		page, size         int32
		want               []string
		wantStart, wantEnd int32
	}{
		{"the first page", 1, 2, []string{"a", "b"}, 1, 2},
		{"a middle page", 2, 2, []string{"c", "d"}, 3, 4},
		{"a partial last page", 3, 2, []string{"e"}, 5, 5},
		{"past the end is empty, not the last page again", 9, 2, []string{}, 17, 5},
		// A page number below 1 floors to 1 rather than producing a negative offset.
		{"a zero page number floors to the first page", 0, 2, []string{"a", "b"}, 1, 2},
		// An over-max size is clamped, so it cannot be used to fetch everything.
		{"an over-max page size is clamped", 1, MaxPageSize * 10, items, 1, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, pag := PageSlice(items, Pagination{PageNumber: tc.page, PageSize: tc.size})
			if len(got) != len(tc.want) {
				t.Fatalf("returned %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("returned %v, want %v", got, tc.want)
				}
			}
			if pag.PageStart != tc.wantStart || pag.PageEnd != tc.wantEnd {
				t.Errorf("reported span %d-%d, want %d-%d",
					pag.PageStart, pag.PageEnd, tc.wantStart, tc.wantEnd)
			}
			if pag.TotalRecords != total {
				t.Errorf("reported %d total records, want %d", pag.TotalRecords, total)
			}
		})
	}
}
