// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// upWidgetPatch is a destination of another type than the model, whose tenant column is
// reached through a column tag rather than the model's field name.
type upWidgetPatch struct {
	ID   uint
	Tid  string `gorm:"column:tenant_id"`
	Name string
}

func (upWidgetPatch) TableName() string { return "up_widgets" }

// upWidgetPtrPatch spells the same column as a pointer, which cannot be checked as a string.
type upWidgetPtrPatch struct {
	ID   uint
	Tid  *string `gorm:"column:tenant_id"`
	Name string
}

func (upWidgetPtrPatch) TableName() string { return "up_widgets" }

type upWidgetNullPatch struct {
	ID   uint
	Tid  sql.NullString `gorm:"column:tenant_id"`
	Name string
}

func (upWidgetNullPatch) TableName() string { return "up_widgets" }

// runTenantUpdateMatrix is the update-path counterpart of the create path's stamp-and-refuse
// rule, written once against *gorm.DB and run on SQLite and on PostgreSQL. Every case reads
// both tenants' rows back under a system context.
func runTenantUpdateMatrix(t *testing.T, db *gorm.DB) {
	seed := func(t *testing.T) {
		t.Helper()
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxA).Create(&upWidget{ID: 1, Name: "a-old", Note: "a-note"}).Error)
		require.NoError(t, db.WithContext(upCtxB).Create(&upWidget{ID: 2, Name: "b-name", Note: "b-note"}).Error)
	}
	bUntouched := func(t *testing.T) {
		t.Helper()
		got, found := upWidgetRow(t, db, 2)
		require.True(t, found)
		assert.Equal(t, upWidget{ID: 2, TenantScoped: TenantScoped{"tenant-b"}, Name: "b-name", Note: "b-note"}, got)
	}
	aIs := func(t *testing.T, name, note string) {
		t.Helper()
		got, found := upWidgetRow(t, db, 1)
		require.True(t, found)
		assert.Equal(t, upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: name, Note: note}, got)
	}
	ctxA := func() *gorm.DB { return db.WithContext(upCtxA) }

	t.Run("a Save with a blank tenant is stamped with the context's", func(t *testing.T) {
		seed(t)
		w := &upWidget{ID: 1, Name: "a-new", Note: "a-note"}
		require.NoError(t, ctxA().Save(w).Error)
		assert.Equal(t, "tenant-a", w.TenantId)
		aIs(t, "a-new", "a-note")
		bUntouched(t)
	})

	t.Run("a Save or Updates naming another tenant is refused and writes nothing", func(t *testing.T) {
		seed(t)
		other := TenantScoped{"tenant-b"}
		assert.ErrorIs(t, ctxA().Save(&upWidget{ID: 1, TenantScoped: other, Name: "x"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidget{TenantScoped: other, Name: "x"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(&upWidget{TenantScoped: other, Name: "x"}).Error, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")
		bUntouched(t)
	})

	t.Run("a map that sets the tenant column follows the same rule", func(t *testing.T) {
		seed(t)
		m := func() *gorm.DB { return ctxA().Model(&upWidget{ID: 1}) }
		assert.ErrorIs(t, m().Updates(map[string]any{"tenant_id": "tenant-b", "name": "x"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, m().Updates(map[string]any{"TenantId": "tenant-b"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Update("tenant_id", "tenant-b").Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{"tenant_id": gorm.Expr("'tenant-b'")}).Error, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")

		// A blank is stamped, and the context's own tenant is accepted.
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{"tenant_id": "", "name": "a-blank"}).Error)
		aIs(t, "a-blank", "a-note")
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{"tenant_id": "tenant-a", "name": "a-same"}).Error)
		aIs(t, "a-same", "a-note")
		bUntouched(t)
	})

	t.Run("updates that leave the tenant alone behave as before", func(t *testing.T) {
		seed(t)
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidget{Name: "a-partial"}).Error)
		aIs(t, "a-partial", "a-note")
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Update("note", "n2").Error)
		aIs(t, "a-partial", "n2")
		require.NoError(t, ctxA().Save(&upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-a"}, Name: "a-saved"}).Error)
		aIs(t, "a-saved", "")
		bUntouched(t)
	})

	t.Run("every spelling gorm resolves to the tenant column is checked in a map", func(t *testing.T) {
		for _, key := range []string{"TENANT_ID", "TenantID", "tenantId", "TenantId", `"tenant_id"`, "`tenant_id`", "\"TenantId\""} {
			seed(t)
			err := ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{key: "tenant-b", "name": "x"}).Error
			assert.ErrorIs(t, err, ErrTenantMismatch, key)
			aIs(t, "a-old", "a-note")
			bUntouched(t)
		}
		seed(t)
		// Every key is looked at, not the first match.
		err := ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{"tenant_id": "tenant-a", "TenantID": "tenant-b"}).Error
		assert.ErrorIs(t, err, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")
		// A blank under a spelling gorm resolves is stamped.
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Updates(map[string]any{"TENANT_ID": "", "name": "a-up"}).Error)
		aIs(t, "a-up", "a-note")
		bUntouched(t)
	})

	t.Run("a blank tenant is stamped however the column is selected", func(t *testing.T) {
		for _, sel := range []string{"tenant_id", "TenantId", "up_widgets.tenant_id", `"tenant_id"`, "`tenant_id`", "up_widgets.*"} {
			seed(t)
			require.NoError(t, ctxA().Select(sel, "name").Save(&upWidget{ID: 1, Name: "a-sel"}).Error, sel)
			got, found := upWidgetRow(t, db, 1)
			require.True(t, found, sel)
			assert.Equal(t, "tenant-a", got.TenantId, sel)
			bUntouched(t)
		}
	})

	t.Run("a value update with the tenant selected and blank is stamped on a copy", func(t *testing.T) {
		seed(t)
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Select("tenant_id", "name").Updates(upWidget{Name: "a-copy"}).Error)
		aIs(t, "a-copy", "a-note")
		bUntouched(t)
	})

	t.Run("a SET clause is checked like the other ways of setting a column", func(t *testing.T) {
		set := func(v any) *gorm.DB {
			return ctxA().Model(&upWidget{ID: 1}).Clauses(clause.Set{{Column: clause.Column{Name: "tenant_id"}, Value: v}}).
				Updates(map[string]any{"name": "ignored"})
		}
		seed(t)
		assert.ErrorIs(t, set("tenant-b").Error, ErrTenantMismatch)
		assert.ErrorIs(t, set(gorm.Expr("'tenant-b'")).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Clauses(clause.Assignments(map[string]any{"tenant_id": "tenant-b"})).
			Updates(map[string]any{"name": "ignored"}).Error, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")
		require.NoError(t, set("").Error)
		require.NoError(t, set("tenant-a").Error)
		aIs(t, "a-old", "a-note")
		bUntouched(t)
	})

	t.Run("a destination of another type is checked through its column", func(t *testing.T) {
		seed(t)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidgetPatch{Tid: "tenant-b", Name: "x"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(&upWidgetPatch{Tid: "tenant-b", Name: "x"}).Error, ErrTenantMismatch)
		other := "tenant-b"
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidgetPtrPatch{Tid: &other, Name: "x"}).Error, ErrTenantMismatch)
		// A column held as anything but a string cannot be checked, even when it is unset.
		assert.ErrorContains(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidgetPtrPatch{Name: "x"}).Error, "plain string")
		assert.ErrorContains(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidgetNullPatch{Name: "x"}).Error, "plain string")
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).Updates(upWidgetNullPatch{Tid: sql.NullString{String: "tenant-b", Valid: true}}).Error, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).Select("tenant_id", "name").Updates(upWidgetPatch{Name: "a-patch"}).Error)
		aIs(t, "a-patch", "a-note")
		bUntouched(t)
	})

	t.Run("UpdateColumn and UpdateColumns follow the same rule", func(t *testing.T) {
		seed(t)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).UpdateColumn("tenant_id", "tenant-b").Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).UpdateColumns(map[string]any{"tenant_id": "tenant-b"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, ctxA().Model(&upWidget{ID: 1}).UpdateColumns(upWidget{TenantScoped: TenantScoped{"tenant-b"}}).Error, ErrTenantMismatch)
		aIs(t, "a-old", "a-note")
		require.NoError(t, ctxA().Model(&upWidget{ID: 1}).UpdateColumns(map[string]any{"tenant_id": "", "name": "a-col"}).Error)
		aIs(t, "a-col", "a-note")
		bUntouched(t)
	})

	t.Run("the plain tenant spelling follows the same rule", func(t *testing.T) {
		upsertTruncate(t, db)
		require.NoError(t, db.WithContext(upCtxA).Create(&upPlain{Key: "k", Val: "a-old"}).Error)
		require.NoError(t, db.WithContext(upCtxB).Create(&upPlain{Key: "k", Val: "b-val"}).Error)
		key := func() *gorm.DB { return ctxA().Model(&upPlain{Tenant: "tenant-a", Key: "k"}) }
		assert.ErrorIs(t, key().Updates(upPlain{Tenant: "tenant-b", Val: "x"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, key().Updates(map[string]any{"tenant": "tenant-b"}).Error, ErrTenantMismatch)
		assert.ErrorIs(t, key().Update("Tenant", "tenant-b").Error, ErrTenantMismatch)
		require.NoError(t, key().Select("tenant", "val").Updates(upPlain{Val: "a-new"}).Error)
		var rows []upPlain
		require.NoError(t, db.WithContext(upSystem).Order("tenant").Find(&rows).Error)
		assert.Equal(t, []upPlain{{"tenant-a", "k", "a-new"}, {"tenant-b", "k", "b-val"}}, rows)
	})

	t.Run("a system context is exempt", func(t *testing.T) {
		seed(t)
		require.NoError(t, db.WithContext(upSystem).Save(&upWidget{ID: 1, TenantScoped: TenantScoped{"tenant-b"}, Name: "moved"}).Error)
		got, _ := upWidgetRow(t, db, 1)
		assert.Equal(t, "tenant-b", got.TenantId)
	})
}

func TestTenantUpdateMatrixSQLite(t *testing.T) {
	runTenantUpdateMatrix(t, newUpsertDB(t))
}

// A partial Updates writes only what it was given: the stamp must not add the tenant column
// to a statement that never wrote it.
func TestTenantUpdateStampOnlyTouchesAColumnBeingWritten(t *testing.T) {
	db := newUpsertDB(t)
	sql := db.WithContext(upCtxA).Session(&gorm.Session{DryRun: true}).
		Model(&upWidget{ID: 1}).Updates(upWidget{Name: "n"}).Statement.SQL.String()
	assert.NotContains(t, sql, "`tenant_id`=")
	sql = db.WithContext(upCtxA).Session(&gorm.Session{DryRun: true}).
		Save(&upWidget{ID: 1, Name: "n"}).Statement.SQL.String()
	assert.Contains(t, sql, "`tenant_id`=")
}
