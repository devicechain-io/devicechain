// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/datatypes"
)

// THIS SERVICE'S HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness check live in core's
// rdb/partialupdatetest, because the three-state update semantic is one platform rule
// and a per-service copy of the harness is the per-family copy one level up. Read that
// package's harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service's
// Api (which needs a secret store, not just a database) and the one converted family.

// partialUpdateTenant is a tenant the fixture context carries. AIProvider is
// INSTANCE-scoped — it has no tenant column and Api.sys runs its statements in the
// system context — so nothing here is narrowed by it. It is supplied because the
// tenant-scope callback fails CLOSED, and a fixture that ran without one would be
// certifying a world in which the callback is absent.
const partialUpdateTenant = "acme"

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family. The database
// half — the named shared-cache DSN, the pool close, the token-grammar registration,
// and the reasons all three are load-bearing — is putest.NewSQLiteDB's.
//
// 🔴 THE SECRET STORE IS PART OF THE FIXTURE, NOT AN EXTRA. A provider's API key is
// never a column, so a fixture without a store could not observe the `secret` field's
// three states at all — and an unobservable field is one the harness cannot protect.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	db := putest.NewSQLiteDB(t, tables...)
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate the secret store: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	if err != nil {
		t.Fatalf("build the instance key provider: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// storedKey renders a provider's write-only API key for the harness's map[string]string
// reading: the sealed value, or NullMarker when none is stored.
//
// NullMarker rather than "" is deliberate. "" is what applyProviderSecret writes to mean
// DELETE, so rendering "no key" as "" would make the harness unable to tell "the caller
// cleared this" from "a key holding the empty string".
func storedKey(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	found, err := api.AIProvidersByToken(ctx, []string{token})
	provider := putest.RequireOne(t, "ai provider", found, err)
	value, err := api.Secrets.Resolve(ctx, AIProviderSecretRef(provider.ID))
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return putest.NullMarker
	}
	if err != nil {
		t.Fatalf("resolve the provider's key: %v", err)
	}
	return string(value)
}

// paramsReading renders the params column for the harness.
//
// 🔴 putest.JSONString TAKES A *datatypes.JSON AND THIS COLUMN IS A VALUE, so passing
// its address would make the pointer NEVER nil and a cleared column read back as "" —
// which is neither the NullMarker the harness compares against nor distinguishable from
// a column holding the empty string. datatypes.JSON is itself a slice, so its own nil
// is the SQL NULL, and that is what has to be tested.
func paramsReading(params datatypes.JSON) string {
	if params == nil {
		return putest.NullMarker
	}
	return string(params)
}

// The values the provider family drives.
const (
	seededProviderEndpoint   = "https://models.example.invalid/v1"
	replacedProviderEndpoint = "https://other.example.invalid/v2"
	seededProviderParams     = `{"maxTokens":256}`
	replacedProviderParams   = `{"maxTokens":512}`
)

// aiProviderFamily is this service's one converted family.
//
// 🔴 THE SEED CARRIES AN ENDPOINT EVEN THOUGH ITS KIND DOES NOT NEED ONE, and that is
// load-bearing rather than incidental. `kind` and `endpoint` are validated as a pair:
// `openai-compatible` is DEFINED by its address and has no built-in default, so it is
// refused without one. The kind field's replacement value is exactly that kind, and the
// property that drives it moves no other field — so a seed with a blank endpoint would
// make a LEGAL request fail, and the only way to green the suite would be to stop
// driving `kind` at all. Seeding the endpoint keeps the pair valid in both directions
// without needing the partner mechanism.
//
// The mirror case — clearing the endpoint while the kind is openai-compatible — IS
// refused, and it has its own test (TestOpenAICompatibleProviderCannotHaveItsEndpointCleared)
// because it is a refusal rather than one of these properties.
func aiProviderFamily() putest.Family[*Api] {
	return putest.Family[*Api]{
		Name:    "aiProvider",
		Token:   "prov-1",
		Migrate: []any{&AIProvider{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateAIProvider(ctx, &AIProviderCreateRequest{
				Token:       "prov-1",
				Name:        strp("Claude"),
				Description: strp("Original description"),
				Kind:        string(AIProviderKindAnthropic),
				Endpoint:    strp(seededProviderEndpoint),
				Model:       "claude-opus-4-8",
				Params:      strp(seededProviderParams),
				Enabled:     true,
				Secret:      strp("sk-seeded"),
			}); err != nil {
				t.Fatalf("seed ai provider: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.AIProvidersByToken(ctx, []string{"prov-1"})
			e := putest.RequireOne(t, "ai provider", rows, err)
			return map[string]string{
				"name":        putest.NullString(e.Name),
				"description": putest.NullString(e.Description),
				"kind":        e.Kind,
				"endpoint":    e.Endpoint,
				"model":       e.ModelID,
				"params":      paramsReading(e.Params),
				"enabled":     putest.BoolString(e.Enabled),
				"secret":      storedKey(t, api, ctx, "prov-1"),
			}
		},
		NewRequest: func() any { return new(AIProviderUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			// expectedUpdatedAt is nil: the optimistic-concurrency precondition is a
			// different contract with its own tests, and passing one here would make
			// every property depend on a timestamp round trip.
			_, err := api.UpdateAIProvider(ctx, token, req.(*AIProviderUpdateRequest), nil)
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "Claude", "Renamed",
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", "Original description", "Rewritten",
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.RequiredStringField("kind", string(AIProviderKindAnthropic),
				string(AIProviderKindOpenAICompatible),
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalString { return &r.Kind }),
			// The endpoint column is a plain NOT-NULL-in-Go string whose EMPTY value is
			// the meaningful state ("use the kind's built-in default"), so its cleared
			// reading is "" rather than NullMarker — there is no SQL NULL to reach.
			putest.Field{
				Name: "endpoint", Seeded: seededProviderEndpoint,
				Replace: replacedProviderEndpoint, Cleared: "",
				Kind: putest.Clearable,
				Set: func(req any, v string) {
					req.(*AIProviderUpdateRequest).Endpoint = dcgraphql.OptionalStringOf(v)
				},
				SetNull: func(req any) {
					req.(*AIProviderUpdateRequest).Endpoint = dcgraphql.ClearedString()
				},
			},
			putest.RequiredStringField("model", "claude-opus-4-8", "claude-haiku-4-5-20251001",
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalString { return &r.Model }),
			putest.OptionalStringField("params", seededProviderParams, replacedProviderParams,
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalString { return &r.Params }),
			putest.RequiredBoolField("enabled", true,
				func(r *AIProviderUpdateRequest) *dcgraphql.OptionalBool { return &r.Enabled }),
			// The write-only key. It is CLEARABLE, and that is this conversion's
			// decision: an explicit null DELETES the stored key, which is what null
			// means on every other field on the platform. Under the old *string the
			// clear was spelled as the empty string, because a pointer had no third
			// state to give it.
			putest.Field{
				Name: "secret", Seeded: "sk-seeded", Replace: "sk-rotated",
				Cleared: putest.NullMarker,
				Kind:    putest.Clearable,
				Set: func(req any, v string) {
					req.(*AIProviderUpdateRequest).Secret = dcgraphql.OptionalStringOf(v)
				},
				SetNull: func(req any) {
					req.(*AIProviderUpdateRequest).Secret = dcgraphql.ClearedString()
				},
			},
		},
	}
}

func partialUpdateFamilies() []putest.Family[*Api] {
	return []putest.Family[*Api]{aiProviderFamily()}
}

// TestPartialUpdate drives every property over every converted family in this service.
//
// A single property or family is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/aiProvider'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Api]{
		NewApi:   newPartialUpdateApi,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe: the only token-keyed create this service has.
		CreateWithToken: func(t *testing.T, api *Api, ctx context.Context, token string) error {
			_, err := api.CreateAIProvider(ctx, claudeReq(token, nil))
			return err
		},
		StrictnessTables: []any{&AIProvider{}},
		ValidToken:       "a-valid_token1",
	})
}

// 🔴 THE EXHAUSTIVENESS CHECK OVER THIS SERVICE'S UPDATE SURFACE.
//
// The guard itself is core's putest.AssertEveryUpdateTakesADedicatedRequest — it
// enumerates *Api's own Update* methods and asks three structural things of each: that
// the request type is REGISTERED with partialUpdateFamilies() so something drives its
// three states against a real database; that it carries no Token field; and that every
// exported field carries the three states. Run asks whether the converted families
// BEHAVE; this asks whether the set of converted families is still the whole set.
//
// What is local is what only this service can say: which updates have NOT been
// converted, and how many Update* methods reflection must find before the walk is
// believable.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Api]{
		Families: partialUpdateFamilies(),

		// No exemptions: this service's one update is converted. The guard fails on an
		// exemption matching nothing, so the map cannot quietly regrow an entry
		// describing a state of the world that has ended.
		Exempt:            map[string]string{},
		NotAnEntityUpdate: map[string]string{},

		// The anti-vacuity floor, set at the measured count. Reflection over a renamed
		// or embedded receiver could find nothing at all, and a loop over nothing
		// reports success.
		MinUpdateMethods: 1,
	})
}
