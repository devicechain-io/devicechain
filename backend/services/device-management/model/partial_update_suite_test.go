// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// THIS SERVICE'S HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness check live in
// core's rdb/partialupdatetest, because the three-state update semantic is one platform
// rule and a per-service copy of the harness is the per-family copy one level up — the
// eighth copy certifies less than the first, and every one of them is green. Read that
// package's harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service's
// Api, the tenant it runs under, and the registry of converted families
// (partial_update_families_test.go).

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family. The database
// half — the named shared-cache DSN, the pool close, the token-grammar registration, and
// the reasons all three are load-bearing — is putest.NewSQLiteDB's.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	return NewApi(&rdb.RdbManager{Database: putest.NewSQLiteDB(t, tables...)})
}

// partialUpdateTenant is the one place this service's fixture tenant is spelled. Both the
// suite below and the standalone fixtures that call partialUpdateCtx read it, so the two
// cannot drift onto different tenants — which under the fail-closed tenant-scope callback
// would surface as a family that seeds into one database and reads from another.
const partialUpdateTenant = "acme"

func partialUpdateCtx() context.Context {
	return putest.TenantContext(partialUpdateTenant)()
}

// Local spellings of the harness's shared readings. They are aliases rather than
// re-implementations: a family's read() calls them once per column, and the harness
// compares the rendered strings, so a second definition here would be a way for the two
// halves to disagree about what "cleared" looks like.
const nullMarker = putest.NullMarker

var (
	nullStr  = putest.NullString
	jsonStr  = putest.JSONString
	boolStr  = putest.BoolString
	floatStr = putest.FloatString
	intStr   = putest.IntString
)

// requireOne is the reload guard every family's read shares: exactly one row, or the
// test is measuring something other than what it seeded.
func requireOne[E any](t *testing.T, what string, rows []*E, err error) *E {
	t.Helper()
	return putest.RequireOne(t, what, rows, err)
}

// TestPartialUpdate drives every property over every converted family in this service.
//
// A single property or family is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/device'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Api]{
		NewApi:   newPartialUpdateApi,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe. An entity group is used because it is the cheapest
		// token-keyed create in this service — one table, no references — so a refusal
		// can only be the token grammar's.
		CreateWithToken: func(t *testing.T, api *Api, ctx context.Context, token string) error {
			_, err := api.CreateEntityGroup(ctx, &EntityGroupCreateRequest{
				Token: token, MemberType: "device",
			})
			return err
		},
		StrictnessTables: []any{&EntityGroup{}},
		ValidToken:       "a-valid_token1",
	})
}
