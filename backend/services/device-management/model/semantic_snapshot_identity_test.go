// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/require"
)

// The semantic layer adds fields to the frozen profile snapshot (the metric bindings, the
// pack versions) and to asset-type versions. Every one is omitempty, and that is the whole
// of the compatibility claim: a stored snapshot from before the layer must read and
// re-serialise byte for byte, an unbound draft published today must store exactly what the
// pre-change binary stored, and the Thing Model export must not move by default.
//
// The fixture holds what the PRE-CHANGE binary stored (see snapshot_identity_seed_test.go),
// so these tests compare today's code against real earlier bytes rather than against
// themselves.

func loadPreSemanticFixture(t *testing.T) preSemanticFixtureDoc {
	t.Helper()
	raw, err := os.ReadFile(preSemanticFixture)
	require.NoError(t, err)
	var doc preSemanticFixtureDoc
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Len(t, doc.ProfileVersions, 3)
	require.Len(t, doc.AssetTypeVersions, 2)
	return doc
}

var updatedAtField = regexp.MustCompile(`"UpdatedAt":"[^"]*"`)

// maskUpdatedAt blanks the row-version timestamps inside a snapshot. A definition edit now
// advances updated_at strictly past the version it read (rdb.AdvancingFrom), so under this
// fixture's frozen clock the edited metric stores one microsecond later than the pre-change
// binary did. The timestamp is clock data; what this test pins is the snapshot FORMAT.
func maskUpdatedAt(s string) string { return updatedAtField.ReplaceAllString(s, `"UpdatedAt":"-"`) }

// The same history, replayed by today's code with nothing bound, stores the same bytes.
func TestAnUnboundPublishStoresThePreChangeSnapshotBytes(t *testing.T) {
	want := loadPreSemanticFixture(t)
	api, ctx := identityApi(t, &SemanticContext{})
	seedIdentityHistory(t, api, ctx)
	got := readIdentityHistory(t, api, ctx)

	for i, v := range want.ProfileVersions {
		require.Equal(t, maskUpdatedAt(v.Snapshot), maskUpdatedAt(got.ProfileVersions[i].Snapshot), "profile version %d snapshot moved", v.Version)
		require.Equal(t, v.ThingModel, got.ProfileVersions[i].ThingModel, "profile version %d Thing Model moved", v.Version)
	}
	for i, v := range want.AssetTypeVersions {
		require.Equal(t, v.PropertySchema, got.AssetTypeVersions[i].PropertySchema, "asset type version %d moved", v.Version)
	}
	var stored []AssetTypeVersion
	require.NoError(t, api.RDB.DB(ctx).Find(&stored).Error)
	for _, v := range stored {
		require.Nil(t, v.SemanticTypes, "an unbound publish froze a binding")
		require.Nil(t, v.SemanticPackVersions, "an unbound publish recorded pack versions")
	}
}

// A stored pre-change snapshot decodes into today's types and re-serialises byte for byte,
// and today's Thing Model of it, by default, is the one the pre-change binary rendered.
func TestAPreChangeSnapshotReserialisesAndRendersUnchanged(t *testing.T) {
	doc := loadPreSemanticFixture(t)
	profile := &DeviceProfile{}
	profile.Token = "plant"
	name, desc := "Plant room", "A plant room controller"
	profile.Name.String, profile.Name.Valid = name, true
	profile.Description.String, profile.Description.Valid = desc, true

	for _, v := range doc.ProfileVersions {
		var snap ProfileSnapshot
		require.NoError(t, json.Unmarshal([]byte(v.Snapshot), &snap))
		again, err := json.Marshal(snap)
		require.NoError(t, err)
		require.Equal(t, v.Snapshot, string(again), "version %d does not re-serialise byte for byte", v.Version)
		require.NotContains(t, string(again), "Semantic", "a pre-change snapshot grew a semantic key")

		version := &DeviceProfileVersion{Version: v.Version, Snapshot: []byte(v.Snapshot)}
		tm, err := ThingModelOf(profile, version)
		require.NoError(t, err)
		require.Equal(t, v.ThingModel, string(tm), "version %d default Thing Model moved", v.Version)
		tm, err = ThingModelWith(profile, version, false)
		require.NoError(t, err)
		require.Equal(t, v.ThingModel, string(tm), "version %d Thing Model with the opt-in off moved", v.Version)
	}
}

// With the opt-in on, a bound metric's semantic property and point class join its descriptor
// in @type, in the fixed order [descriptor, property, point class] and without repeats;
// with it off the descriptor is all there is.
func TestTheThingModelOptInAddsTheSemanticProperty(t *testing.T) {
	api, ctx := identityApi(t, &SemanticContext{}, &AreaType{}, &CustomerType{}, &EntityRelationshipType{})
	enable(t, api, ctx, "brick", "sdm-streetlighting")
	seedIdentityHistory(t, api, ctx)
	_, err := api.UpdateMetricDefinition(ctx, "temp", &MetricDefinitionUpdateRequest{
		SemanticProperty:   dcgraphql.OptionalStringOf("brick:Dry_Bulb_Temperature"),
		SemanticPointClass: dcgraphql.OptionalStringOf("brick:Zone_Air_Temperature_Sensor")})
	require.NoError(t, err)
	_, err = api.UpdateMetricDefinition(ctx, "humidity_raw", &MetricDefinitionUpdateRequest{
		SemanticProperty: dcgraphql.OptionalStringOf("sdm-streetlighting:activePower")})
	require.NoError(t, err)
	v, err := api.PublishDeviceProfile(ctx, "plant", nil, nil, "tester")
	require.NoError(t, err)
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"plant"})
	require.NoError(t, err)

	props := func(include bool) map[string]map[string]any {
		raw, err := ThingModelWith(profiles[0], v, include)
		require.NoError(t, err)
		var tm struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(raw, &tm))
		return tm.Properties
	}
	off, on := props(false), props(true)
	require.Equal(t, "saref:Temperature", off["temp"]["@type"])
	require.NotContains(t, off["humidity_raw"], "@type")
	// [descriptor, observed property, point class]: the point class is the series' rdf:type.
	require.Equal(t, []any{"saref:Temperature", "https://brickschema.org/schema/Brick#Dry_Bulb_Temperature",
		"https://brickschema.org/schema/Brick#Zone_Air_Temperature_Sensor"}, on["temp"]["@type"])
	require.Equal(t, []any{"https://smartdatamodels.org/dataModel.Streetlighting/activePower"}, on["humidity_raw"]["@type"])
	require.NotContains(t, on["mode"], "@type", "an unbound metric with no descriptor carries no @type")

	// The default rendering of the bound version still never mentions the binding.
	raw, err := ThingModelOf(profiles[0], v)
	require.NoError(t, err)
	require.False(t, strings.Contains(string(raw), "Brick#Dry_Bulb_Temperature"))
}
