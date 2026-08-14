// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// idThing is a tenant-scoped registry entity — the shape every xById lookup targets.
// Tenant scoping is part of the fixture on purpose: it is what bounds the damage of the
// bug this file is about (an over-read stays inside one tenant), and it is also what
// makes the bug easy to wave away. It does not prevent it.
type idThing struct {
	gorm.Model
	TenantScoped
	Name string
}

func newFindByIdsDB(t *testing.T) (*gorm.DB, context.Context) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := RegisterTenantScoping(db); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.AutoMigrate(&idThing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	for _, name := range []string{"one", "two", "three"} {
		if err := db.WithContext(ctx).Create(&idThing{Name: name}).Error; err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	return db, ctx
}

func seededIds(t *testing.T, db *gorm.DB, ctx context.Context) []uint {
	t.Helper()
	all := make([]*idThing, 0)
	if err := db.WithContext(ctx).Order("id").Find(&all).Error; err != nil {
		t.Fatalf("read back seeds: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("seeded %d rows, want 3", len(all))
	}
	ids := make([]uint, 0, len(all))
	for _, row := range all {
		ids = append(ids, row.ID)
	}
	return ids
}

// The guard. An empty id list is a request for nothing, and must be answered with
// nothing — not with the table.
func TestFindByIdsWithNoIdsReturnsNoRows(t *testing.T) {
	db, ctx := newFindByIdsDB(t)

	for _, tc := range []struct {
		name string
		ids  []uint
	}{
		{"empty slice", []uint{}},
		{"nil slice", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, err := FindByIds[idThing](db.WithContext(ctx), tc.ids)
			if err != nil {
				t.Fatalf("FindByIds: %v", err)
			}
			if len(found) != 0 {
				t.Fatalf("FindByIds(%v) returned %d rows, want 0 — the id filter was dropped",
					tc.ids, len(found))
			}
			if found == nil {
				t.Fatal("FindByIds returned a nil slice; callers marshal this straight to a GraphQL list")
			}
		})
	}
}

// The counterweight. Without this, a helper that returned an empty slice unconditionally
// would satisfy the guard test above and pass review while breaking every lookup.
func TestFindByIdsReturnsExactlyTheRowsNamed(t *testing.T) {
	db, ctx := newFindByIdsDB(t)
	ids := seededIds(t, db, ctx)

	found, err := FindByIds[idThing](db.WithContext(ctx), []uint{ids[0], ids[2]})
	if err != nil {
		t.Fatalf("FindByIds: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("FindByIds(2 ids) returned %d rows, want 2", len(found))
	}
	for _, row := range found {
		if row.ID != ids[0] && row.ID != ids[2] {
			t.Fatalf("FindByIds returned id %d, which was not asked for", row.ID)
		}
	}

	// An id that exists in no row is not an error, just no row — several resolvers
	// depend on the partial answer rather than a failure.
	missing, err := FindByIds[idThing](db.WithContext(ctx), []uint{ids[1], 99999})
	if err != nil {
		t.Fatalf("FindByIds with an unknown id: %v", err)
	}
	if len(missing) != 1 || missing[0].ID != ids[1] {
		t.Fatalf("FindByIds([known, unknown]) = %d rows, want just the known one", len(missing))
	}
}

// Pins the third-party behaviour the guard exists for, rather than asserting it in a
// comment. This is the raw call every xById made before the sweep: same handle, same
// empty input, and it comes back with the whole table.
//
// If gorm ever starts rendering a match-nothing predicate here, THIS test fails and the
// guard becomes belt-and-braces — which is worth being told about, since the comment on
// FindByIds would then be describing a hazard that no longer exists.
func TestGormDropsAnEmptyInlineIdSlice(t *testing.T) {
	db, ctx := newFindByIdsDB(t)

	unguarded := make([]*idThing, 0)
	if err := db.WithContext(ctx).Find(&unguarded, []uint{}).Error; err != nil {
		t.Fatalf("unguarded Find: %v", err)
	}
	if len(unguarded) != 3 {
		t.Fatalf("gorm returned %d rows for an empty inline id slice, want the whole table (3). "+
			"If this is now 0, gorm has been fixed — see the comment on FindByIds", len(unguarded))
	}

	// The sibling form, for contrast: identical call shape, opposite behaviour. This
	// asymmetry is why the bug survived — the safe lookup sits one function away from
	// the unsafe one in every api_*.go.
	byToken := make([]*idThing, 0)
	if err := db.WithContext(ctx).Find(&byToken, "name in ?", []string{}).Error; err != nil {
		t.Fatalf("sibling Find: %v", err)
	}
	if len(byToken) != 0 {
		t.Fatalf("the `name in ?` form returned %d rows for an empty slice, want 0", len(byToken))
	}
}
