// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"gorm.io/gorm"
)

// This file seeds the stored-snapshot identity fixture, and is deliberately written against
// only what existed BEFORE the semantic layer, so the same file compiles on the commit that
// precedes it. The fixture in testdata/semantic was produced by running
//
//	DC_WRITE_PRE_SEMANTIC_FIXTURE=1 go test ./model -run TestWritePreSemanticSnapshotFixture
//
// with this file copied onto that earlier commit: its bytes are what the PRE-CHANGE binary
// stored. semantic_snapshot_identity_test.go then holds today's code to them. Regenerating it
// on a tree that already has the layer would prove nothing, which is why the writer refuses
// to run unless asked.

const preSemanticFixture = "testdata/semantic/pre_semantic_snapshots.json"

// identityClock pins every timestamp gorm writes, so a snapshot (which freezes each metric's
// CreatedAt/UpdatedAt) is the same bytes on every run.
var identityClock = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type preSemanticFixtureDoc struct {
	ProfileVersions []struct {
		Version    int32  `json:"version"`
		Snapshot   string `json:"snapshot"`
		ThingModel string `json:"thingModel"`
	} `json:"profileVersions"`
	AssetTypeVersions []struct {
		Version        int32  `json:"version"`
		PropertySchema string `json:"propertySchema"`
	} `json:"assetTypeVersions"`
}

func identityApi(t *testing.T, extra ...any) (*Api, context.Context) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, &gorm.Config{NowFunc: func() time.Time { return identityClock }})
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(append([]any{&DeviceType{}, &AssetType{}, &AssetTypeVersion{}, &Asset{},
		&DeviceProfile{}, &DeviceProfileVersion{}, &DeviceProfileActivation{}, &AutomationReviewPolicy{}, &AutomationReviewPolicyChange{}, &AutomationReviewBreakGlass{}, &AutomationReview{}, &MetricDefinition{},
		&CommandDefinition{}, &DetectionRule{}, &DetectionRuleScopeRef{}}, extra...)...); err != nil {
		t.Fatal(err)
	}
	api := NewApi(&rdb.RdbManager{Database: db})
	api.CalculatedMetricsCeiling = &stubCeiling{n: 10}
	return api, core.WithTenant(context.Background(), "acme")
}

// seedIdentityHistory publishes a profile three times (empty; measured, enum and calculated
// metrics plus a command; an edited description) and an asset type twice.
func seedIdentityHistory(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }

	must(api.CreateDeviceProfile(ctx, &DeviceProfileCreateRequest{Token: "plant", Name: s("Plant room"),
		Description: s("A plant room controller")}))
	must(api.PublishDeviceProfile(ctx, "plant", nil, nil, "tester"))
	must(api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: "temp", DeviceProfileToken: "plant",
		MetricKey: "temp", Name: s("Temperature"), DataType: string(MetricDouble), Unit: s("Cel"),
		MinValue: f(-40), MaxValue: f(85.5), Descriptor: s("saref:Temperature"), Metadata: s(`{"b":1,"a":[2,1]}`)}))
	must(api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: "mode", DeviceProfileToken: "plant",
		MetricKey: "mode", DataType: string(MetricInt), Enum: s(`["0","1","2"]`)}))
	must(api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: "humidity_raw", DeviceProfileToken: "plant",
		MetricKey: "humidity_raw", DataType: string(MetricDouble)}))
	must(api.CreateMetricDefinition(ctx, &MetricDefinitionCreateRequest{Token: "humidity", DeviceProfileToken: "plant",
		MetricKey: "humidity", DataType: string(MetricDouble), Unit: s("%"), Expression: s("humidity_raw * 0.5")}))
	must(api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{Token: "reboot", DeviceProfileToken: "plant",
		CommandKey: "reboot", Name: s("Reboot"), ParameterSchema: s(`[{"name":"delay","dataType":"INT"}]`)}))
	must(api.PublishDeviceProfile(ctx, "plant", s("v2"), nil, "tester"))
	must(api.UpdateMetricDefinition(ctx, "temp", &MetricDefinitionUpdateRequest{Description: dcgraphql.OptionalStringOf("Supply air")}))
	must(api.PublishDeviceProfile(ctx, "plant", nil, nil, "tester"))

	must(api.CreateAssetType(ctx, &AssetTypeCreateRequest{Token: "cabinet",
		PropertySchema: s(`[{"name":"vendor","dataType":"STRING"}]`)}))
	must(api.PublishAssetType(ctx, "cabinet", nil, nil, "tester"))
	must(api.UpdateAssetType(ctx, "cabinet", &AssetTypeUpdateRequest{
		PropertySchema: dcgraphql.OptionalStringOf(`[{"name":"vendor","dataType":"STRING"},{"name":"stages","dataType":"INT"}]`)}))
	must(api.PublishAssetType(ctx, "cabinet", nil, nil, "tester"))
}

// readIdentityHistory renders what seedIdentityHistory stored, in the fixture's shape.
func readIdentityHistory(t *testing.T, api *Api, ctx context.Context) preSemanticFixtureDoc {
	t.Helper()
	var doc preSemanticFixtureDoc
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"plant"})
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profile: %v", err)
	}
	var versions []DeviceProfileVersion
	if err := api.RDB.DB(ctx).Order("version").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	for i := range versions {
		tm, err := ThingModelOf(profiles[0], &versions[i])
		if err != nil {
			t.Fatal(err)
		}
		doc.ProfileVersions = append(doc.ProfileVersions, struct {
			Version    int32  `json:"version"`
			Snapshot   string `json:"snapshot"`
			ThingModel string `json:"thingModel"`
		}{versions[i].Version, string(versions[i].Snapshot), string(tm)})
	}
	var assetVersions []AssetTypeVersion
	if err := api.RDB.DB(ctx).Order("version").Find(&assetVersions).Error; err != nil {
		t.Fatal(err)
	}
	for _, v := range assetVersions {
		doc.AssetTypeVersions = append(doc.AssetTypeVersions, struct {
			Version        int32  `json:"version"`
			PropertySchema string `json:"propertySchema"`
		}{v.Version, string(v.PropertySchema)})
	}
	return doc
}

func TestWritePreSemanticSnapshotFixture(t *testing.T) {
	if os.Getenv("DC_WRITE_PRE_SEMANTIC_FIXTURE") == "" {
		t.Skip("writes the pre-change fixture; run on the commit BEFORE the semantic layer only")
	}
	api, ctx := identityApi(t)
	seedIdentityHistory(t, api, ctx)
	raw, err := json.MarshalIndent(readIdentityHistory(t, api, ctx), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(preSemanticFixture), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preSemanticFixture, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
