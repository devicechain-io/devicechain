// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

const wireProfileVersions = `query ($token: String!, $limit: Int, $offset: Int) {
  deviceProfileVersions(token: $token, limit: $limit, offset: $offset) { version } }`

// The optional limit and offset of a version list reach the model through the served
// schema, and a nonsense value is answered with a typed code.
func TestVersionListLimitAndOffsetOnTheWire(t *testing.T) {
	ctx := conflictWireCtx(t)
	requireNoErrors(t, execServed(t, ctx, wireCreateProfile,
		map[string]any{"request": map[string]any{"token": "rover"}}), "the profile create")

	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	var profile model.DeviceProfile
	if err := api.RDB.DB(ctx).Where("token = ?", "rover").First(&profile).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]*model.DeviceProfileVersion, 0, 5)
	for v := 1; v <= 5; v++ {
		rows = append(rows, &model.DeviceProfileVersion{DeviceProfileId: profile.ID, Version: int32(v), Snapshot: datatypes.JSON(`{}`)})
	}
	if err := api.RDB.DB(ctx).Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, wireProfileVersions, "", map[string]any{"token": "rover", "limit": 2, "offset": 1})
	requireNoErrors(t, resp.Errors, "the paged read")
	want := `{"deviceProfileVersions":[{"version":4},{"version":3}]}`
	if string(resp.Data) != want {
		t.Fatalf("data = %s, want %s", resp.Data, want)
	}

	qe := onlyError(t, schema.Exec(ctx, wireProfileVersions, "", map[string]any{"token": "rover", "limit": 0}).Errors)
	if got := qe.Extensions["code"]; got != "INVALID_VALUE" {
		t.Fatalf("limit 0: extensions.code = %v, want INVALID_VALUE (%s)", got, qe.Message)
	}
}
