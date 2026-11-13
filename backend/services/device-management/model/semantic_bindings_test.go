// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/semantic"
	"github.com/devicechain-io/dc-microservice/semantic/packs"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The semantic layer's backend. Every test here runs the real write path against the real
// generated vocabulary packs; nothing stubs the validator, so a binding that passes here is
// one the shipped tables accept.

const (
	iriStreetlight = "https://smartdatamodels.org/dataModel.Streetlighting/Streetlight"
	iriActivePower = "https://smartdatamodels.org/dataModel.Streetlighting/activePower"
	iriFeeds       = "https://brickschema.org/schema/Brick#feeds"
	iriIsFedBy     = "https://brickschema.org/schema/Brick#isFedBy"
	iriLuminaire   = "https://brickschema.org/schema/Brick#Luminaire"

	iriStreetlightCabinet   = "https://smartdatamodels.org/dataModel.Streetlighting/StreetlightControlCabinet"
	iriActivePowerR         = "https://smartdatamodels.org/dataModel.Streetlighting/activePowerR"
	iriElectricalMeter      = "https://brickschema.org/schema/Brick#Electrical_Meter"
	iriPublicAdmin          = "https://saref.etsi.org/saref4city/PublicAdministration"
	iriDistrict             = "https://saref.etsi.org/saref4city/District"
	iriHasPart              = "https://brickschema.org/schema/Brick#hasPart"
	iriIsPartOf             = "https://brickschema.org/schema/Brick#isPartOf"
	iriZoneAirTempSensor    = "https://brickschema.org/schema/Brick#Zone_Air_Temperature_Sensor"
	iriAvgZoneAirTempSensor = "https://brickschema.org/schema/Brick#Average_Zone_Air_Temperature_Sensor"
)

// seedSemanticContext enables the packs the partial-update families bind from.
func seedSemanticContext(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	// Setting a context reads every binding column (refuseOrphaningContext), so the family's
	// database needs every table that holds one, not only the family's own.
	require.NoError(t, api.RDB.Database.WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&DeviceType{}, &AssetType{}, &AreaType{},
		&CustomerType{}, &EntityRelationshipType{}, &MetricDefinition{}))
	enable(t, api, ctx, "brick", "s4city", "sdm-streetlighting")
}

// nullPtrStr renders an optional string the way the harness reads a nullable column.
func nullPtrStr(v *string) string {
	if v == nil {
		return nullMarker
	}
	return *v
}

func semanticTestApi(t *testing.T) (*Api, context.Context) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&SemanticContext{}, &DeviceType{}, &AssetType{}, &AssetTypeVersion{},
		&Asset{}, &AreaType{}, &CustomerType{}, &EntityRelationshipType{}, &DeviceProfile{},
		&DeviceProfileVersion{}, &DeviceProfileActivation{}, &AutomationReviewPolicy{}, &AutomationReviewPolicyChange{}, &AutomationReviewBreakGlass{}, &AutomationReview{}, &MetricDefinition{}, &CommandDefinition{},
		&DetectionRule{}, &DetectionRuleScopeRef{}, &MaintenanceWindow{}))
	return NewApi(&rdb.RdbManager{Database: db}), core.WithTenant(context.Background(), "acme")
}

func enable(t *testing.T, api *Api, ctx context.Context, packs ...string) {
	t.Helper()
	_, err := api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: packs})
	require.NoError(t, err)
}

func strs(s ...string) *[]string { return &s }

// requireRefusal asserts err is an INVALID_ARGUMENT SemanticError naming term.
func requireRefusal(t *testing.T, err error, sentinel error, term string) *SemanticError {
	t.Helper()
	require.Error(t, err)
	var se *SemanticError
	require.True(t, errors.As(err, &se), "want a *SemanticError, got %T: %v", err, err)
	require.Equal(t, CodeInvalidArgument, se.Extensions()["code"])
	if sentinel != nil {
		require.ErrorIs(t, err, sentinel)
	}
	if term != "" {
		require.Equal(t, term, se.Extensions()["term"], "the refusal must name the term")
		require.Contains(t, err.Error(), term)
	}
	return se
}

// requireInUse asserts err is the SEMANTIC_CONTEXT_IN_USE refusal carrying references.
func requireInUse(t *testing.T, err error, references int) *SemanticError {
	t.Helper()
	require.Error(t, err)
	var se *SemanticError
	require.True(t, errors.As(err, &se), "want a *SemanticError, got %T: %v", err, err)
	ext := se.Extensions()
	require.Equal(t, CodeSemanticContextInUse, ext["code"])
	require.Equal(t, references, ext["references"])
	require.Equal(t, references, se.References)
	return se
}

func TestDeviceTypeSemanticTypesNeedTheEnabledPack(t *testing.T) {
	api, ctx := semanticTestApi(t)

	// No context at all: nothing is enabled, so nothing binds.
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "lamp",
		SemanticTypes: strs("sdm-streetlighting:Streetlight")})
	requireRefusal(t, err, semantic.ErrUnknownPrefix, "sdm-streetlighting:Streetlight")

	enable(t, api, ctx, "sdm-streetlighting")
	created, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "lamp",
		SemanticTypes: strs("sdm-streetlighting:Streetlight", iriStreetlight)})
	require.NoError(t, err)
	// Stored EXPANDED, and the same term written twice is stored once.
	require.Equal(t, rdb.Anchors{iriStreetlight}, created.SemanticTypes)

	got, err := api.DeviceTypesByToken(ctx, []string{"lamp"})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{iriStreetlight}, got[0].SemanticTypes, "read back as the expanded IRI")

	// Wrong kind, unknown term and over-limit are refused.
	_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "bad-kind",
		SemanticTypes: strs("sdm-streetlighting:activePower")})
	requireRefusal(t, err, semantic.ErrWrongKind, "sdm-streetlighting:activePower")
	_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "unknown",
		SemanticTypes: strs("sdm-streetlighting:NoSuchThing")})
	requireRefusal(t, err, semantic.ErrUnknownTerm, "sdm-streetlighting:NoSuchThing")
	many := make([]string, MaxSemanticTypes+1)
	for i := range many {
		many[i] = "sdm-streetlighting:Streetlight"
	}
	_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "many", SemanticTypes: &many})
	requireRefusal(t, err, semantic.ErrLimit, "")

	// A refused create wrote nothing.
	all, err := api.DeviceTypesByToken(ctx, []string{"bad-kind", "unknown", "many"})
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestSemanticTypesPartialUpdate(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "sdm-streetlighting", "brick")
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "lamp", Name: strp("Lamp"),
		SemanticTypes: strs("sdm-streetlighting:Streetlight")})
	require.NoError(t, err)

	reload := func() *DeviceType {
		got, err := api.DeviceTypesByToken(ctx, []string{"lamp"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		return got[0]
	}

	// Omitted: left alone.
	_, err = api.UpdateDeviceType(ctx, "lamp", &DeviceTypeUpdateRequest{Name: dcgraphql.OptionalStringOf("Renamed")})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{iriStreetlight}, reload().SemanticTypes)

	// A value: replaces.
	_, err = api.UpdateDeviceType(ctx, "lamp", &DeviceTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Luminaire"}}})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{iriLuminaire}, reload().SemanticTypes)

	// A refused value changes nothing.
	_, err = api.UpdateDeviceType(ctx, "lamp", &DeviceTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Nope"}}})
	requireRefusal(t, err, semantic.ErrUnknownTerm, "brick:Nope")
	require.Equal(t, rdb.Anchors{iriLuminaire}, reload().SemanticTypes)

	// Null: clears.
	_, err = api.UpdateDeviceType(ctx, "lamp", &DeviceTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true}})
	require.NoError(t, err)
	require.Empty(t, reload().SemanticTypes)
	require.Equal(t, "Renamed", reload().Name.String)
}

func TestEveryTypeFamilyBinds(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city")

	_, err := api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "floor", SemanticTypes: strs("s4city:District")})
	require.NoError(t, err)
	_, err = api.CreateCustomerType(ctx, &CustomerTypeCreateRequest{Token: "org", SemanticTypes: strs("s4city:PublicAdministration")})
	require.NoError(t, err)
	_, err = api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "meter", SemanticTypes: strs("brick:Electrical_Meter")})
	require.NoError(t, err)

	at, err := api.AreaTypesByToken(ctx, []string{"floor"})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"https://saref.etsi.org/saref4city/District"}, at[0].SemanticTypes)

	_, err = api.UpdateAreaType(ctx, "floor", &AreaTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Nope"}}})
	requireRefusal(t, err, semantic.ErrUnknownTerm, "brick:Nope")
	_, err = api.UpdateCustomerType(ctx, "org", &CustomerTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Nope"}}})
	requireRefusal(t, err, semantic.ErrUnknownTerm, "brick:Nope")
	_, err = api.UpdateAssetType(ctx, "meter", &AssetTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Nope"}}})
	requireRefusal(t, err, semantic.ErrUnknownTerm, "brick:Nope")
}

func TestRemappingACustomPrefixDoesNotChangeAStoredBinding(t *testing.T) {
	api, ctx := semanticTestApi(t)
	const ns = "https://city.example/ontology#"
	_, err := api.SetSemanticContext(ctx, &SemanticContextRequest{
		CustomPrefixes: &[]CustomPrefixInput{{Prefix: "ex", Namespace: ns}}})
	require.NoError(t, err)
	_, err = api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "bin", SemanticTypes: strs("ex:SmartBin")})
	require.NoError(t, err)

	// The prefix is respelled, same namespace: allowed, and the binding is the same IRI.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{
		CustomPrefixes: &[]CustomPrefixInput{{Prefix: "city", Namespace: ns}}})
	require.NoError(t, err)
	got, err := api.DeviceTypesByToken(ctx, []string{"bin"})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{ns + "SmartBin"}, got[0].SemanticTypes)
	sc, err := api.SemanticContext(ctx)
	require.NoError(t, err)
	custom, err := sc.CustomPrefixMap()
	require.NoError(t, err)
	require.Equal(t, "city:SmartBin", SemanticCURIE(got[0].SemanticTypes[0], custom), "only the display follows the prefix")

	// Pointing the prefix at ANOTHER namespace would orphan the binding: refused.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{
		CustomPrefixes: &[]CustomPrefixInput{{Prefix: "city", Namespace: "https://other.example/#"}}})
	requireInUse(t, err, 1)
	require.Contains(t, err.Error(), "https://city.example/ontology#")
}

func TestDisablingAReferencedPackIsRefusedWithTheCount(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city", "sdm-streetlighting")
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "lamp", SemanticTypes: strs("sdm-streetlighting:Streetlight")})
	require.NoError(t, err)
	_, err = api.CreateAreaType(ctx, &AreaTypeCreateRequest{Token: "zone", SemanticTypes: strs("sdm-streetlighting:StreetlightGroup")})
	require.NoError(t, err)
	_, err = api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "fed",
		SemanticPredicate: strp("brick:feeds")})
	require.NoError(t, err)

	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"}})
	requireInUse(t, err, 2)
	require.Contains(t, err.Error(), "2 binding(s)")
	require.Contains(t, err.Error(), "sdm-streetlighting")

	// brick is referenced by the relationship (predicate and its defaulted inverse = 2 uses).
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"sdm-streetlighting"}})
	requireInUse(t, err, 2)

	// Unbind, then the pack can go.
	_, err = api.UpdateDeviceType(ctx, "lamp", &DeviceTypeUpdateRequest{SemanticTypes: dcgraphql.OptionalStringList{Set: true}})
	require.NoError(t, err)
	_, err = api.UpdateAreaType(ctx, "zone", &AreaTypeUpdateRequest{SemanticTypes: dcgraphql.OptionalStringList{Set: true}})
	require.NoError(t, err)
	sc, err := api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"}})
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"brick"}, sc.EnabledPacks)

	// An unshipped pack and a shadowing prefix are INVALID_ARGUMENT too.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"nonesuch"}})
	requireRefusal(t, err, semantic.ErrUnknownPack, "")
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"},
		CustomPrefixes: &[]CustomPrefixInput{{Prefix: "brick", Namespace: "https://x.example/#"}}})
	requireRefusal(t, err, semantic.ErrPrefixShadow, "brick")
}

func TestContextRecordsPackVersions(t *testing.T) {
	api, ctx := semanticTestApi(t)
	sc, err := api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick", "saref"}})
	require.NoError(t, err)
	versions, err := sc.PackVersionMap()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"brick": "1.4.4", "saref": "4.1.1"}, versions)

	// One row per tenant, however many times it is set.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"}})
	require.NoError(t, err)
	var n int64
	require.NoError(t, api.RDB.DB(ctx).Model(&SemanticContext{}).Count(&n).Error)
	require.EqualValues(t, 1, n)

	// Another tenant starts empty.
	other := core.WithTenant(context.Background(), "globex")
	got, err := api.SemanticContext(other)
	require.NoError(t, err)
	require.Empty(t, got.EnabledPacks)
}

func TestRelationshipTypePredicateAndInverse(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city", "sdm-streetlighting")

	// The inverse defaults to the pack's declared one, and the resolved term is stored.
	created, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "feeds",
		SemanticPredicate: strp("brick:feeds")})
	require.NoError(t, err)
	require.Equal(t, iriFeeds, created.SemanticPredicate.String)
	require.Equal(t, iriIsFedBy, created.SemanticInverse.String)

	// A class is not a predicate; an inverse needs a predicate.
	_, err = api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "bad",
		SemanticPredicate: strp("brick:Luminaire")})
	requireRefusal(t, err, semantic.ErrWrongKind, "brick:Luminaire")
	_, err = api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: "bad2",
		SemanticInverse: strp("brick:isFedBy")})
	require.Error(t, err)

	reload := func() *EntityRelationshipType {
		got, err := api.EntityRelationshipTypesByToken(ctx, []string{"feeds"})
		require.NoError(t, err)
		return got[0]
	}
	// Renaming leaves both bindings (and validates neither).
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		Name: dcgraphql.OptionalStringOf("Feeds")})
	require.NoError(t, err)
	require.Equal(t, iriFeeds, reload().SemanticPredicate.String)
	require.Equal(t, iriIsFedBy, reload().SemanticInverse.String)

	// Re-pointing the predicate re-derives the inverse rather than keeping the old one's.
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		SemanticPredicate: dcgraphql.OptionalStringOf("brick:isFedBy")})
	require.NoError(t, err)
	require.Equal(t, iriFeeds, reload().SemanticInverse.String)

	// An explicit null on the inverse clears just it.
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		SemanticInverse: dcgraphql.OptionalString{Set: true}})
	require.NoError(t, err)
	require.False(t, reload().SemanticInverse.Valid)
	require.True(t, reload().SemanticPredicate.Valid)

	// Nulling the predicate takes the inverse with it, and an inverse alone is then refused.
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		SemanticInverse: dcgraphql.OptionalStringOf("brick:feeds")})
	require.NoError(t, err)
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		SemanticPredicate: dcgraphql.OptionalString{Set: true}})
	require.NoError(t, err)
	require.False(t, reload().SemanticPredicate.Valid)
	require.False(t, reload().SemanticInverse.Valid)
	_, err = api.UpdateEntityRelationshipType(ctx, "feeds", &EntityRelationshipTypeUpdateRequest{
		SemanticInverse: dcgraphql.OptionalStringOf("brick:feeds")})
	require.Error(t, err)
}

func seedSemanticProfile(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	_, err := api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "cabinet"})
	require.NoError(t, err)
}

func TestMetricBindingsAndTheSnapshot(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city", "sdm-streetlighting")
	seedSemanticProfile(t, api, ctx)

	// Observed property must be an OBSERVABLE_PROPERTY; point class must be a brick:Point.
	req := func(token string) *MetricDefinitionCreateRequest {
		return &MetricDefinitionCreateRequest{Token: token, DeviceProfileToken: "cabinet",
			MetricKey: token, DataType: string(MetricDouble)}
	}
	r := req("power")
	r.SemanticProperty = strp("sdm-streetlighting:Streetlight")
	_, err := api.CreateMetricDefinition(ctx, r)
	requireRefusal(t, err, semantic.ErrWrongKind, "sdm-streetlighting:Streetlight")

	r = req("power")
	r.SemanticPointClass = strp("brick:Luminaire")
	_, err = api.CreateMetricDefinition(ctx, r)
	requireRefusal(t, err, semantic.ErrWrongKind, "brick:Luminaire")

	r = req("power")
	r.SemanticProperty = strp("sdm-streetlighting:activePower")
	r.SemanticPointClass = strp("brick:Zone_Air_Temperature_Sensor")
	made, err := api.CreateMetricDefinition(ctx, r)
	require.NoError(t, err)
	require.Equal(t, iriActivePower, *made.SemanticProperty)

	// An unbound metric alongside it.
	_, err = api.CreateMetricDefinition(ctx, req("plain"))
	require.NoError(t, err)

	// Partial update: omitted leaves, null clears, value sets.
	_, err = api.UpdateMetricDefinition(ctx, "power", &MetricDefinitionUpdateRequest{Name: dcgraphql.OptionalStringOf("P")})
	require.NoError(t, err)
	got, err := api.MetricDefinitionsByToken(ctx, []string{"power"})
	require.NoError(t, err)
	require.Equal(t, iriActivePower, *got[0].SemanticProperty)
	_, err = api.UpdateMetricDefinition(ctx, "power", &MetricDefinitionUpdateRequest{
		SemanticPointClass: dcgraphql.OptionalString{Set: true}})
	require.NoError(t, err)
	got, err = api.MetricDefinitionsByToken(ctx, []string{"power"})
	require.NoError(t, err)
	require.Nil(t, got[0].SemanticPointClass)
	require.NotNil(t, got[0].SemanticProperty)

	// The published snapshot freezes the binding and the pack versions it was validated against.
	v, err := api.PublishDeviceProfile(ctx, "cabinet", nil, nil, "tester")
	require.NoError(t, err)
	snap, err := parseProfileSnapshot(v.Snapshot)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"sdm-streetlighting": packs.SDMStreetlighting.Version}, snap.SemanticPackVersions)

	var wire struct {
		Metrics []map[string]any `json:"metrics"`
	}
	require.NoError(t, json.Unmarshal(v.Snapshot, &wire))
	for _, m := range wire.Metrics {
		if m["MetricKey"] == "plain" {
			for k := range m {
				require.False(t, strings.HasPrefix(k, "Semantic"), "an unbound metric must not carry %q", k)
			}
		}
		if m["MetricKey"] == "power" {
			require.Equal(t, iriActivePower, m["SemanticProperty"])
			require.NotContains(t, m, "SemanticPointClass")
		}
	}
}

func TestAssetTypeVersionFreezesAndRollbackRestoresTheBinding(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city", "sdm-streetlighting")
	_, err := api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "cabinet",
		PropertySchema: strp(`[]`), SemanticTypes: strs("sdm-streetlighting:StreetlightControlCabinet")})
	require.NoError(t, err)
	v1, err := api.PublishAssetType(ctx, "cabinet", nil, nil, "alice")
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"https://smartdatamodels.org/dataModel.Streetlighting/StreetlightControlCabinet"}, v1.SemanticTypes)
	frozen, err := DecodePackVersions(*v1.SemanticPackVersions)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"sdm-streetlighting": packs.SDMStreetlighting.Version}, frozen)

	// Rebind and publish v2.
	_, err = api.UpdateAssetType(ctx, "cabinet", &AssetTypeUpdateRequest{
		SemanticTypes: dcgraphql.OptionalStringList{Set: true, Value: []string{"brick:Electrical_Meter"}}})
	require.NoError(t, err)
	_, err = api.PublishAssetType(ctx, "cabinet", nil, nil, "alice")
	require.NoError(t, err)

	typ := func() *AssetType {
		got, err := api.AssetTypesByToken(ctx, []string{"cabinet"})
		require.NoError(t, err)
		return got[0]
	}
	require.Equal(t, rdb.Anchors{"https://brickschema.org/schema/Brick#Electrical_Meter"}, typ().SemanticTypes)

	// Rolling back to v1 restores v1's binding, and the draft edit does not move the pointer.
	_, err = api.RollbackAssetType(ctx, "cabinet", 1)
	require.NoError(t, err)
	require.Equal(t, v1.SemanticTypes, typ().SemanticTypes)
	require.EqualValues(t, 1, typ().ActiveVersion.Int32)

	// Rolling forward restores v2's.
	_, err = api.RollbackAssetType(ctx, "cabinet", 2)
	require.NoError(t, err)
	require.Equal(t, rdb.Anchors{"https://brickschema.org/schema/Brick#Electrical_Meter"}, typ().SemanticTypes)

	// A version that froze no binding restores none.
	_, err = api.UpdateAssetType(ctx, "cabinet", &AssetTypeUpdateRequest{SemanticTypes: dcgraphql.OptionalStringList{Set: true}})
	require.NoError(t, err)
	v3, err := api.PublishAssetType(ctx, "cabinet", nil, nil, "alice")
	require.NoError(t, err)
	require.Empty(t, v3.SemanticTypes)
	require.Nil(t, v3.SemanticPackVersions)
	_, err = api.RollbackAssetType(ctx, "cabinet", 1)
	require.NoError(t, err)
	_, err = api.RollbackAssetType(ctx, "cabinet", 3)
	require.NoError(t, err)
	require.Empty(t, typ().SemanticTypes)

	// A rollback whose frozen binding names a vocabulary the tenant no longer enables is refused.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"}})
	require.NoError(t, err) // typ() is unbound now, so sdm-streetlighting may go
	_, err = api.RollbackAssetType(ctx, "cabinet", 1)
	requireRefusal(t, err, semantic.ErrUnknownPrefix, "")
	require.Empty(t, typ().SemanticTypes, "a refused rollback restores nothing")
	require.EqualValues(t, 3, typ().ActiveVersion.Int32, "and does not move the pointer")
}

// A binding the context no longer explains (left behind by a context change that raced a
// binding write) makes the NEXT setSemanticContext refuse loudly with the true count, even
// though that change drops nothing: every bound IRI is checked against the next context,
// not just the packs a change drops.
func TestAnExistingOrphanBlocksTheNextContextChange(t *testing.T) {
	api, ctx := semanticTestApi(t)
	enable(t, api, ctx, "brick", "s4city")
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "lamp", SemanticTypes: strs("brick:Luminaire")})
	require.NoError(t, err)
	_, err = api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "meter", SemanticTypes: strs("brick:Electrical_Meter")})
	require.NoError(t, err)
	// Plant the orphan: the stored context stops enabling brick behind the bindings' back.
	require.NoError(t, api.RDB.DB(ctx).Model(&SemanticContext{}).Where("1 = 1").
		Update("enabled_packs", rdb.Anchors{"s4city"}).Error)

	// Adding a pack drops nothing, and is still refused: two bindings name brick.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"s4city", "saref"}})
	se := requireInUse(t, err, 2)
	require.Contains(t, se.Error(), `"brick"`)

	// Re-enabling the vocabulary the orphans need is accepted.
	_, err = api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick", "s4city"}})
	require.NoError(t, err)
}

// Two FIRST setSemanticContext calls for one tenant race to insert its one live row; the
// loser hits the unique index and is answered CONFLICT (retry), not a raw driver error.
func TestAConcurrentFirstSetIsAConflict(t *testing.T) {
	api, ctx := semanticTestApi(t)
	sys := api.RDB.Database.WithContext(core.WithSystemContext(context.Background()))
	require.NoError(t, sys.Exec(`CREATE UNIQUE INDEX uix_semantic_contexts_tenant ON semantic_contexts (tenant_id) WHERE deleted_at IS NULL`).Error)
	// The rival commits between this request's read (no row) and its insert.
	const cb = "test:rival-first-set"
	require.NoError(t, api.RDB.Database.Callback().Create().Before("gorm:create").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "semantic_contexts" {
			tx.Session(&gorm.Session{NewDB: true}).WithContext(core.WithSystemContext(context.Background())).
				Exec(`INSERT INTO semantic_contexts (tenant_id, enabled_packs, custom_prefixes, pack_versions) VALUES ('acme', '[]', '{}', '{}')`)
		}
	}))
	t.Cleanup(func() { _ = api.RDB.Database.Callback().Create().Remove(cb) })

	_, err := api.SetSemanticContext(ctx, &SemanticContextRequest{EnabledPacks: []string{"brick"}})
	require.Error(t, err)
	var ce *conflict.Error
	require.True(t, errors.As(err, &ce), "want a *conflict.Error, got %T: %v", err, err)
	require.Equal(t, conflict.Code, ce.Extensions()["code"])
	require.Contains(t, err.Error(), "semantic context")
}
