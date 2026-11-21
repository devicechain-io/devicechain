// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
)

const wireImportFacets = `mutation ($rows: [FacetValueImportRow!]!, $dryRun: Boolean!) {
  importFacetValues(rows: $rows, dryRun: $dryRun) {
    row memberType token key outcome code message
  }
}`

func TestImportFacetValuesUsesFacetMemberTypeOverTheWire(t *testing.T) {
	ctx := withAuthorities(importWireCtx(t), auth.DeviceWrite)
	api := (&SchemaResolver{}).GetApi(ctx)
	if err := api.RDB.DB(ctx).AutoMigrate(&model.EntityAttribute{}, &model.FacetKey{}, &model.EntityGroup{},
		&model.EntityGroupVersion{}, &model.EntityGroupMembership{}, &model.DeviceConfigurationRevision{}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "d1", DeviceTypeToken: "sensor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.SetFacetKey(ctx, &model.FacetKeySetRequest{MemberType: "device", Key: "floors", ValueType: "LONG"}); err != nil {
		t.Fatal(err)
	}
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, wireImportFacets, "", map[string]any{"dryRun": true, "rows": []any{
		map[string]any{"memberType": "device", "token": "d1", "key": "floors", "value": "3"},
		map[string]any{"memberType": "device", "token": "d1", "key": "floors", "value": "no"},
	}})
	if len(res.Errors) != 0 {
		t.Fatalf("importFacetValues: %v", res.Errors)
	}
	var data struct {
		Rows []struct {
			Row           int
			Outcome, Code string
			MemberType    string
		} `json:"importFacetValues"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 2 || data.Rows[0].MemberType != "device" || data.Rows[0].Row != 1 || data.Rows[0].Outcome != "SET" || data.Rows[1].Outcome != "INVALID" {
		t.Fatalf("unexpected per-row report: %s", res.Data)
	}
}

func TestFacetImportFamilyFieldNames(t *testing.T) {
	t.Setenv(gqlcore.EnvGraphQLDevTools, "true")
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(context.Background(), `{
	  input: __type(name: "FacetValueImportRow") { inputFields { name } }
	  result: __type(name: "FacetValueImportRowResult") { fields { name } }
	}`, "", nil)
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	var data struct {
		Input  struct{ InputFields []struct{ Name string } }
		Result struct{ Fields []struct{ Name string } }
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatal(err)
	}
	for _, fields := range [][]struct{ Name string }{data.Input.InputFields, data.Result.Fields} {
		found := false
		for _, field := range fields {
			if field.Name == "entityType" {
				t.Fatal("facet imports must use the existing memberType vocabulary")
			}
			found = found || field.Name == "memberType"
		}
		if !found {
			t.Fatal("memberType is missing from the facet import contract")
		}
	}
}

func TestImportFacetValuesRequiresDeviceWrite(t *testing.T) {
	// Authorization must run before API lookup, including for previews.
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	vars := map[string]any{"dryRun": true, "rows": []any{
		map[string]any{"memberType": "device", "token": "d1", "key": "floors", "value": "3"},
	}}
	base := importWireCtx(t)
	for _, ctx := range []context.Context{base, withAuthorities(base, viewerBaseline...)} {
		res := schema.Exec(ctx, wireImportFacets, "", vars)
		if len(res.Errors) == 0 || strings.Contains(res.Errors[0].Message, "panic") {
			t.Fatalf("caller without device:write admitted: %s %v", res.Data, res.Errors)
		}
	}
}

func TestImportFacetValuesRejectsUnknownInputFields(t *testing.T) {
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(withAuthorities(context.Background(), auth.DeviceWrite), wireImportFacets, "", map[string]any{
		"dryRun": true, "rows": []any{map[string]any{
			"memberType": "device", "token": "d1", "key": "floors", "value": "3", "scope": "CLIENT",
		}},
	})
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "scope") {
		t.Fatalf("unknown input silently accepted: %s %v", res.Data, res.Errors)
	}
}
