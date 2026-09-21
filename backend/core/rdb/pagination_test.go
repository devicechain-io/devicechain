// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// EffectivePageSize enforces the ADR-029 floor and ceiling: a below-1 request
// falls back to the default, an over-max request is clamped, and an in-range one
// passes through unchanged.
func TestEffectivePageSize(t *testing.T) {
	cases := []struct {
		name string
		in   int32
		want int32
	}{
		{"zero -> default", 0, DefaultPageSize},
		{"negative -> default", -5, DefaultPageSize},
		{"in range passes through", 50, 50},
		{"at max passes through", MaxPageSize, MaxPageSize},
		{"over max -> clamped", MaxPageSize + 1, MaxPageSize},
		{"huge -> clamped", 100000, MaxPageSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Pagination{PageSize: tc.in}).EffectivePageSize(); got != tc.want {
				t.Errorf("EffectivePageSize(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// Paginate applies a LIMIT for EVERY value it can be handed.
//
// 🔑 THIS IS NOW A TOTAL CLAIM OVER THE TYPE, WHICH IS WHY IT IS WRITTEN AS A LOOP.
// Pagination has exactly two fields and both are int32, so the cases below are its
// edges. While it also carried an Unbounded bool the claim could only ever be "every
// request EXCEPT the one that asks not to be limited", and a test of that shape passes
// just as happily when a new caller learns to ask.
func TestPaginateAppliesBounds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	sqlFor := func(pag Pagination) string {
		stmt := db.Model(&struct {
			ID uint
		}{}).Scopes(Paginate(pag)).Find(&[]struct{ ID uint }{}).Statement
		return stmt.SQL.String()
	}

	for _, pag := range []Pagination{
		{}, // the zero value: what an omitted criteria block decodes to
		{PageNumber: 1, PageSize: 0},
		{PageNumber: 1, PageSize: 100000}, // over-max is clamped, never lifted
		{PageNumber: -1, PageSize: -1},    // negatives default rather than disabling
		{PageNumber: math.MaxInt32, PageSize: math.MaxInt32},
		{PageNumber: math.MaxInt32, PageSize: 100},
	} {
		sql := sqlFor(pag)
		if !strings.Contains(sql, "LIMIT") {
			t.Errorf("Pagination%+v produced no LIMIT, which is an unbounded scan: %q", pag, sql)
		}
		// A max-int page number must not overflow the offset into a negative (wrapping
		// to an early page) — int64 math keeps it a large, past-the-end offset.
		if strings.Contains(sql, "OFFSET -") {
			t.Errorf("Pagination%+v overflowed the offset negative: %q", pag, sql)
		}
	}
}

// 🔴🔴 THE OFFSET IS ASSERTED BY VALUE, and the reason is that the obvious assertion
// cannot fail. Paginate widens the offset to int64 so a large page number cannot overflow
// int32 and wrap back to an early page; the test above checks for the absence of
// "OFFSET -", which reads like it covers that — but the `if offset < 0 { offset = 0 }`
// floor three lines below the widening GUARANTEES no negative offset is ever emitted. So
// the check passes whether the widening is there or not.
//
// 🔑 What the defect actually looks like is measured, not imagined: with the widening
// removed, (MaxInt32-1)*100 overflows int32 to zero and gorm emits the statement with NO
// OFFSET AT ALL — page one, silently, for a caller who asked for the last page. A test can
// only see that by naming the number it expects.
func TestALargePageNumberOffsetsPastTheEndInsteadOfWrappingToPageOne(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlFor := func(pag Pagination) string {
		stmt := db.Model(&struct {
			ID uint
		}{}).Scopes(Paginate(pag)).Find(&[]struct{ ID uint }{}).Statement
		return stmt.SQL.String()
	}

	const size = 100
	// int64 arithmetic, mirroring what Paginate must do rather than what int32 would give.
	want := (int64(math.MaxInt32) - 1) * size
	sql := sqlFor(Pagination{PageNumber: math.MaxInt32, PageSize: size})
	if !strings.Contains(sql, fmt.Sprintf("OFFSET %d", want)) {
		t.Errorf("the largest page number did not offset past the end. Want OFFSET %d, got %q. "+
			"An absent or small OFFSET here means the offset wrapped and the caller was handed "+
			"an EARLY page while believing it asked for the last one", want, sql)
	}
}

// The no-LIMIT read still exists — it just has to be asked for BY NAME now. Without
// this, removing the Unbounded flag could equally well have deleted the capability
// outright and every remaining test would still pass.
func TestListAllOfOmitsTheLimitThatListOfAlwaysApplies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rdb := &RdbManager{Database: db}

	all, _ := rdb.ListAllOf(context.Background(), &sortableRow{}, nil)
	if sql := all.Find(&[]sortableRow{}).Statement.SQL.String(); strings.Contains(sql, "LIMIT") {
		t.Errorf("ListAllOf carried a LIMIT, so the full-set read it exists to provide is bounded: %q", sql)
	}

	one, _ := rdb.ListOf(context.Background(), &sortableRow{}, nil, Pagination{PageNumber: 1, PageSize: 10})
	if sql := one.Find(&[]sortableRow{}).Statement.SQL.String(); !strings.Contains(sql, "LIMIT") {
		t.Errorf("ListOf omitted the LIMIT: %q", sql)
	}
}
