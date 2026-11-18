// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

func TestSnapshotFenceIdentityOnGraphQLWire(t *testing.T) {
	ctx, _ := twoTenantFenceCtx(t)
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)
	s, err := api.CurrentGeoFenceSetSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var f model.GeoFence
	if err := api.RDB.DB(ctx).Where("token = ?", "acme-yard").First(&f).Error; err != nil {
		t.Fatal(err)
	}
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, `query { currentGeoFenceSet { fences(pagination: {pageNumber: 1, pageSize: 10}) { results { token fenceId geometry } } } }`, "", nil)
	if len(resp.Errors) != 0 {
		t.Fatalf("GraphQL: %v", resp.Errors)
	}
	var data struct {
		Set struct {
			Fences struct {
				Results []struct {
					Token    string
					FenceID  *string
					Geometry string
				}
			}
		} `json:"currentGeoFenceSet"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatal(err)
	}
	results := data.Set.Fences.Results
	if len(results) != 1 || results[0].FenceID == nil || *results[0].FenceID != fmt.Sprint(f.ID) ||
		results[0].Token != "acme-yard" || results[0].Geometry != string(s.Fences[0].Geometry) {
		t.Fatalf("snapshot wire fields: %s", resp.Data)
	}
	// No live lookup or invented sentinel for historical entries without identity.
	legacy := &GeoFenceSnapshotEntryResolver{M: model.GeoFenceSnapshotRef{Token: "acme-yard"}, C: ctx}
	if legacy.FenceId() != nil {
		t.Fatal("legacy entry must expose null identity")
	}
}
