// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// THIS SERVICE'S HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness checks live in core's
// rdb/partialupdatetest, because the three-state update semantic is one platform rule and
// a per-service copy of the harness is the per-family copy one level up — the eighth copy
// certifies less than the first, and every one of them is green. Read that package's
// harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service's Api,
// the tenant it runs under, and the registry of converted families.
//
// dashboard-management has ONE entity with an update mutation, so the registry has one
// row. That is not a reason to skip the harness: the properties it drives — set one field
// and nothing else moves, clear one and nothing else moves, name nothing and nothing
// moves — are exactly the ones this conversion could get wrong, and a hand-written test
// per field is the shape the harness exists to replace.

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family. The database
// half — the named shared-cache DSN, the pool close, the token-grammar registration, and
// the reasons all three are load-bearing — is putest.NewSQLiteDB's.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	return NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, tables...)})
}

// partialUpdateTenant is the one place this service's fixture tenant is spelled, so the
// suite and the family cannot drift onto different tenants — which under the fail-closed
// tenant-scope callback would surface as a family that seeds into one database and reads
// from another.
const partialUpdateTenant = "acme"

// TestPartialUpdate drives every property over every converted family in this service.
//
// A single property is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/ClearingOneFieldClearsOnlyIt/dashboard'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Api]{
		NewApi:   newPartialUpdateApi,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe: the fixture must refuse the tokens production refuses.
		// A dashboard is this service's only token-keyed create, and it needs a
		// well-formed definition so a refusal can only be the token grammar's — a
		// malformed one would make the counterweight ("and still accepts a valid one")
		// fail for a reason that has nothing to do with the token.
		CreateWithToken: func(t *testing.T, api *Api, ctx context.Context, token string) error {
			_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token: token, Definition: dashboardDefinitionSeed,
			})
			return err
		},
		StrictnessTables: []any{&Dashboard{}},
		ValidToken:       "a-valid_token1",
	})
}

// partialUpdateFamilies is the registry. Converting the next family is adding a row.
//
// 🔴 It is not left to this comment to enforce that the registry is the WHOLE set:
// TestEveryUpdateTakesADedicatedUpdateRequest (partial_update_guard_test.go) reflects
// over *Api's own Update* methods and requires each to take a request this registry
// covers, so an update added tomorrow on the full-replace shape fails on the day it is
// added rather than the day someone notices it erasing a field.
func partialUpdateFamilies() []putest.Family[*Api] {
	return []putest.Family[*Api]{dashboardFamily()}
}

// The seeded values, and the replacements the harness sends. They are constants rather
// than literals in two places because the family's seed writes them and its field table
// declares them, and the harness fails when the two disagree — which is the anti-vacuity
// control, not a formatting preference.
const (
	dashboardNameSeed        = "Original name"
	dashboardDescriptionSeed = "Original description"
	dashboardDefinitionSeed  = `{"schemaVersion":1,"widgets":[]}`

	dashboardDefinitionReplace = `{"schemaVersion":1,"widgets":[{"id":"w1"}]}`
)

func dashboardFamily() putest.Family[*Api] {
	const token = "dash-1"
	return putest.Family[*Api]{
		Name:    "dashboard",
		Token:   token,
		Migrate: []any{&Dashboard{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			name, description := dashboardNameSeed, dashboardDescriptionSeed
			if _, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token:       token,
				Name:        &name,
				Description: &description,
				Definition:  dashboardDefinitionSeed,
			}); err != nil {
				t.Fatalf("seed dashboard: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.DashboardsByToken(ctx, []string{token})
			d := putest.RequireOne(t, "dashboard", rows, err)
			return map[string]string{
				"name":        putest.NullString(d.Name),
				"description": putest.NullString(d.Description),
				// Not JSONString: that renders a NULLABLE JSON column and answers
				// NullMarker for a nil one. definition is NOT NULL and its stored bytes
				// are what the update wrote, verbatim.
				"definition": string(d.Definition),
			}
		},
		NewRequest: func() any { return new(DashboardUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			// nil precondition: the harness drives the three states, not optimistic
			// concurrency. The precondition's own behaviour — including what an update
			// naming NO field does under one — is pinned in api_partial_update_test.go,
			// because no other converted family on the platform has one.
			_, err := api.UpdateDashboard(ctx, token, req.(*DashboardUpdateRequest), nil)
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", dashboardNameSeed, "Renamed",
				func(r *DashboardUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", dashboardDescriptionSeed, "Rewritten",
				func(r *DashboardUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			// 🔴 RequiredStringField, NOT OptionalStringField. The column is NOT NULL and
			// a dashboard with no definition is not a thing, so the harness asserts that
			// an explicit null is REFUSED rather than that it clears. Declaring it the
			// other way would make the suite demand the fail-open this conversion closes.
			putest.RequiredStringField("definition", dashboardDefinitionSeed, dashboardDefinitionReplace,
				func(r *DashboardUpdateRequest) *dcgraphql.OptionalString { return &r.Definition }),
		},
	}
}
