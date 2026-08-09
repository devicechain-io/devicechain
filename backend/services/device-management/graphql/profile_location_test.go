// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// CRUD over the profile's position declaration (ADR-078), driven through the REAL
// schema with REAL variables rather than by calling resolver methods directly. The
// variable path is the one that matters: every real client (console, SDKs, dcctl,
// codegen) sends variables, and the input-object handling on that path is the one this
// repo runs a forked graphql-go to keep honest.

func profileLocationCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DeviceType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	// Profile edits are device:write, like every other profile mutation in this area.
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// execProfileGql runs an operation and fails the test on any GraphQL error, so a
// silently-errored response can never be mistaken for a null field.
func execProfileGql(t *testing.T, ctx context.Context, query string, vars map[string]any) json.RawMessage {
	t.Helper()
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(ctx, query, "", vars)
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors: %v", resp.Errors)
	}
	return resp.Data
}

const createProfileMutation = `
mutation ($request: DeviceProfileCreateRequest) {
  createDeviceProfile(request: $request) {
    token
    location { expectedAccuracyMeters expectedUpdateIntervalSeconds }
  }
}`

const updateProfileMutation = `
mutation ($token: String!, $request: DeviceProfileCreateRequest) {
  updateDeviceProfile(token: $token, request: $request) {
    token
    location { expectedAccuracyMeters expectedUpdateIntervalSeconds }
  }
}`

const readProfileQuery = `
query ($tokens: [String!]!) {
  deviceProfilesByToken(tokens: $tokens) {
    token
    location { expectedAccuracyMeters expectedUpdateIntervalSeconds }
  }
}`

// locationFromProfiles digs the single profile's location object out of a
// deviceProfilesByToken response, distinguishing an explicit JSON null (undeclared)
// from a present object.
func locationFromProfiles(t *testing.T, data json.RawMessage) (present bool, accuracy *float64, interval *int32) {
	t.Helper()
	var parsed struct {
		Profiles []struct {
			Location *struct {
				ExpectedAccuracyMeters        *float64 `json:"expectedAccuracyMeters"`
				ExpectedUpdateIntervalSeconds *int32   `json:"expectedUpdateIntervalSeconds"`
			} `json:"location"`
		} `json:"deviceProfilesByToken"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(parsed.Profiles) != 1 {
		t.Fatalf("expected exactly one profile, got %d", len(parsed.Profiles))
	}
	loc := parsed.Profiles[0].Location
	if loc == nil {
		return false, nil, nil
	}
	return true, loc.ExpectedAccuracyMeters, loc.ExpectedUpdateIntervalSeconds
}

// Set it, then read it back. The two expectations carry distinct, non-round values so a
// resolver that returned the accuracy for the interval would be visible.
func TestGraphQLSetsAndReadsLocationDeclaration(t *testing.T) {
	ctx := profileLocationCtx(t)

	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{
			"token": "tracker",
			"location": map[string]any{
				"expectedAccuracyMeters":        3.75,
				"expectedUpdateIntervalSeconds": 45,
			},
		},
	})

	present, accuracy, interval := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"tracker"}}))
	if !present {
		t.Fatal("the declaration did not survive the write: location read back as null")
	}
	if accuracy == nil || *accuracy != 3.75 {
		t.Errorf("expectedAccuracyMeters = %v, want 3.75", accuracy)
	}
	if interval == nil || *interval != 45 {
		t.Errorf("expectedUpdateIntervalSeconds = %v, want 45", interval)
	}
}

// 🔴 A profile with no declaration reads back as an explicit null, and that null stays
// distinct from a declaration that simply states no expectations. Both requests below
// are legal and they are DIFFERENT claims: "does not report position" versus "reports
// position, nothing stated about it".
func TestGraphQLNullLocationIsDistinctFromDeclaredEmpty(t *testing.T) {
	ctx := profileLocationCtx(t)

	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "silent"},
	})
	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{"token": "declared-empty", "location": map[string]any{}},
	})

	undeclared, _, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"silent"}}))
	if undeclared {
		t.Error("a profile that declares no location must read back null, not an empty object")
	}

	present, accuracy, interval := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"declared-empty"}}))
	if !present {
		t.Fatal("a declared-but-unconfigured location read back as null — the declaration itself was lost")
	}
	if accuracy != nil || interval != nil {
		t.Errorf("declared-empty gained values from nowhere: accuracy=%v interval=%v", accuracy, interval)
	}
}

// Clearing over GraphQL: an update that carries no location removes the declaration,
// the same replace semantics name/category/metadata already have on this request.
func TestGraphQLClearsLocationDeclaration(t *testing.T) {
	ctx := profileLocationCtx(t)

	execProfileGql(t, ctx, createProfileMutation, map[string]any{
		"request": map[string]any{
			"token":    "tracker",
			"location": map[string]any{"expectedAccuracyMeters": 3.75},
		},
	})
	if present, _, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"tracker"}})); !present {
		t.Fatal("precondition failed: the declaration was never stored, so clearing it proves nothing")
	}

	execProfileGql(t, ctx, updateProfileMutation, map[string]any{
		"token":   "tracker",
		"request": map[string]any{"token": "tracker"},
	})

	if present, _, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
		map[string]any{"tokens": []any{"tracker"}})); present {
		t.Error("the declaration survived a clearing update")
	}
}

// Editing the declaration is a profile edit, so it is gated on device:write like every
// other profile mutation — a read-only caller cannot set one.
func TestGraphQLLocationDeclarationRequiresDeviceWrite(t *testing.T) {
	ctx := profileLocationCtx(t)
	readOnly := withAuthorities(ctx, viewerBaseline...)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(readOnly, createProfileMutation, "", map[string]any{
		"request": map[string]any{
			"token":    "tracker",
			"location": map[string]any{"expectedAccuracyMeters": 3.75},
		},
	})
	if len(resp.Errors) == 0 {
		t.Fatal("a read-only caller was allowed to declare a location on a profile")
	}
}
