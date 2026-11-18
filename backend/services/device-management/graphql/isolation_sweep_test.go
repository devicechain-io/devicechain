// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/test/isolationsweep"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// These operations read tenant-wide configuration, not customer-owned records.
// Catalogue rows are deliberately identical for scoped and unscoped callers, so no
// customer-hidden marker can honestly be planted in them.
func sweepCatalogueExceptions() map[string]string {
	return map[string]string{
		"query deviceTypes":                        "Device types are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query deviceTypes (all arguments)":        "Device type name filtering still reads the tenant-wide catalogue, with no customer-hidden rows.",
		"query deviceTypesById":                    "Device types are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query deviceTypesByToken":                 "Device types are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query metricDefinitions":                  "Metric definitions are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query metricDefinitions (all arguments)":  "Profile and metric-key filters still read tenant-wide configuration, with no customer-hidden rows.",
		"query metricDefinitionsById":              "Metric definitions are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query metricDefinitionsByToken":           "Metric definitions are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query deviceProfiles":                     "Profiles are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query deviceProfilesById":                 "Profiles are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query deviceProfilesByToken":              "Profiles are a tenant-wide catalogue (rdb.Catalogue), with no customer-hidden rows.",
		"query commandDefinitions":                 "Commands are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query commandDefinitions (all arguments)": "Profile and command-key filters still read tenant-wide configuration, with no customer-hidden rows.",
		"query commandDefinitionsById":             "Commands are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query commandDefinitionsByToken":          "Commands are tenant-wide profile configuration (rdb.Catalogue), with no customer-hidden rows.",
		"query areaTypesByToken":                   "Area type tokens are public catalogue identities; the device/place probe cannot name a customer-hidden type. Private type metadata is seeded and checked by the list and ID reads.",
		"query assetTypesByToken":                  "Asset type tokens are public catalogue identities; the device/place probe cannot name a customer-hidden type. Private type metadata is seeded and checked by the list and ID reads.",
		"query customerTypesByToken":               "Customer type tokens are public catalogue identities; the device/place probe cannot name a customer-hidden type. Private type metadata is seeded and checked by the list and ID reads.",
	}
}

// The group-scoped fixture already migrates these tables before seeding north.
// Other sweeps need them created while this is still a fresh schema.
func newSweepScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	f := newScopeFixture(t)
	require.NoError(t, f.db.AutoMigrate(&model.EntityGroup{}, &model.IssuedGroupToken{}, &model.EntityGroupVersion{}, &model.EntityGroupMembership{}))
	return f
}

// seedSweep gives the generated lookup arguments real rows to hide. The ID literal
// is shared by all ID-list queries, so every owned family has a hidden row at 50.
// Type metadata is private even though the type's identity is public configuration.
func (f scopeFixture) seedSweep(t *testing.T, probe string) {
	t.Helper()
	ctx := f.operator()
	private := datatypes.JSON(`{"note":"globex-private-note"}`)
	for _, row := range []any{
		&model.AreaType{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "sweep-private-type"}, MetadataEntity: rdb.MetadataEntity{Metadata: &private}},
		&model.AssetType{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "sweep-private-type"}, MetadataEntity: rdb.MetadataEntity{Metadata: &private}},
		&model.CustomerType{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "sweep-private-type"}, MetadataEntity: rdb.MetadataEntity{Metadata: &private}},
		&model.Device{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "globex-by-id"}, DeviceTypeId: 1},
		&model.Area{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "globex-by-id"}, AreaTypeId: 1},
		&model.Asset{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "globex-by-id"}, AssetTypeId: 1},
		&model.Customer{Model: gorm.Model{ID: 50}, TokenReference: rdb.TokenReference{Token: "globex-by-id"}, CustomerTypeId: 1},
	} {
		require.NoError(t, f.db.WithContext(ctx).Create(row).Error)
	}
	for _, edge := range []struct{ source, kind, target string }{
		{"globex-by-id", "customer", "globex"},
		{"globex-pump", "area", "globex-by-id"},
		{"globex-pump", "asset", "globex-by-id"},
	} {
		_, err := f.api.CreateEntityRelationship(ctx, &model.EntityRelationshipCreateRequest{
			Token: "sweep-" + edge.kind, SourceType: "device", Source: edge.source,
			TargetType: edge.kind, Target: edge.target, RelationshipType: "assigned"})
		require.NoError(t, err)
	}
	devices, err := f.api.DevicesByToken(ctx, []string{probe})
	require.NoError(t, err)
	if len(devices) == 0 {
		f.addDevice(t, probe, "globex")
	}
	// The by-external-ID argument is the probe, too.
	require.NoError(t, f.db.WithContext(ctx).Model(&model.Device{}).Where("1 = 1").Update("external_id", gorm.Expr("token")).Error)
	areas, err := f.api.AreasByToken(ctx, []string{probe})
	require.NoError(t, err)
	if len(areas) == 0 {
		_, err = f.api.CreateArea(ctx, &model.AreaCreateRequest{Token: probe, AreaTypeToken: "floor"})
		require.NoError(t, err)
		_, err = f.api.CreateEntityRelationship(ctx, &model.EntityRelationshipCreateRequest{
			Token: "sweep-probe-area", SourceType: "device", Source: "globex-pump",
			TargetType: "area", Target: probe, RelationshipType: "assigned"})
		require.NoError(t, err)
	}
	assets, err := f.api.AssetsByToken(ctx, []string{probe})
	require.NoError(t, err)
	if len(assets) == 0 {
		_, err = f.api.CreateAsset(ctx, &model.AssetCreateRequest{Token: probe, AssetTypeToken: "machine"})
		require.NoError(t, err)
		_, err = f.api.CreateEntityRelationship(ctx, &model.EntityRelationshipCreateRequest{
			Token: "sweep-probe-asset", SourceType: "device", Source: "globex-pump",
			TargetType: "asset", Target: probe, RelationshipType: "assigned"})
		require.NoError(t, err)
	}
	_, err = f.api.CreateCustomer(ctx, &model.CustomerCreateRequest{Token: probe, CustomerTypeToken: "retail"})
	require.NoError(t, err)
	_, err = f.api.CreateEntityGroup(ctx, &model.EntityGroupCreateRequest{Token: probe, MemberType: "device"})
	require.NoError(t, err)
}

// TestIsolationSweep calls every query, mutation and subscription device-management
// serves as acme's member, aimed at globex's device, and fails on any answer that carries
// globex's data or tells globex's device apart from a missing one (ADR-081). Its negative
// control is the same queries unscoped, which must surface globex.
func TestIsolationSweep(t *testing.T) {
	f := newSweepScopeFixture(t)
	f.seedSweep(t, "globex-pump")
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:        gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:        f.sweepMember("customer:acme"),
		Unscoped:      f.operator(),
		Probe:         "globex-pump",
		Hidden:        []string{"globex"},
		NothingSeeded: sweepCatalogueExceptions(),
		// The alarm reads and the alarm stream name their originator as a (type, token)
		// pair, and validate the state and severity filters before resolving it; with these
		// filled, the all-arguments documents aim the token at globex's device rather than
		// stopping at an unknown entity type or an invalid filter.
		Literals: map[string]string{"ids": `"50"`, "areaTypeToken": `"floor"`, "assetTypeToken": `"machine"`, "customerTypeToken": `"retail"`, "originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`},
	})
	counts := map[string]int{}
	for _, o := range out {
		switch {
		case o.Data:
			counts["data"]++
		case len(o.Codes) > 0:
			counts[o.Codes[0]]++
		default:
			counts["silent"]++
		}
	}
	t.Logf("swept %d operations as acme's member: %v", len(out), counts)
	// Review evidence and protection have no scope rule. These reads refuse before looking
	// up the requested profile or token and preserve the explicit actiongate code.
	for _, op := range []string{"query automationReview", "query automationReviews", "query automationReviewPolicy", "query automationReviewBreakGlass"} {
		if o, ok := isolationsweep.OutcomeOf(out, op); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "SCOPED_GRANT_NOT_SUPPORTED" {
			t.Errorf("%s: %+v (reached=%v), want SCOPED_GRANT_NOT_SUPPORTED", op, o, ok)
		}
	}
	// Policy writes must refuse scoped grants before the model sees a profile or token.
	for _, op := range []string{"mutation setAutomationReviewPolicy", "mutation approveAutomationReviewPolicyChange", "mutation cancelAutomationReviewPolicyChange"} {
		if o, ok := isolationsweep.OutcomeOf(out, op); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
			t.Errorf("%s: %+v (reached=%v), want FORBIDDEN", op, o, ok)
		}
	}
	for _, o := range out {
		if !o.Data && (len(o.Codes) == 0 || o.Codes[0] == "-") {
			t.Logf("  answered with no code: %s %v", o.Operation, o.Codes)
		}
	}
	// The sweep reached the opted-in reads, and they answered data rather than a refusal.
	for _, op := range []string{"query devices", "query customers", "query deviceTypes",
		"query areas", "query areasById", "query areasByToken", "query assets", "query assetsById", "query assetsByToken",
		"query areaTypes", "query assetTypes"} {
		o, ok := isolationsweep.OutcomeOf(out, op)
		if !ok || !o.Data {
			t.Errorf("%s: %+v (reached=%v), want data for acme's member", op, o, ok)
		}
	}
	// deviceScopeAnchors stays closed to a scoped member even though Device now admits one:
	// a device's anchors can name another customer sharing it, so the query is gated on
	// Authorize, not AuthorizeScoped. The sweep must reach it and see it refused.
	// customerDevices is opted in, but answers a scoped member only about a customer its
	// scope names; the sweep aims it at the probe, which names none, so it must be reached
	// and refused (its hidden-vs-missing twin is the sweep's own second call).
	if o, ok := isolationsweep.OutcomeOf(out, "query customerDevices"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query customerDevices: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// anchorDevices is opted in, but the sweep aims it at the probe's token with the probe as
	// the anchor KIND, which names no anchor a scoped member may ask about: reached, refused.
	if o, ok := isolationsweep.OutcomeOf(out, "query anchorDevices"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query anchorDevices: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// The relationship graph names co-owners: still closed, now with areas and assets open
	// beside it.
	if o, ok := isolationsweep.OutcomeOf(out, "query entityRelationships"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query entityRelationships: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	if o, ok := isolationsweep.OutcomeOf(out, "query deviceScopeAnchors"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query deviceScopeAnchors: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
	// deviceConstantsPage reads device METADATA (the numbers under the names asked for) for every
	// device of a profile. It is the rule preview's door, reached over a service token, so it is
	// gated on Authorize: a scoped member is refused before any profile or device is looked up.
	if o, ok := isolationsweep.OutcomeOf(out, "query deviceConstantsPage"); !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
		t.Errorf("query deviceConstantsPage: %+v (reached=%v), want FORBIDDEN for acme's member", o, ok)
	}
}

// LOW-2: the main sweep aims its probe at globex's DEVICE, so its hidden-versus-missing
// comparison never met a real hidden area or asset. These runs aim it at floor-globex and at
// asset-globex, which the unscoped control sees and acme's member must not: every operation's
// answer for the hidden place must be indistinguishable from the answer for a name that
// exists nowhere. anchorDevices is called with a literal targetType so its all-arguments
// document reaches the area (or asset) branch rather than stopping at an unknown kind.
func TestIsolationSweepAimedAtAHiddenAreaAndAsset(t *testing.T) {
	for _, c := range []struct{ probe, kind string }{{"floor-globex", "area"}, {"asset-globex", "asset"}} {
		t.Run(c.probe, func(t *testing.T) {
			f := newSweepScopeFixture(t)
			f.seedSweep(t, c.probe)
			out := isolationsweep.Run(t, isolationsweep.Config{
				Schema:        gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
				Scoped:        f.sweepMember("customer:acme"),
				Unscoped:      f.operator(),
				Probe:         c.probe,
				Hidden:        []string{"globex"},
				NothingSeeded: sweepCatalogueExceptions(),
				Literals: map[string]string{"ids": `"50"`, "areaTypeToken": `"floor"`, "assetTypeToken": `"machine"`, "customerTypeToken": `"retail"`, "originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`,
					"targetType": `"` + c.kind + `"`},
			})
			byToken := "query " + c.kind + "sByToken"
			if o, ok := isolationsweep.OutcomeOf(out, byToken); !ok || !o.Data {
				t.Errorf("%s: %+v (reached=%v), want data (an empty list) for acme's member", byToken, o, ok)
			}
			// What the sweep's anchorDevices call means: acme asking about a place only globex
			// has a device on is an empty page with total 0, and the operator's is not.
			q := `{ anchorDevices(criteria: {pageNumber: 1, pageSize: 10, targetType: "` + c.kind + `", target: "` + c.probe +
				`"}) { results { token } pagination { totalRecords } } }`
			acme := serve(t, f.member("customer:acme"), q, nil)
			requireServed(t, acme, "anchorDevices on the hidden place")
			page := acme.data["anchorDevices"].(map[string]any)
			if n := page["pagination"].(map[string]any)["totalRecords"].(float64); n != 0 || len(page["results"].([]any)) != 0 {
				t.Errorf("acme on %s: %v, want an empty page", c.probe, page)
			}
			op := serve(t, f.operator(), q, nil)
			requireServed(t, op, "control anchorDevices")
			if n := op.data["anchorDevices"].(map[string]any)["pagination"].(map[string]any)["totalRecords"].(float64); n != 1 {
				t.Errorf("control: the operator on %s saw %v devices, want globex's one", c.probe, n)
			}
		})
	}
}

// The alarm anchor filter, swept: every alarm read and the alarm stream take an anchor, and
// the sweep fills it with a CUSTOMER anchor naming the probe, which acme's scope does not
// name. The reads must be reached and REFUSED, not answered empty, as a customer outside the
// scope would otherwise name a co-owner. Its control is the operator, who is not refused.
func TestIsolationSweepAimedAtACustomerAlarmAnchor(t *testing.T) {
	f := newSweepScopeFixture(t)
	f.seedSweep(t, "globex-pump")
	out := isolationsweep.Run(t, isolationsweep.Config{
		Schema:        gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{}),
		Scoped:        f.sweepMember("customer:acme"),
		Unscoped:      f.operator(),
		Probe:         "globex-pump",
		Hidden:        []string{"globex"},
		NothingSeeded: sweepCatalogueExceptions(),
		Literals: map[string]string{"ids": `"50"`, "areaTypeToken": `"floor"`, "assetTypeToken": `"machine"`, "customerTypeToken": `"retail"`, "originatorType": `"device"`, "state": `"ACTIVE"`, "severity": `"WARNING"`,
			"type": `"customer"`},
	})
	for _, op := range []string{"query alarms (all arguments)", "query alarmCountsBySeverity (all arguments)", "query alarmCountsByDeviceType (all arguments)"} {
		o, ok := isolationsweep.OutcomeOf(out, op)
		if !ok || o.Data || len(o.Codes) == 0 || o.Codes[0] != "FORBIDDEN" {
			t.Errorf("%s: %+v (reached=%v), want FORBIDDEN for a customer anchor outside acme's scope", op, o, ok)
		}
	}
	// A subscribe-time refusal carries no wire code, so the stream is judged as refused (no data)
	// here and as FORBIDDEN by TestAlarmStreamRefusesACustomerAnchorOutsideTheScope.
	if o, ok := isolationsweep.OutcomeOf(out, "subscription alarmStream (all arguments)"); !ok || o.Data {
		t.Errorf("subscription alarmStream (all arguments): %+v (reached=%v), want a refusal", o, ok)
	}
}
