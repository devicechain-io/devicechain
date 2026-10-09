// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
