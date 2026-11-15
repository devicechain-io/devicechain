// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"math"
	"testing"

	"gorm.io/gorm"
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

func TestClampTotal(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want int32
	}{
		{0, 0}, {42, 42}, {math.MaxInt32, math.MaxInt32},
		{1 << 31, math.MaxInt32}, {(1 << 31) + 5, math.MaxInt32},
		{3_000_000_000, math.MaxInt32}, {-1, 0},
	} {
		if got := clampTotal(tc.in); got != tc.want {
			t.Errorf("clampTotal(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestListTotalsSaturate(t *testing.T) {
	rdb := newSortableDB(t)
	countCalls := 0
	// GORM's existing callback seam replaces only the COUNT destination after
	// SQLite has executed it, without a production hook or billions of rows.
	if err := rdb.Database.Callback().Query().After("gorm:query").Register("test:large_count", func(q *gorm.DB) {
		if count, ok := q.Statement.Dest.(*int64); ok && q.Error == nil {
			*count = 3_000_000_000
			countCalls++
		}
	}); err != nil {
		t.Fatal(err)
	}
	query, pag := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: 2, PageSize: 100})
	var rows []sortableRow
	if err := query.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if countCalls != 1 || pag.TotalRecords != math.MaxInt32 || pag.PageStart != 101 || pag.PageEnd != 200 {
		t.Fatalf("COUNT calls=%d, pagination=%+v, want 101-200 of MaxInt32", countCalls, pag)
	}
	query, pag = rdb.ListAllOf(context.Background(), &sortableRow{}, nil)
	if err := query.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if countCalls != 2 || len(rows) != 4 || pag.PageStart != 1 || pag.PageEnd != math.MaxInt32 || pag.TotalRecords != math.MaxInt32 {
		t.Fatalf("COUNT calls=%d, rows=%d, pagination=%+v", countCalls, len(rows), pag)
	}
}

func TestListOfLargePageAndRealCount(t *testing.T) {
	for _, tc := range []struct {
		name               string
		count              int
		page, size         int32
		wantRows           int
		wantStart, wantEnd int32
	}{
		{"largest page", 5, math.MaxInt32, 100, 0, math.MaxInt32, 5},
		{"ordinary real count", 42, 2, 10, 10, 11, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rdb := newSortableDB(t)
			for i := 4; i < tc.count; i++ {
				if err := rdb.Database.Create(&sortableRow{Token: "extra"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			query, pag := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: tc.page, PageSize: tc.size})
			var rows []sortableRow
			if err := query.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.wantRows || pag.TotalRecords != int32(tc.count) || pag.PageStart != tc.wantStart || pag.PageEnd != tc.wantEnd {
				t.Fatalf("rows=%d, pagination=%+v, want %d rows and %d-%d of %d", len(rows), pag, tc.wantRows, tc.wantStart, tc.wantEnd, tc.count)
			}
		})
	}
}

func TestPageSliceLargestPageSaturates(t *testing.T) {
	rows, pag := PageSlice([]int{1, 2, 3, 4, 5}, Pagination{PageNumber: math.MaxInt32, PageSize: 100})
	if len(rows) != 0 || pag.PageStart != math.MaxInt32 || pag.PageEnd != 5 || pag.TotalRecords != 5 {
		t.Fatalf("rows=%v, pagination=%+v", rows, pag)
	}
}
