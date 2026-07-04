// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
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

// Paginate applies a LIMIT for every external request (defaulting/clamping the
// size) and applies NO limit only for the explicit internal Unbounded path.
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

	// pageSize:0 must NOT return everything — it defaults and thus carries a LIMIT.
	if sql := sqlFor(Pagination{PageNumber: 1, PageSize: 0}); !contains(sql, "LIMIT") {
		t.Errorf("pageSize:0 produced no LIMIT (unbounded scan): %q", sql)
	}
	// An over-max request is still LIMITed (clamped), never unbounded.
	if sql := sqlFor(Pagination{PageNumber: 1, PageSize: 100000}); !contains(sql, "LIMIT") {
		t.Errorf("over-max request produced no LIMIT: %q", sql)
	}
	// The explicit internal Unbounded path is the ONLY way to omit the LIMIT.
	if sql := sqlFor(Pagination{Unbounded: true}); contains(sql, "LIMIT") {
		t.Errorf("Unbounded path unexpectedly carried a LIMIT: %q", sql)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
