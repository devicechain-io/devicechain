// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestListPageOfLookaheadAndParity(t *testing.T) {
	for _, tc := range []struct {
		page, size int32
		more       bool
		start, end int32
	}{
		{1, 3, true, 1, 3}, {1, 4, false, 1, 4}, {2, 2, false, 3, 4}, {9, 2, false, 17, 16},
		{0, 3, true, 1, 3}, {math.MaxInt32, 2, false, math.MaxInt32, math.MaxInt32 - 1},
	} {
		manager := newSortableDB(t)
		var statements []string
		if err := manager.Database.Callback().Query().After("gorm:query").Register("capture_page", func(db *gorm.DB) {
			statements = append(statements, strings.ToLower(db.Statement.SQL.String()))
		}); err != nil {
			t.Fatal(err)
		}
		// A filter-supplied order must remain ahead of the model's default order.
		filters := func(db *gorm.DB) *gorm.DB { return db.Where("token <> ?", "absent").Order("token DESC") }
		pag := Pagination{PageNumber: tc.page, PageSize: tc.size}
		db, w := manager.ListPageOf(context.Background(), &sortableRow{}, filters, pag)
		var got []sortableRow
		if err := db.Find(&got).Error; err != nil {
			t.Fatal(err)
		}
		if len(statements) != 1 || strings.Contains(statements[0], "count(") || !strings.Contains(statements[0], "limit "+strconv.Itoa(int(tc.size)+1)) {
			t.Fatalf("unexpected SQL: %v", statements)
		}
		got, w = TrimPage(got, w)
		if w.HasMore != tc.more || w.PageStart != tc.start || w.PageEnd != tc.end {
			t.Fatalf("page %+v: %+v", tc, w)
		}
		statements = nil
		db, total := manager.ListOf(context.Background(), &sortableRow{}, filters, pag)
		var want []sortableRow
		if err := db.Find(&want).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rows differ: %v vs %v", got, want)
		}
		if total.TotalRecords != 4 || len(statements) != 2 || !strings.Contains(statements[0], "count(") {
			t.Fatalf("counted negative control: %v, %+v", statements, total)
		}
	}
}

func TestTrimPageSaturatesEnd(t *testing.T) {
	rows, w := TrimPage([]int{1, 2, 3}, PageWindow{PageStart: math.MaxInt32, size: 2})
	if len(rows) != 2 || !w.HasMore || w.PageEnd != math.MaxInt32 {
		t.Fatalf("%v %+v", rows, w)
	}
}
