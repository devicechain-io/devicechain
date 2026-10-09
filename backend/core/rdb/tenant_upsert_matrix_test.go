// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/devicechain-io/dc-microservice/core"
)

// The upsert matrix. It is written once against *gorm.DB and run twice: on SQLite (always,
// in tenant_upsert_test.go) and on a real PostgreSQL (tenant_upsert_integration_test.go),
// because the thing under test is how each database treats the DO UPDATE arm and how many
// rows it reports.
//
// Every case asserts the ROWS afterwards, read back under a system context, and not just an
// error: an error can follow a committed write.

// upWidget is the common shape: a surrogate key that does not include the tenant, so Save's
// fallback upsert conflicts on `id` alone.
type upWidget struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Name string
	Note string
}

// upSlug has a natural key (slug) that is unique across tenants and omits the tenant column.
type upSlug struct {
	ID uint `gorm:"primaryKey"`
	TenantScoped
	Slug string `gorm:"uniqueIndex;size:64"`
	Name string
}

// upPair's primary key includes the tenant, spelled TenantId.
type upPair struct {
	TenantId string `gorm:"primaryKey;size:128"`
	Key      string `gorm:"primaryKey;size:64"`
	Val      string
}

// upPlain is the second spelling of the tenant column, `tenant`.
type upPlain struct {
	Tenant string `gorm:"primaryKey;size:128"`
	Key    string `gorm:"primaryKey;size:64"`
	Val    string
}

// upFree has no tenant column at all.
type upFree struct {
	Key string `gorm:"primaryKey;size:64"`
	Val string
}

func upsertModels() []any {
	return []any{&upWidget{}, &upSlug{}, &upPair{}, &upPlain{}, &upFree{}}
}

var (
	upCtxA   = core.WithTenant(context.Background(), "tenant-a")
	upCtxB   = core.WithTenant(context.Background(), "tenant-b")
	upSystem = core.WithSystemContext(context.Background())
)

// upsertTruncate empties every matrix table.
func upsertTruncate(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, m := range upsertModels() {
		require.NoError(t, db.WithContext(upSystem).Session(&gorm.Session{AllowGlobalUpdate: true}).
			Unscoped().Delete(m).Error)
	}
}

func upWidgetRow(t *testing.T, db *gorm.DB, id uint) (w upWidget, found bool) {
	t.Helper()
	res := db.WithContext(upSystem).Where("id = ?", id).Find(&w)
	require.NoError(t, res.Error)
	return w, res.RowsAffected == 1
}

func upSlugRow(t *testing.T, db *gorm.DB, slug string) (s upSlug, found bool) {
	t.Helper()
	res := db.WithContext(upSystem).Where("slug = ?", slug).Find(&s)
	require.NoError(t, res.Error)
	return s, res.RowsAffected == 1
}

func upCount(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.WithContext(upSystem).Model(model).Count(&n).Error)
	return n
}

// victimSave is what a cross-tenant Save leaves behind: tenant A saves a row carrying tenant
// B's primary key, and the probe reports B's row afterwards and the error A got.
func victimSave(t *testing.T, db *gorm.DB, forged func() any) (after upWidget, err error) {
	t.Helper()
	require.NoError(t, db.WithContext(upCtxB).Create(&upWidget{ID: 100, Name: "b-name", Note: "b-note"}).Error)
	err = db.WithContext(upCtxA).Save(forged()).Error
	after, found := upWidgetRow(t, db, 100)
	require.True(t, found, "tenant B's row must still exist")
	return after, err
}

// victimUnchanged asserts tenant B's row is exactly as B wrote it, every column.
func victimUnchanged(t *testing.T, after upWidget) {
	t.Helper()
	assert.Equal(t, "tenant-b", after.TenantId, "the row's tenant must not move")
	assert.Equal(t, "b-name", after.Name)
	assert.Equal(t, "b-note", after.Note)
}

// runTenantUpsertMatrix is the whole matrix.
func runTenantUpsertMatrix(t *testing.T, db *gorm.DB) {
	t.Run("Save with another tenant's id changes nothing and fails", func(t *testing.T) {
		upsertTruncate(t, db)
		after, err := victimSave(t, db, func() any { return &upWidget{ID: 100, Name: "hijack", Note: "hijack"} })
		victimUnchanged(t, after)
		assert.ErrorIs(t, err, ErrTenantUpsertConflict)
		assert.EqualValues(t, 1, upCount(t, db, &upWidget{}))
	})

	t.Run("Save of a slice with another tenant's id changes nothing and fails", func(t *testing.T) {
		upsertTruncate(t, db)
		after, err := victimSave(t, db, func() any {
			return &[]upWidget{{ID: 100, Name: "hijack", Note: "hijack"}}
		})
		victimUnchanged(t, after)
		assert.ErrorIs(t, err, ErrTenantUpsertConflict)
	})

	t.Run("a batch holding one foreign conflict is rolled back whole", func(t *testing.T) {
		upsertTruncate(t, db)
		after, err := victimSave(t, db, func() any {
			return &[]upWidget{{ID: 101, Name: "fresh"}, {ID: 100, Name: "hijack", Note: "hijack"}}
		})
		victimUnchanged(t, after)
		assert.ErrorIs(t, err, ErrTenantUpsertConflict)
		_, found := upWidgetRow(t, db, 101)
		assert.False(t, found, "the statement must not leave the batch's other row behind")
	})

	t.Run("explicit upsert on a marked tenant-less key cannot touch another tenant's row", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxB).Create(&upSlug{ID: 1, Slug: "s", Name: "b-name"}).Error)
		err := AllowTenantlessUpsert(db.WithContext(upCtxA), "slug is unique per tenant in this test").
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "slug"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "tenant_id"}),
			}).Create(&upSlug{ID: 2, Slug: "s", Name: "hijack"}).Error
		assert.ErrorIs(t, err, ErrTenantUpsertConflict)
		got, found := upSlugRow(t, db, "s")
		require.True(t, found)
		assert.Equal(t, "tenant-b", got.TenantId)
		assert.Equal(t, "b-name", got.Name)
		assert.EqualValues(t, 1, upCount(t, db, &upSlug{}))
	})

	t.Run("explicit upsert on the primary key cannot touch another tenant's row", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxB).Create(&upWidget{ID: 100, Name: "b-name", Note: "b-note"}).Error)
		err := db.WithContext(upCtxA).Clauses(clause.OnConflict{UpdateAll: true}).
			Create(&upWidget{ID: 100, Name: "hijack"}).Error
		assert.ErrorIs(t, err, ErrTenantUpsertConflict)
		after, _ := upWidgetRow(t, db, 100)
		victimUnchanged(t, after)
	})

	t.Run("a key forged through the tenant field is still refused outright", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxB).Create(&upPlain{Key: "k", Val: "b-val"}).Error)
		err := db.WithContext(upCtxA).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"val"}),
		}).Create(&upPlain{Tenant: "tenant-b", Key: "k", Val: "hijack"}).Error
		assert.ErrorIs(t, err, ErrTenantMismatch)
		var got upPlain
		require.NoError(t, db.WithContext(upSystem).Where("tenant = ? AND key = ?", "tenant-b", "k").First(&got).Error)
		assert.Equal(t, "b-val", got.Val)
	})

	t.Run("same-tenant Save still updates, inserts and batches", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxA).Create(&upWidget{ID: 1, Name: "old", Note: "n"}).Error)
		require.NoError(t, db.WithContext(upCtxA).Save(&upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: "new", Note: "n2"}).Error)
		got, _ := upWidgetRow(t, db, 1)
		assert.Equal(t, upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: "new", Note: "n2"}, got)

		// An unchanged Save is a match, not a miss.
		require.NoError(t, db.WithContext(upCtxA).Save(&upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: "new", Note: "n2"}).Error)

		// A Save of a row that does not exist yet takes the insert path.
		require.NoError(t, db.WithContext(upCtxA).Save(&upWidget{ID: 2, TenantScoped: TenantScoped{"tenant-a"}, Name: "ins"}).Error)
		got, found := upWidgetRow(t, db, 2)
		assert.True(t, found)
		assert.Equal(t, "tenant-a", got.TenantId)

		// A slice Save mixes existing and new rows.
		require.NoError(t, db.WithContext(upCtxA).Save(&[]upWidget{
			{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: "slice-1"}, {ID: 2, TenantScoped: TenantScoped{"tenant-a"}, Name: "slice-2"},
			{ID: 3, TenantScoped: TenantScoped{"tenant-a"}, Name: "slice-3"},
		}).Error)
		for id, want := range map[uint]string{1: "slice-1", 2: "slice-2", 3: "slice-3"} {
			got, found := upWidgetRow(t, db, id)
			assert.True(t, found)
			assert.Equal(t, want, got.Name)
			assert.Equal(t, "tenant-a", got.TenantId)
		}
	})

	t.Run("same-tenant explicit upserts update as before, in both spellings", func(t *testing.T) {
		upsertTruncate(t, db)
		for _, v := range []string{"first", "second"} {
			require.NoError(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"val"}),
			}).Create(&upPair{Key: "k", Val: v}).Error)
			require.NoError(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "tenant"}, {Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"val"}),
			}).Create(&upPlain{Key: "k", Val: v}).Error)
		}
		// The same key under another tenant is a different row, not a conflict.
		require.NoError(t, db.WithContext(upCtxB).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"val"}),
		}).Create(&upPair{Key: "k", Val: "b"}).Error)

		var pairs []upPair
		require.NoError(t, db.WithContext(upSystem).Order("tenant_id").Find(&pairs).Error)
		assert.Equal(t, []upPair{{"tenant-a", "k", "second"}, {"tenant-b", "k", "b"}}, pairs)
		var plain upPlain
		require.NoError(t, db.WithContext(upSystem).First(&plain).Error)
		assert.Equal(t, upPlain{"tenant-a", "k", "second"}, plain)
	})

	t.Run("a same-tenant upsert on a marked tenant-less key updates", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxA).Create(&upSlug{ID: 1, Slug: "s", Name: "old"}).Error)
		require.NoError(t, AllowTenantlessUpsert(db.WithContext(upCtxA), "slug is unique per tenant in this test").
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "slug"}},
				DoUpdates: clause.AssignmentColumns([]string{"name"}),
			}).Create(&upSlug{ID: 9, Slug: "s", Name: "new"}).Error)
		got, _ := upSlugRow(t, db, "s")
		assert.Equal(t, upSlug{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Slug: "s", Name: "new"}, got)
	})

	t.Run("a tenant-less conflict target is refused unless marked with a reason", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxA).Create(&upSlug{ID: 1, Slug: "s", Name: "old"}).Error)
		onSlug := clause.OnConflict{
			Columns:   []clause.Column{{Name: "slug"}},
			DoUpdates: clause.AssignmentColumns([]string{"name"}),
		}
		for name, tx := range map[string]*gorm.DB{
			"unmarked":     db.WithContext(upCtxA),
			"empty reason": AllowTenantlessUpsert(db.WithContext(upCtxA), "  "),
		} {
			err := tx.Clauses(onSlug).Create(&upSlug{ID: 9, Slug: "s", Name: "new"}).Error
			assert.ErrorIs(t, err, ErrTenantlessUpsert, name)
		}
		// A named constraint cannot be inspected, so it needs the marker too.
		err := db.WithContext(upCtxA).Clauses(clause.OnConflict{
			OnConstraint: "up_slugs_pkey",
			DoUpdates:    clause.AssignmentColumns([]string{"name"}),
		}).Create(&upSlug{ID: 1, Name: "new"}).Error
		assert.ErrorIs(t, err, ErrTenantlessUpsert)
		got, _ := upSlugRow(t, db, "s")
		assert.Equal(t, "old", got.Name, "a refused upsert writes nothing")
	})

	t.Run("a caller's own DO UPDATE condition is kept and the guard is added to it", func(t *testing.T) {
		upsertTruncate(t, db)
		skipEqual := clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "up_pairs.val <> excluded.val"}}}
		upsert := func(ctx context.Context, tenant, v string) error {
			return db.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"val"}),
				Where:     skipEqual,
			}).Create(&upPair{Key: "k", Val: v}).Error
		}
		require.NoError(t, upsert(upCtxA, "a", "one"))
		require.NoError(t, upsert(upCtxA, "a", "one")) // her own condition makes this a no-op, not an error
		require.NoError(t, upsert(upCtxA, "a", "two"))
		require.NoError(t, upsert(upCtxB, "b", "other"))
		var pairs []upPair
		require.NoError(t, db.WithContext(upSystem).Order("tenant_id").Find(&pairs).Error)
		assert.Equal(t, []upPair{{"tenant-a", "k", "two"}, {"tenant-b", "k", "other"}}, pairs)
		assert.Len(t, skipEqual.Exprs, 1, "the caller's shared Where must not be appended to")
	})

	t.Run("a system context and an untenanted model are left alone", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxB).Create(&upWidget{ID: 100, Name: "b-name"}).Error)
		// A deliberate system context is the sanctioned unscoped path; it is not guarded.
		require.NoError(t, db.WithContext(upSystem).Save(&upWidget{ID: 100, TenantScoped: TenantScoped{"tenant-b"}, Name: "ops"}).Error)
		got, _ := upWidgetRow(t, db, 100)
		assert.Equal(t, "ops", got.Name)

		for _, v := range []string{"x", "y"} {
			require.NoError(t, db.WithContext(upCtxA).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"val"}),
			}).Create(&upFree{Key: "k", Val: v}).Error)
		}
		var f upFree
		require.NoError(t, db.WithContext(upCtxA).First(&f).Error)
		assert.Equal(t, "y", f.Val)
	})
}

// sabotageUpsertGuard registers a callback that strips the injected guard back out, which
// is what the matrix must notice: the negative control.
func sabotageUpsertGuard(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Create().After("dc:tenant_create").Before("gorm:create").
		Register("test:strip_upsert_guard", func(tx *gorm.DB) {
			c, ok := tx.Statement.Clauses["ON CONFLICT"]
			if !ok {
				return
			}
			oc, _ := c.Expression.(clause.OnConflict)
			kept := []clause.Expression{}
			for _, e := range oc.Where.Exprs {
				if _, g := e.(tenantUpsertGuard); !g {
					kept = append(kept, e)
				}
			}
			oc.Where = clause.Where{Exprs: kept}
			tx.Statement.AddClause(oc)
		}))
}

// runUpsertNegativeControl shows the matrix's cross-tenant probe can see the overwrite it
// guards against: with the guard stripped, tenant B's row is rewritten and moved.
func runUpsertNegativeControl(t *testing.T, db *gorm.DB) {
	upsertTruncate(t, db)
	after, err := victimSave(t, db, func() any { return &upWidget{ID: 100, Name: "hijack", Note: "hijack"} })
	assert.NoError(t, err, "without the guard nothing reports the collision")
	assert.Equal(t, "tenant-a", after.TenantId, "without the guard the row is moved to the caller's tenant")
	assert.Equal(t, "hijack", after.Name)
}
