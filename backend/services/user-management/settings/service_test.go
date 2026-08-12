// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package settings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// This package is the generic store, so its tests use a SYNTHETIC registry rather
// than the shipped one (settingsdefs). That is not just isolation from a moving
// target: a test that reaches for a real key ends up asserting store mechanics
// through that key's validation rules, and then breaks — misleadingly — the day
// those rules change. The shipped keys are exercised in settingsdefs' own tests.
const (
	keyOpaque    = "test.opaque"
	keyValidated = "test.validated"
)

// mustBeObject is the synthetic key's rule: the value has to be a JSON object.
// Deliberately trivial and easy to violate from a test.
func mustBeObject(value json.RawMessage) error {
	var m map[string]any
	if err := json.Unmarshal(value, &m); err != nil || m == nil {
		return errors.New("value must be a JSON object")
	}
	return nil
}

func testRegistry() *Registry {
	return NewRegistry(
		Define(keyOpaque, json.RawMessage(`{"default":"{slug}"}`), "an opaque key", OpaqueJSON),
		Define(keyValidated, json.RawMessage(`{"ok":true}`), "a validated key", mustBeObject),
	)
}

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
	return NewService(NewStore(&rdb.RdbManager{Database: db}), testRegistry())
}

func TestListReturnsCodeDefaults(t *testing.T) {
	svc := newTestService(t)
	list, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 2)

	opaque := findEffective(t, list, keyOpaque)
	assert.False(t, opaque.Overridden, "an untouched setting is not overridden")
	assert.JSONEq(t, `{"default":"{slug}"}`, string(opaque.Value))
	assert.Nil(t, opaque.UpdatedAt)
	assert.Empty(t, opaque.UpdatedBy)
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

	eff, err := svc.Set(ctx, keyOpaque, override, "superuser@devicechain.local")
	require.NoError(t, err)
	assert.True(t, eff.Overridden)
	assert.JSONEq(t, string(override), string(eff.Value))
	require.NotNil(t, eff.UpdatedAt)
	assert.Equal(t, "superuser@devicechain.local", eff.UpdatedBy)

	// The override is visible via a fresh read.
	got, err := svc.Get(ctx, keyOpaque)
	require.NoError(t, err)
	assert.True(t, got.Overridden)
	assert.JSONEq(t, string(override), string(got.Value))

	// Clearing reverts to the code default.
	reverted, err := svc.Clear(ctx, keyOpaque)
	require.NoError(t, err)
	assert.False(t, reverted.Overridden)
	assert.JSONEq(t, `{"default":"{slug}"}`, string(reverted.Value))
	assert.Empty(t, reverted.UpdatedBy)
}

func TestSetIsUpsert(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, err := svc.Set(ctx, keyOpaque, []byte(`{"a":"1"}`), "alice")
	require.NoError(t, err)
	eff, err := svc.Set(ctx, keyOpaque, []byte(`{"b":"2"}`), "bob")
	require.NoError(t, err)
	assert.JSONEq(t, `{"b":"2"}`, string(eff.Value))
	assert.Equal(t, "bob", eff.UpdatedBy)

	// Exactly one row persists for the key.
	list, err := svc.List(ctx)
	require.NoError(t, err)
	assert.True(t, findEffective(t, list, keyOpaque).Overridden)
}

func TestSetUnknownKeyRejected(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Set(context.Background(), "no.such.key", []byte(`{}`), "x")
	assert.ErrorIs(t, err, ErrUnknownSetting)
}

func TestSetInvalidJSONRejected(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Set(context.Background(), keyOpaque, []byte(`not json`), "x")
	assert.ErrorIs(t, err, ErrInvalidValue)
}

func TestSetOversizedValueRejected(t *testing.T) {
	svc := newTestService(t)
	// A valid-JSON string literal just over the cap.
	big := make([]byte, MaxValueBytes+2)
	big[0], big[len(big)-1] = '"', '"'
	for i := 1; i < len(big)-1; i++ {
		big[i] = 'a'
	}
	_, err := svc.Set(context.Background(), keyOpaque, big, "x")
	assert.ErrorIs(t, err, ErrValueTooLarge)
}

// The point of the whole arrangement: a value that is perfectly good JSON but
// wrong for its key does not reach the store, and nothing is written.
func TestSetRunsTheKeysValidator(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Set(ctx, keyValidated, []byte(`"a string, not an object"`), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a JSON object")

	// Nothing was persisted — a rejected write must not leave a half-applied row.
	got, err := svc.Get(ctx, keyValidated)
	require.NoError(t, err)
	assert.False(t, got.Overridden, "a rejected write must not create an override row")

	// The counterweight: a legal value for the same key still goes through, so the
	// validator is refusing the value rather than the key.
	_, err = svc.Set(ctx, keyValidated, []byte(`{"fine":1}`), "x")
	require.NoError(t, err)
}

// The opaque key accepts what the validated one refuses. Without this, a
// validator wired to run on EVERY key regardless of definition would pass every
// test above.
func TestOpaqueKeyAcceptsAnyJSON(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Set(context.Background(), keyOpaque, []byte(`"a bare string"`), "x")
	assert.NoError(t, err)
}

func TestDefineRejectsAMissingValidator(t *testing.T) {
	// Omitting the argument entirely does not compile, which is the real guard.
	// Passing nil explicitly is the way around it, so that panics.
	assert.PanicsWithValue(t,
		`settings.Define: "k" needs a validator; pass settings.OpaqueJSON to state that it has no shape`,
		func() { Define("k", json.RawMessage(`{}`), "d", nil) })
}

// A Definition assembled as a bare struct literal bypasses Define, and its zero
// validator would otherwise read as "accept anything" — the exact failure this
// design removes. Fail closed instead.
func TestBareDefinitionFailsClosed(t *testing.T) {
	d := Definition{Key: "k", Default: json.RawMessage(`{}`)}
	assert.ErrorIs(t, d.Validate(json.RawMessage(`{}`)), ErrNoValidator)
}

func TestRegistryRejectsADuplicateKey(t *testing.T) {
	assert.Panics(t, func() {
		NewRegistry(
			Define("dupe", json.RawMessage(`{}`), "", OpaqueJSON),
			Define("dupe", json.RawMessage(`{}`), "", OpaqueJSON),
		)
	})
}

// A shipped default its own validator refuses is a real bug and a confusing one:
// the settings page renders the value, the operator edits one field, and the save
// fails for a reason that predates them.
func TestRegistryRejectsADefaultItsOwnValidatorRefuses(t *testing.T) {
	assert.Panics(t, func() {
		NewRegistry(Define("bad", json.RawMessage(`"not an object"`), "", mustBeObject))
	})
}

// A Service built without a registry must say so rather than reporting every key
// as unknown, which would read as missing data instead of missing wiring.
func TestServiceWithoutRegistry(t *testing.T) {
	svc := NewService(nil, nil)
	_, err := svc.List(context.Background())
	assert.ErrorIs(t, err, ErrNoRegistry)
	_, err = svc.Get(context.Background(), keyOpaque)
	assert.ErrorIs(t, err, ErrNoRegistry)
	_, err = svc.Set(context.Background(), keyOpaque, []byte(`{}`), "x")
	assert.ErrorIs(t, err, ErrNoRegistry)
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
