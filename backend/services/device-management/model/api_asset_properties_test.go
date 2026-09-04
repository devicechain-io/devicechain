// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// assetPropertyTestApi builds the sqlite-backed Api these tests run against.
//
// 🔴 FOREIGN KEYS ON, AND THAT IS A MEASUREMENT RATHER THAN TIDINESS. sqlite
// enforces no foreign key unless asked, and most fixtures in this package leave it
// off — so a cascade that forgets a child table passes here and fails on Postgres
// with a raw constraint error. asset_type_versions carries a foreign key to
// asset_types, so with enforcement off, a DeleteAssetType that did not remove the
// versions would pass every test and leave any PUBLISHED asset type permanently
// undeletable in production. That is exactly the shape device_replacements hit.
//
// It has to be set AFTER AutoMigrate: gorm creates tables in dependency order, and
// turning enforcement on first would make an ordering slip a migrate failure rather
// than the thing under test.
//
// The token grammar is registered for the same reason the hierarchy harness does it:
// it is registered unconditionally in production, so a fixture without it is more
// permissive than the thing it is standing in for.
func assetPropertyTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, rdb.RegisterTokenGrammar(db), "register token grammar")
	require.NoError(t, db.AutoMigrate(&AssetType{}, &Asset{}, &AssetTypeVersion{}), "migrate")

	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error, "enable foreign keys")
	var fk int
	require.NoError(t, db.Raw(`PRAGMA foreign_keys`).Scan(&fk).Error, "read foreign_keys pragma")
	require.Equal(t, 1, fk,
		"foreign keys are still off; this fixture would not see a missing cascade")

	api := NewApi(&rdb.RdbManager{Database: db})
	return api, core.WithTenant(context.Background(), "acme")
}

// seedTypeWithSchema creates an asset type carrying the given draft schema, and
// publishes it when publish is true.
func seedTypeWithSchema(t *testing.T, api *Api, ctx context.Context,
	token, schema string, publish bool) *AssetType {
	t.Helper()

	req := &AssetTypeCreateRequest{Token: token}
	if schema != "" {
		req.PropertySchema = strp(schema)
	}
	created, err := api.CreateAssetType(ctx, req)
	require.NoError(t, err, "create asset type %q", token)
	if publish {
		_, err := api.PublishAssetType(ctx, token, nil, nil, "tester")
		require.NoError(t, err, "publish asset type %q", token)
		reloaded, err := api.assetTypeByToken(ctx, token)
		require.NoError(t, err)
		return reloaded
	}
	return created
}

// ---------------------------------------------------------------------------
// The declaration gate: is the contract itself coherent?
// ---------------------------------------------------------------------------

// A malformed or incoherent draft schema is refused when it is WRITTEN, not when
// something later tries to satisfy it. Each case names an input class rather than a
// spelling: a shape that is not an array, a key no descriptor declares, and the four
// ways a descriptor can declare something nothing could satisfy.
func TestAssetTypeDraftSchemaIsValidatedOnWrite(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{"not an array", `{"name":"vendor","dataType":"STRING"}`},
		{"not JSON at all", `[{`},
		{"unknown constraint key", `[{"name":"psi","dataType":"INT","maximum":10}]`},
		{"empty property name", `[{"dataType":"STRING"}]`},
		{"duplicate property name", `[{"name":"v","dataType":"STRING"},{"name":"v","dataType":"INT"}]`},
		{"unknown data type", `[{"name":"v","dataType":"WIDGET"}]`},
		{"bounds on a non-numeric type", `[{"name":"v","dataType":"STRING","minValue":0}]`},
		{"min above max", `[{"name":"v","dataType":"INT","minValue":10,"maxValue":1}]`},
		{"default outside its own enum", `[{"name":"v","dataType":"STRING","enum":["a"],"default":"b"}]`},
		{"object property declaring scalar constraints",
			`[{"name":"v","kind":"OBJECT","dataType":"STRING","parameters":[]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := assetPropertyTestApi(t)
			_, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{
				Token: "pump", PropertySchema: strp(tc.schema),
			})
			require.Error(t, err, "a schema that %s must be refused", tc.name)

			// The refusal is TOTAL: nothing was created. A create that stored the row and
			// only skipped the schema would leave an asset type nobody asked for.
			rows, readErr := api.AssetTypesByToken(ctx, []string{"pump"})
			require.NoError(t, readErr)
			require.Empty(t, rows, "a refused create must write nothing at all")
		})
	}
}

// The counterweight to the case above: a well-formed contract, including every
// constraint the descriptor can carry, is stored untouched. Rejecting bad schemas is
// only safe while good ones still pass.
func TestAssetTypeDraftSchemaAcceptsAWellFormedContract(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	schema := `[{"name":"vendor","dataType":"STRING","required":true,"enum":["Acme","Northwind"]},` +
		`{"name":"psi","dataType":"INT","minValue":0,"maxValue":300,"unit":"psi","default":"100"},` +
		`{"name":"site","kind":"OBJECT","parameters":[{"name":"bay","dataType":"STRING"}]}]`

	created, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{
		Token: "pump", PropertySchema: strp(schema),
	})
	require.NoError(t, err)
	require.NotNil(t, created.PropertySchema)
	require.JSONEq(t, schema, string(*created.PropertySchema))
	require.False(t, created.ActiveVersion.Valid, "a draft is not published by writing it")
}

// ---------------------------------------------------------------------------
// Publish / rollback
// ---------------------------------------------------------------------------

// Publishing freezes the draft, advances the pointer, and leaves the draft alone —
// so a later draft edit does not retroactively change what a published version says.
func TestPublishAssetTypeFreezesTheDraft(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"vendor","dataType":"STRING"}]`, false)

	v1, err := api.PublishAssetType(ctx, "pump", strp("first"), strp("initial contract"), "alice")
	require.NoError(t, err)
	require.EqualValues(t, 1, v1.Version)
	require.Equal(t, "alice", v1.PublishedBy)
	require.JSONEq(t, `[{"name":"vendor","dataType":"STRING"}]`, string(v1.PropertySchema))

	// Edit the draft. The frozen version must not move with it.
	_, err = api.UpdateAssetType(ctx, "pump", &AssetTypeUpdateRequest{
		PropertySchema: dcgraphql.OptionalStringOf(`[{"name":"psi","dataType":"INT"}]`),
	})
	require.NoError(t, err)

	versions, err := api.AssetTypeVersions(ctx, "pump")
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.JSONEq(t, `[{"name":"vendor","dataType":"STRING"}]`, string(versions[0].PropertySchema),
		"a draft edit rewrote a frozen version")

	// And the pointer still names it, because an edit is not a publish.
	at, err := api.assetTypeByToken(ctx, "pump")
	require.NoError(t, err)
	require.True(t, at.ActiveVersion.Valid)
	require.EqualValues(t, 1, at.ActiveVersion.Int32)
}

// A type with no draft contract has nothing to freeze, and refusing is the whole of
// what stops a publish from inventing an empty contract that would REFUSE properties
// where none had been declared.
func TestPublishAssetTypeRefusesATypeWithNoDraft(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", "", false)

	_, err := api.PublishAssetType(ctx, "pump", nil, nil, "alice")
	require.Error(t, err)

	versions, err := api.AssetTypeVersions(ctx, "pump")
	require.NoError(t, err)
	require.Empty(t, versions)
}

// An EMPTY contract is a different statement from no contract, and it publishes.
// This is the input class the "nothing to freeze" refusal must not swallow.
func TestPublishAssetTypeAcceptsAnEmptyContract(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[]`, false)

	v, err := api.PublishAssetType(ctx, "pump", nil, nil, "alice")
	require.NoError(t, err)
	require.JSONEq(t, `[]`, string(v.PropertySchema))

	// And it BINDS: an asset of this type may now carry nothing.
	_, err = api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{"vendor":"Acme"}`),
	})
	require.Error(t, err, "an empty published contract must refuse every property")
}

// Version numbers are monotonic per type and each publish appends rather than
// replacing.
func TestPublishAssetTypeAppendsMonotonicVersions(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"a","dataType":"STRING"}]`, true)

	_, err := api.UpdateAssetType(ctx, "pump", &AssetTypeUpdateRequest{
		PropertySchema: dcgraphql.OptionalStringOf(`[{"name":"b","dataType":"STRING"}]`),
	})
	require.NoError(t, err)
	v2, err := api.PublishAssetType(ctx, "pump", nil, nil, "alice")
	require.NoError(t, err)
	require.EqualValues(t, 2, v2.Version)

	versions, err := api.AssetTypeVersions(ctx, "pump")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.EqualValues(t, 2, versions[0].Version, "versions list newest first")
	require.EqualValues(t, 1, versions[1].Version)
}

// Rollback moves the pointer and changes what assets are validated against, without
// deleting a version or touching the draft — so it can be rolled forward again.
func TestRollbackAssetTypeRepointsWithoutDestroying(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"vendor","dataType":"STRING"}]`, true)
	_, err := api.UpdateAssetType(ctx, "pump", &AssetTypeUpdateRequest{
		PropertySchema: dcgraphql.OptionalStringOf(`[{"name":"psi","dataType":"INT"}]`),
	})
	require.NoError(t, err)
	_, err = api.PublishAssetType(ctx, "pump", nil, nil, "alice")
	require.NoError(t, err)

	// Under v2, "psi" is the declared property and "vendor" is not.
	_, err = api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{"psi":40}`),
	})
	require.NoError(t, err)

	rolled, err := api.RollbackAssetType(ctx, "pump", 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, rolled.ActiveVersion.Int32)

	// The draft is untouched by the rollback.
	require.JSONEq(t, `[{"name":"psi","dataType":"INT"}]`, string(*rolled.PropertySchema))

	// Both versions still exist, so it rolls forward again.
	versions, err := api.AssetTypeVersions(ctx, "pump")
	require.NoError(t, err)
	require.Len(t, versions, 2)

	// And the ACTIVE contract is v1's: "vendor" is now the declared one.
	_, err = api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-2", AssetTypeToken: "pump", Properties: strp(`{"vendor":"Acme"}`),
	})
	require.NoError(t, err, "after rollback, v1's contract must be the one enforced")
	_, err = api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-3", AssetTypeToken: "pump", Properties: strp(`{"psi":40}`),
	})
	require.Error(t, err, "v2's vocabulary must stop being accepted after the rollback")
}

// Rolling back to a version that does not exist is a not-found, and it must not move
// the pointer on its way to saying so.
func TestRollbackAssetTypeRefusesAnUnknownVersion(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"vendor","dataType":"STRING"}]`, true)

	_, err := api.RollbackAssetType(ctx, "pump", 7)
	require.Error(t, err)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	at, err := api.assetTypeByToken(ctx, "pump")
	require.NoError(t, err)
	require.EqualValues(t, 1, at.ActiveVersion.Int32)
}

// A draft edit must not write the active-version pointer back from the value it
// loaded. This is the Omit("ActiveVersion") in UpdateAssetType, and without it an
// edit racing a publish silently reverts the contract every asset of the type is
// checked against.
//
// 🔴 THE INTERLEAVING IS THE TEST, AND AN EARLIER VERSION OF THIS TEST DID NOT
// PRODUCE IT. It loaded a "stale" copy of the type, published, and then called
// UpdateAssetType — which re-reads the row itself, so by the time Save ran the
// in-memory pointer was CURRENT and writing it back changed nothing. Mutation
// testing found that: deleting the Omit left every assertion here passing. The
// missing input class was not an assertion, it was a MOMENT — the publish has to
// land between UpdateAssetType's own read and its own write, which no sequence of
// API calls can arrange from outside.
//
// A gorm callback registered before the update is that moment, exactly and
// deterministically: it fires inside UpdateAssetType, after its load, before its
// Save. It advances the pointer with raw SQL, the way a concurrent publish would,
// and then the Save either preserves that (Omit present) or clobbers it back to the
// value the load saw (Omit absent).
func TestAssetTypeUpdateDoesNotWriteBackTheVersionPointer(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"vendor","dataType":"STRING"}]`, true)

	at, err := api.assetTypeByToken(ctx, "pump")
	require.NoError(t, err)
	require.EqualValues(t, 1, at.ActiveVersion.Int32, "the seed should have published version 1")

	// Fire once, on the first update that reaches the database.
	fired := false
	db := api.RDB.Database
	require.NoError(t, db.Callback().Update().Before("gorm:update").
		Register("test:concurrent_publish", func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "asset_types" {
				return
			}
			fired = true
			// Raw SQL on a fresh session so this is a write by someone else, not a
			// re-entrant use of the statement being built.
			if err := db.Exec(
				`UPDATE asset_types SET active_version = 2 WHERE token = ?`, "pump").Error; err != nil {
				t.Errorf("simulated concurrent publish failed: %v", err)
			}
		}), "register the concurrent-publish callback")
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove("test:concurrent_publish")
	})

	// An ordinary rename. It mentions nothing about versions.
	_, err = api.UpdateAssetType(ctx, "pump", &AssetTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Pump"),
	})
	require.NoError(t, err)
	require.True(t, fired, "the callback never ran; this test would pass vacuously")

	reloaded, err := api.assetTypeByToken(ctx, "pump")
	require.NoError(t, err)
	require.EqualValues(t, 2, reloaded.ActiveVersion.Int32,
		"a rename reverted the active version pointer a concurrent publish had advanced")
	// And the rename itself still landed — the Omit must skip one column, not the write.
	require.Equal(t, "Pump", reloaded.Name.String)
}

// ---------------------------------------------------------------------------
// The instance gate: does this document satisfy the published contract?
// ---------------------------------------------------------------------------

// The four-way case analysis validateAssetProperties is written around, each arm
// exercised by the input class it exists for.
func TestAssetPropertiesAgainstThePublishedContract(t *testing.T) {
	const schema = `[{"name":"vendor","dataType":"STRING","enum":["Acme","Northwind"]},` +
		`{"name":"psi","dataType":"INT","minValue":0,"maxValue":300}]`

	cases := []struct {
		name       string
		schemaJSON string
		publish    bool
		properties *string
		wantErr    bool
	}{
		{"absent document on an unpublished type", schema, false, nil, false},
		{"a document on an unpublished type", schema, false, strp(`{"vendor":"Acme"}`), true},
		{"a document on a type with no schema at all", "", false, strp(`{"vendor":"Acme"}`), true},
		{"a conforming document", schema, true, strp(`{"vendor":"Acme","psi":40}`), false},
		{"a partial document, nothing required", schema, true, strp(`{"psi":40}`), false},
		{"an empty document, nothing required", schema, true, strp(`{}`), false},
		{"an undeclared property", schema, true, strp(`{"colour":"red"}`), true},
		{"a value of the wrong type", schema, true, strp(`{"psi":"forty"}`), true},
		{"a value outside its bounds", schema, true, strp(`{"psi":400}`), true},
		{"a value outside its allowed values", schema, true, strp(`{"vendor":"Globex"}`), true},
		{"a document that is not an object", schema, true, strp(`["Acme"]`), true},
		{"an explicit null for an optional property", schema, true, strp(`{"psi":null}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := assetPropertyTestApi(t)
			seedTypeWithSchema(t, api, ctx, "pump", tc.schemaJSON, tc.publish)

			_, err := api.CreateAsset(ctx, &AssetCreateRequest{
				Token: "p-1", AssetTypeToken: "pump", Properties: tc.properties,
			})
			if tc.wantErr {
				require.Error(t, err)
				rows, readErr := api.AssetsByToken(ctx, []string{"p-1"})
				require.NoError(t, readErr)
				require.Empty(t, rows, "a refused create must write no asset at all")
				return
			}
			require.NoError(t, err)
		})
	}
}

// A required property is required: neither omitting it from the document nor omitting
// the document entirely satisfies it.
func TestAssetPropertiesRequiredMustBePresent(t *testing.T) {
	const schema = `[{"name":"serial","dataType":"STRING","required":true}]`

	for _, tc := range []struct {
		name       string
		properties *string
	}{
		{"omitted from the document", strp(`{}`)},
		{"the document omitted entirely", nil},
		{"sent as an explicit null", strp(`{"serial":null}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := assetPropertyTestApi(t)
			seedTypeWithSchema(t, api, ctx, "pump", schema, true)

			_, err := api.CreateAsset(ctx, &AssetCreateRequest{
				Token: "p-1", AssetTypeToken: "pump", Properties: tc.properties,
			})
			require.Error(t, err, "a required property must not be satisfiable by absence")
		})
	}
}

// A DEFAULT is an authoring hint, not an injected value: it does not satisfy a
// required property and it is not materialized into the stored document. The
// alternative — filling the value in at write — would freeze one version's
// declaration into data a later publish could no longer reach.
func TestAssetPropertiesRequiredIsNotSatisfiedByADefault(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump",
		`[{"name":"serial","dataType":"STRING","required":true,"default":"UNKNOWN"},`+
			`{"name":"psi","dataType":"INT","default":"100"}]`, true)

	_, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{}`),
	})
	require.Error(t, err, "a default must not satisfy a required property")

	created, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-2", AssetTypeToken: "pump", Properties: strp(`{"serial":"SN-1"}`),
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"serial":"SN-1"}`, string(*created.Properties),
		"the optional property's default was materialized into the stored document")
}

// A RETYPE re-validates the properties the asset already carries, even though the
// caller never mentioned them. Without this an asset moved to a type whose contract
// does not declare its properties would sit there conformant-when-written and
// silently not afterwards.
func TestAssetRetypeRevalidatesTheStoredProperties(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, true)
	seedTypeWithSchema(t, api, ctx, "valve", `[{"name":"bore","dataType":"INT"}]`, true)

	_, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{"psi":40}`),
	})
	require.NoError(t, err)

	_, err = api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		AssetTypeToken: dcgraphql.OptionalStringOf("valve"),
	})
	require.Error(t, err, "a retype must not strand properties the new type never declared")

	// The refusal is total: the asset still belongs to its old type.
	rows, err := api.AssetsByToken(ctx, []string{"p-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "pump", rows[0].AssetType.Token)

	// Retyping WITH a document the destination declares succeeds, which is what shows
	// the refusal above is about conformance rather than about retyping at all.
	_, err = api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		AssetTypeToken: dcgraphql.OptionalStringOf("valve"),
		Properties:     dcgraphql.OptionalStringOf(`{"bore":50}`),
	})
	require.NoError(t, err)
}

// Clearing the document is how an asset stops carrying properties, and it is allowed
// as long as the contract demands nothing.
func TestAssetPropertiesCanBeCleared(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, true)
	_, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{"psi":40}`),
	})
	require.NoError(t, err)

	updated, err := api.UpdateAsset(ctx, "p-1", &AssetUpdateRequest{
		Properties: dcgraphql.ClearedString(),
	})
	require.NoError(t, err)
	require.Nil(t, updated.Properties)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// A published asset type is deletable, and its version history goes with it. With
// foreign keys ON this is the test that would have failed had the cascade been left
// out — the delete would report a raw constraint error instead of succeeding.
func TestDeleteAssetTypeCascadesItsVersions(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	at := seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, true)

	deleted, err := api.DeleteAssetType(ctx, "pump")
	require.NoError(t, err, "a published asset type must still be deletable")
	require.True(t, deleted)

	var remaining int64
	require.NoError(t, api.RDB.DB(ctx).Unscoped().Model(&AssetTypeVersion{}).
		Where("asset_type_id = ?", at.ID).Count(&remaining).Error)
	require.Zero(t, remaining, "the version history outlived its asset type")
}

// The delete is still REFUSED while an asset references the type — the cascade above
// must not have turned the in-use check into a cascade of its own.
func TestDeleteAssetTypeStillRefusesWhileAssetsReferenceIt(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, true)
	_, err := api.CreateAsset(ctx, &AssetCreateRequest{Token: "p-1", AssetTypeToken: "pump"})
	require.NoError(t, err)

	_, err = api.DeleteAssetType(ctx, "pump")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrEntityInUse))

	versions, err := api.AssetTypeVersions(ctx, "pump")
	require.NoError(t, err)
	require.Len(t, versions, 1, "a refused delete must not have removed the history")
}

// ActiveAssetTypeVersion answers the contract an author is filling, and answers null
// — not an error — for a type that has simply never been published.
func TestActiveAssetTypeVersion(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, false)

	none, err := api.ActiveAssetTypeVersion(ctx, "pump")
	require.NoError(t, err)
	require.Nil(t, none)

	_, err = api.PublishAssetType(ctx, "pump", nil, nil, "alice")
	require.NoError(t, err)

	active, err := api.ActiveAssetTypeVersion(ctx, "pump")
	require.NoError(t, err)
	require.NotNil(t, active)
	require.EqualValues(t, 1, active.Version)
	require.JSONEq(t, `[{"name":"psi","dataType":"INT"}]`, string(active.PropertySchema))
}

// A pointer naming a version that does not exist FAILS CLOSED rather than resolving
// to "no contract". The profile equivalent logs and resolves empty; here an empty
// resolve is the branch that accepts anything, so the same leniency would turn a
// broken pointer into an open door.
//
// The pointer is corrupted with a direct write — the one door that skips the API —
// because no API path can produce this state, which is exactly why it needs a test.
func TestDanglingActiveVersionFailsClosed(t *testing.T) {
	api, ctx := assetPropertyTestApi(t)
	seedTypeWithSchema(t, api, ctx, "pump", `[{"name":"psi","dataType":"INT"}]`, true)
	require.NoError(t, api.RDB.DB(ctx).Model(&AssetType{}).Where("token = ?", "pump").
		Update("active_version", 99).Error)

	_, err := api.CreateAsset(ctx, &AssetCreateRequest{
		Token: "p-1", AssetTypeToken: "pump", Properties: strp(`{"psi":40}`),
	})
	require.Error(t, err, "a dangling active-version pointer must not accept everything")

	_, err = api.ActiveAssetTypeVersion(ctx, "pump")
	require.Error(t, err)
}
