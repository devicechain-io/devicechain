// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestService spins up an in-memory sqlite database with the core callbacks
// registered (as production does) and the system_settings table migrated, then
// wraps it in the settings Service. SystemSetting carries no tenant or token, so
// it passes through both callbacks untouched — exercising that here is deliberate.
func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&SystemSetting{}))
	return NewService(NewStore(&rdb.RdbManager{Database: db}))
}

func TestListReturnsCodeDefaults(t *testing.T) {
	svc := newTestService(t)
	list, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, len(Definitions()))

	masks := findEffective(t, list, KeyTokenMasks)
	assert.False(t, masks.Overridden, "an untouched setting is not overridden")
	assert.JSONEq(t, `{"default":"{slug}"}`, string(masks.Value))
	assert.Nil(t, masks.UpdatedAt)
	assert.Empty(t, masks.UpdatedBy)
}

func TestGetUnknownKeyRejected(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Get(context.Background(), "no.such.key")
	assert.ErrorIs(t, err, ErrUnknownSetting)
}

func TestSetOverridesThenClearReverts(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	override := []byte(`{"device":"device-{alphanumeric-8}","default":"{slug}"}`)

	eff, err := svc.Set(ctx, KeyTokenMasks, override, "superuser@devicechain.local")
	require.NoError(t, err)
	assert.True(t, eff.Overridden)
	assert.JSONEq(t, string(override), string(eff.Value))
	require.NotNil(t, eff.UpdatedAt)
	assert.Equal(t, "superuser@devicechain.local", eff.UpdatedBy)

	// The override is visible via a fresh read.
	got, err := svc.Get(ctx, KeyTokenMasks)
	require.NoError(t, err)
	assert.True(t, got.Overridden)
	assert.JSONEq(t, string(override), string(got.Value))

	// Clearing reverts to the code default.
	reverted, err := svc.Clear(ctx, KeyTokenMasks)
	require.NoError(t, err)
	assert.False(t, reverted.Overridden)
	assert.JSONEq(t, `{"default":"{slug}"}`, string(reverted.Value))
	assert.Empty(t, reverted.UpdatedBy)
}

func TestSetIsUpsert(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Set(ctx, KeyTokenMasks, []byte(`{"a":"1"}`), "alice")
	require.NoError(t, err)
	eff, err := svc.Set(ctx, KeyTokenMasks, []byte(`{"b":"2"}`), "bob")
	require.NoError(t, err)
	assert.JSONEq(t, `{"b":"2"}`, string(eff.Value))
	assert.Equal(t, "bob", eff.UpdatedBy)

	// Exactly one row persists for the key.
	list, err := svc.List(ctx)
	require.NoError(t, err)
	assert.True(t, findEffective(t, list, KeyTokenMasks).Overridden)
}

func TestSetUnknownKeyRejected(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Set(context.Background(), "no.such.key", []byte(`{}`), "x")
	assert.ErrorIs(t, err, ErrUnknownSetting)
}

func TestSetInvalidJSONRejected(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Set(context.Background(), KeyTokenMasks, []byte(`not json`), "x")
	assert.ErrorIs(t, err, ErrInvalidValue)
}

func findEffective(t *testing.T, list []Effective, key string) Effective {
	t.Helper()
	for _, e := range list {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("setting %q not found in list", key)
	return Effective{}
}
