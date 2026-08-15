// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// sortableRow is a stand-in for the real models: a non-unique column that ties, and
// a unique one to break the tie. Its DefaultOrder is deliberately NOT the insertion
// order — see the fixture below.
type sortableRow struct {
	ID     uint `gorm:"primaryKey"`
	Bucket string
	Token  string
}

func (sortableRow) TableName() string { return "sortable_rows" }

func (sortableRow) DefaultOrder() string {
	return "sortable_rows.bucket DESC, sortable_rows.token ASC"
}

// newSortableDB builds the harness and seeds it ANTI-SORTED — the insertion order is
// t3, t2, t1, t4 while the declared order is t2, t4, t1, t3.
//
// 🔴 THAT IS THE ENTIRE POINT OF THIS FIXTURE. SQLite hands rows back in insertion
// order when a query names no ORDER BY, so a fixture seeded in its expected order
// passes whether or not the code under test orders anything at all. That property is
// exactly how ~30 unordered list endpoints reached production with a green suite:
// the defect is invisible on the harness and only appears on Postgres, under load.
// Seed anti-sorted or this file is decoration.
func newSortableDB(t *testing.T) *RdbManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&sortableRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range []sortableRow{
		{Bucket: "a", Token: "t3"},
		{Bucket: "b", Token: "t2"},
		{Bucket: "a", Token: "t1"},
		{Bucket: "b", Token: "t4"},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("seed %s: %v", r.Token, err)
		}
	}
	return &RdbManager{Database: db}
}

// The declared order is applied, and it beats the order the rows were written in.
func TestListOfAppliesDefaultOrder(t *testing.T) {
	rdb := newSortableDB(t)
	results := make([]sortableRow, 0)
	db, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: 1, PageSize: 10})
	if err := db.Find(&results).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	got := tokensOf(results)
	if want := "t2,t4,t1,t3"; got != want {
		t.Fatalf("order = %s, want %s (insertion order was t3,t2,t1,t4)", got, want)
	}
}

// A nil filters closure must still be ordered. Six call sites pass nil, and an
// injection written inside the closure branch would leave every one of them unordered
// while every test that passes a closure stayed green.
func TestListOfOrdersWithNilFilters(t *testing.T) {
	rdb := newSortableDB(t)
	results := make([]sortableRow, 0)
	db, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: 1, PageSize: 2})
	if err := db.Find(&results).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := tokensOf(results); got != "t2,t4" {
		t.Fatalf("first page with nil filters = %s, want t2,t4", got)
	}
}

// Paging the whole table two rows at a time must yield every row exactly once. This
// is the property the missing ORDER BY actually broke: not "the order looks odd" but
// "a row appears on two pages and another appears on none".
func TestListOfPagesWithoutDuplicatesOrGaps(t *testing.T) {
	rdb := newSortableDB(t)
	seen := make([]string, 0, 4)
	for page := int32(1); page <= 2; page++ {
		results := make([]sortableRow, 0)
		db, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: page, PageSize: 2})
		if err := db.Find(&results).Error; err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		seen = append(seen, strings.Split(tokensOf(results), ",")...)
	}
	if got := strings.Join(seen, ","); got != "t2,t4,t1,t3" {
		t.Fatalf("paged walk = %s, want t2,t4,t1,t3 (each row exactly once)", got)
	}
}

// A closure that sets its own order keeps it as the LEADING key, with the model
// default appended as the tiebreak. That append-not-replace behavior is what makes a
// closure the sanctioned per-query override, so it is pinned here rather than left to
// gorm's merge semantics staying the way they are today.
func TestClosureOrderLeadsAndDefaultTiebreaks(t *testing.T) {
	rdb := newSortableDB(t)
	results := make([]sortableRow, 0)
	db, _ := rdb.ListOf(context.Background(), &sortableRow{}, func(q *gorm.DB) *gorm.DB {
		return q.Order("sortable_rows.bucket ASC")
	}, Pagination{PageNumber: 1, PageSize: 10})
	if err := db.Find(&results).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	// bucket ASC leads (a before b); within each bucket the default's token ASC applies.
	if got := tokensOf(results); got != "t1,t3,t2,t4" {
		t.Fatalf("closure-ordered = %s, want t1,t3,t2,t4", got)
	}
}

// An unbounded read is ordered too. It applies no LIMIT, so it cannot duplicate or
// skip — but its consumers still read off the front of the set, and one of them picks
// which provisioning credential a device gets.
func TestListOfOrdersUnboundedReads(t *testing.T) {
	rdb := newSortableDB(t)
	results := make([]sortableRow, 0)
	db, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{Unbounded: true})
	if err := db.Find(&results).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := tokensOf(results); got != "t2,t4,t1,t3" {
		t.Fatalf("unbounded = %s, want t2,t4,t1,t3", got)
	}
}

// The emitted SQL carries an ORDER BY. The row-order assertions above would also fail
// without one, but only because the fixture is anti-sorted; this asserts the clause
// directly so the guarantee does not rest on that one property of the seed data.
func TestListOfEmitsOrderByClause(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rdb := &RdbManager{Database: db}
	stmt, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: 1, PageSize: 10})
	sql := stmt.Find(&[]sortableRow{}).Statement.SQL.String()
	if !strings.Contains(sql, "ORDER BY") {
		t.Fatalf("no ORDER BY in %q", sql)
	}
	if !strings.Contains(sql, "sortable_rows.token ASC") {
		t.Fatalf("tiebreak column missing from %q", sql)
	}
}

func tokensOf(rows []sortableRow) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return strings.Join(out, ",")
}
