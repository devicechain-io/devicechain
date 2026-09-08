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
mutation ($token: String!, $request: DeviceProfileUpdateRequest!) {
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

// 🔴 THE THREE STATES OF `location`, DRIVEN OVER THE WIRE, AND THE ONE THAT CHANGED.
//
// A nested input object is the only place on the platform where the three states go
// through a hand-written unmarshaler rather than one of core's Optional* scalars, so
// this is where "absent" and "explicit null" are shown to arrive distinguishably — the
// model harness sends Go values and cannot see the packer at all.
//
// The absent case is the behaviour change. It used to CLEAR: a request carrying no
// location removed the declaration, which meant editing a profile's name silently
// un-declared position for every device built on it. It now preserves, and the clear
// has to be asked for.
func TestGraphQLLocationDeclarationThreeStates(t *testing.T) {
	seed := func(t *testing.T) context.Context {
		t.Helper()
		ctx := profileLocationCtx(t)
		execProfileGql(t, ctx, createProfileMutation, map[string]any{
			"request": map[string]any{
				"token":    "tracker",
				"location": map[string]any{"expectedAccuracyMeters": 3.75},
			},
		})
		if present, _, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
			map[string]any{"tokens": []any{"tracker"}})); !present {
			t.Fatal("precondition failed: the declaration was never stored, so nothing below proves anything")
		}
		return ctx
	}

	t.Run("omitted preserves", func(t *testing.T) {
		ctx := seed(t)
		// A well-formed update that says nothing about location, which is exactly the
		// request the old defect arrived in.
		execProfileGql(t, ctx, updateProfileMutation, map[string]any{
			"token":   "tracker",
			"request": map[string]any{"name": "Renamed display only"},
		})
		present, accuracy, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
			map[string]any{"tokens": []any{"tracker"}}))
		if !present {
			t.Fatal("an update that never mentioned location erased the declaration")
		}
		if accuracy == nil || *accuracy != 3.75 {
			t.Errorf("expectedAccuracyMeters = %v, want the untouched 3.75", accuracy)
		}
	})

	t.Run("explicit null clears", func(t *testing.T) {
		ctx := seed(t)
		execProfileGql(t, ctx, updateProfileMutation, map[string]any{
			"token":   "tracker",
			"request": map[string]any{"location": nil},
		})
		if present, _, _ := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
			map[string]any{"tokens": []any{"tracker"}})); present {
			t.Error("the declaration survived an explicit null")
		}
	})

	t.Run("an object replaces", func(t *testing.T) {
		ctx := seed(t)
		execProfileGql(t, ctx, updateProfileMutation, map[string]any{
			"token": "tracker",
			"request": map[string]any{
				"location": map[string]any{"expectedUpdateIntervalSeconds": 45},
			},
		})
		present, accuracy, interval := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
			map[string]any{"tokens": []any{"tracker"}}))
		if !present {
			t.Fatal("a declaration sent as a value did not arrive")
		}
		if accuracy != nil {
			t.Errorf("expectedAccuracyMeters = %v, want it replaced away — the declaration is one "+
				"document, not a per-key merge", accuracy)
		}
		if interval == nil || *interval != 45 {
			t.Errorf("expectedUpdateIntervalSeconds = %v, want 45", interval)
		}
	})

	// The empty object is a fourth wire spelling and is NOT the null: it is the
	// "reports position, no expectations stated" claim, which must stay distinct all
	// the way to the client.
	t.Run("an empty object is not a clear", func(t *testing.T) {
		ctx := seed(t)
		execProfileGql(t, ctx, updateProfileMutation, map[string]any{
			"token":   "tracker",
			"request": map[string]any{"location": map[string]any{}},
		})
		present, accuracy, interval := locationFromProfiles(t, execProfileGql(t, ctx, readProfileQuery,
			map[string]any{"tokens": []any{"tracker"}}))
		if !present {
			t.Fatal("`{}` was stored as NULL, which says 'does not report position' — a different claim")
		}
		if accuracy != nil || interval != nil {
			t.Errorf("declared-empty kept values from the previous declaration: accuracy=%v interval=%v",
				accuracy, interval)
		}
	})
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
