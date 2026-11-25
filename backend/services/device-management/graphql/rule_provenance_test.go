// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/golang-jwt/jwt/v5"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func provenanceRig(t *testing.T) context.Context {
	t.Helper()
	ctx := profileLocationCtx(t)
	ctx = auth.WithClaims(ctx, &auth.Claims{Username: "display-name", Email: "display@example.com",
		RegisteredClaims: jwt.RegisteredClaims{Subject: "principal-42"},
		Authorities:      []string{string(auth.DeviceWrite), string(auth.DeviceRead)}})
	execProfileGql(t, ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "profile"}})
	api := (&SchemaResolver{}).GetApi(ctx)
	db, err := api.RDB.Database.DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.WithValue(ctx, ContextCachedApiKey, model.NewCachedApi(api, &model.Caches{}))
}

const provenanceCreate = `mutation($request:DetectionRuleCreateRequest!){createDetectionRule(request:$request){token authoredVia}}`
const provenanceUpdate = `mutation($request:DetectionRuleUpdateRequest!){updateDetectionRule(token:"rule",request:$request){authoredVia}}`
const provenancePublish = `mutation{publishDeviceProfile(token:"profile",label:"caller-controlled"){version publishedBy publishedById authoredVia}}`

func provenanceData(t *testing.T, raw json.RawMessage, field string) map[string]any {
	t.Helper()
	var data map[string]map[string]any
	require.NoError(t, json.Unmarshal(raw, &data))
	return data[field]
}

func TestRuleProvenanceDoorsRoundTrip(t *testing.T) {
	for _, door := range []string{"FORM", "CANVAS", "DESCRIBE", "PRODUCT_AS_CODE", "API", "UNKNOWN", "omitted", "null"} {
		t.Run(door, func(t *testing.T) {
			ctx := provenanceRig(t)
			input := map[string]any{"token": "rule", "deviceProfileToken": "profile", "definition": `{}`, "enabled": false}
			want := door
			if door == "omitted" || door == "null" {
				want = "API"
				if door == "null" {
					input["authoredVia"] = nil
				}
			} else {
				input["authoredVia"] = door
			}
			got := provenanceData(t, execProfileGql(t, ctx, provenanceCreate, map[string]any{"request": input}), "createDetectionRule")
			require.Equal(t, []any{want}, got["authoredVia"])
			update := map[string]any{"name": "saved again"}
			if door != "omitted" {
				update["authoredVia"] = input["authoredVia"]
			}
			got = provenanceData(t, execProfileGql(t, ctx, provenanceUpdate, map[string]any{"request": update}), "updateDetectionRule")
			require.Equal(t, []any{want}, got["authoredVia"])
			version := provenanceData(t, execProfileGql(t, ctx, provenancePublish, nil), "publishDeviceProfile")
			require.Equal(t, []any{want}, version["authoredVia"])
			require.Equal(t, "principal-42", version["publishedById"])
			require.Equal(t, "display-name", version["publishedBy"])
			// Read from storage again; the create/publish response alone cannot prove persistence.
			raw := execProfileGql(t, ctx, `{detectionRulesByToken(tokens:["rule"]){authoredVia} deviceProfileVersions(token:"profile"){publishedById authoredVia}}`, nil)
			require.JSONEq(t, `{"detectionRulesByToken":[{"authoredVia":["`+want+`"]}],"deviceProfileVersions":[{"publishedById":"principal-42","authoredVia":["`+want+`"]}]}`, string(raw))
		})
	}
}

func TestRuleProvenanceMixedDoorsStayFrozen(t *testing.T) {
	ctx := provenanceRig(t)
	for token, door := range map[string]string{"rule": "DESCRIBE", "other": "CANVAS"} {
		execProfileGql(t, ctx, provenanceCreate, map[string]any{"request": map[string]any{
			"token": token, "deviceProfileToken": "profile", "definition": `{}`, "enabled": false, "authoredVia": door}})
	}
	for _, input := range []map[string]any{{"definition": `{"edited":true}`, "authoredVia": "FORM"}, {"name": "API edit"}} {
		execProfileGql(t, ctx, provenanceUpdate, map[string]any{"request": input})
	}
	version := provenanceData(t, execProfileGql(t, ctx, provenancePublish, nil), "publishDeviceProfile")
	require.Equal(t, []any{"API", "CANVAS", "DESCRIBE", "FORM"}, version["authoredVia"])
	execProfileGql(t, ctx, provenanceUpdate, map[string]any{"request": map[string]any{"authoredVia": "PRODUCT_AS_CODE", "name": "later"}})
	raw := execProfileGql(t, ctx, `{deviceProfileVersions(token:"profile"){authoredVia}}`, nil)
	require.JSONEq(t, `{"deviceProfileVersions":[{"authoredVia":["API","CANVAS","DESCRIBE","FORM"]}]}`, string(raw))
}

func TestRuleProvenanceLegacyNulls(t *testing.T) {
	ctx := provenanceRig(t)
	api := (&SchemaResolver{}).GetApi(ctx)
	profiles, err := api.DeviceProfilesByToken(ctx, []string{"profile"})
	require.NoError(t, err)
	// Old pod writes omit the new columns, including after the migration.
	rule := &model.DetectionRule{DeviceProfileId: profiles[0].ID, Definition: datatypes.JSON(`{}`)}
	rule.Token = "rule"
	require.NoError(t, api.RDB.DB(ctx).Omit("AuthoredVia").Create(rule).Error)
	version := &model.DeviceProfileVersion{DeviceProfileId: profiles[0].ID, Version: 1, Snapshot: datatypes.JSON(`{}`)}
	require.NoError(t, api.RDB.DB(ctx).Omit("AuthoredVia", "PublishedById").Create(version).Error)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, `{detectionRulesByToken(tokens:["rule"]){authoredVia} deviceProfileVersions(token:"profile"){authoredVia publishedById}}`, "", nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"detectionRulesByToken":[{"authoredVia":["UNKNOWN"]}],"deviceProfileVersions":[{"authoredVia":["UNKNOWN"],"publishedById":null}]}`, string(res.Data))
	execProfileGql(t, ctx, provenanceUpdate, map[string]any{"request": map[string]any{"name": "legacy edited", "authoredVia": "FORM"}})
	got := provenanceData(t, execProfileGql(t, ctx, provenancePublish, nil), "publishDeviceProfile")
	require.Equal(t, []any{"FORM", "UNKNOWN"}, got["authoredVia"])
}

func TestRuleProvenanceRefusesClientForgery(t *testing.T) {
	ctx := provenanceRig(t)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	enumResponse := schema.Exec(ctx, `{__type(name:"AuthoredVia"){enumValues{name}}}`, "", nil)
	require.Empty(t, enumResponse.Errors)
	var enumData map[string]map[string][]map[string]string
	require.NoError(t, json.Unmarshal(enumResponse.Data, &enumData))
	names := []string{}
	for _, value := range enumData["__type"]["enumValues"] {
		names = append(names, value["name"])
	}
	require.ElementsMatch(t, []string{"FORM", "CANVAS", "DESCRIBE", "PRODUCT_AS_CODE", "API", "UNKNOWN"}, names)
	execProfileGql(t, ctx, provenanceCreate, map[string]any{"request": map[string]any{
		"token": "rule", "deviceProfileToken": "profile", "definition": `{}`, "enabled": false, "authoredVia": "FORM"}})
	for _, door := range []string{"AGENT_PROPOSAL", "invented", "form", ""} {
		input := map[string]any{"token": "bad-rule", "deviceProfileToken": "profile", "definition": `{}`, "enabled": false, "authoredVia": door}
		res := schema.Exec(ctx, provenanceCreate, "", map[string]any{"request": input})
		require.NotEmpty(t, res.Errors, door)
		res = schema.Exec(ctx, provenanceUpdate, "", map[string]any{"request": map[string]any{"authoredVia": door}})
		require.NotEmpty(t, res.Errors, door)
	}
	res := schema.Exec(ctx, `mutation($request:DetectionRuleCreateRequest!){createDetectionRule(request:$request){token}}`, "", map[string]any{"request": map[string]any{
		"token": "forged-rule", "deviceProfileToken": "profile", "definition": `{}`, "enabled": false, "publishedById": "forged"}})
	require.NotEmpty(t, res.Errors)
	res = schema.Exec(ctx, `mutation{publishDeviceProfile(token:"profile",publishedById:"forged"){version}}`, "", nil)
	require.NotEmpty(t, res.Errors)
	// A service account's stable subject is captured, even with no username or email.
	ctx = auth.WithClaims(ctx, &auth.Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "service-account-7"}, Authorities: []string{string(auth.DeviceWrite)}})
	got := provenanceData(t, execProfileGql(t, ctx, provenancePublish, nil), "publishDeviceProfile")
	require.Equal(t, "service-account-7", got["publishedById"])
	require.Equal(t, []any{"FORM"}, got["authoredVia"])
	ctx = auth.WithClaims(ctx, &auth.Claims{Email: "legacy@example.com", Authorities: []string{string(auth.DeviceWrite)}})
	got = provenanceData(t, execProfileGql(t, ctx, provenancePublish, nil), "publishDeviceProfile")
	require.Nil(t, got["publishedById"])
	require.Equal(t, "legacy@example.com", got["publishedBy"])
}
