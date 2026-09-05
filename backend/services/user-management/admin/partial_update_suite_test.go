// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
)

// THIS SERVICE'S ADMIN-PLANE HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness check live in core's
// rdb/partialupdatetest, because the three-state update semantic is one platform rule
// and a per-service copy of the harness is the per-family copy one level up. Read that
// package's harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service, the
// context its store runs under, and the registry of converted families
// (partial_update_families_test.go).
//
// 🔴 THERE IS A SECOND SUITE, IN THE identity PACKAGE, and it is not an oversight.
// user-management's five converted mutations sit behind TWO application services —
// *admin.Service for the four admin-plane catalogs, *identity.Manager for the
// self-service profile edit — and the harness's Family is parameterised by the service
// type precisely so a family's closures compile against the real methods. One suite
// cannot span two receivers without erasing that, and the exhaustiveness guard reflects
// over ONE service type per call, so a merged suite would leave one of the two update
// surfaces unenumerated.

// newPartialUpdateService builds a SQLite-backed admin Service migrated for one family.
// The database half — the named shared-cache DSN, the pool close, the token-grammar
// registration, and the reasons all three are load-bearing — is putest.NewSQLiteDB's.
//
// The two purge windows are the same throwaway values the rest of this package's tests
// use: nothing in the update paths reads them, and passing zero would make a future
// deletion-progress family silently report "nothing outstanding".
func newPartialUpdateService(t *testing.T, tables ...any) *Service {
	t.Helper()
	db := putest.NewSQLiteDB(t, tables...)
	return NewService(iam.NewStore(&rdb.RdbManager{Database: db}), 300*time.Second, 12*time.Hour, nil)
}

// partialUpdateTenant is the tenant context the families run under.
//
// 🔴 THE iam ENTITIES ARE NOT TENANT-SCOPED — they are the control plane, read and
// written through the store's system context — so nothing here actually needs a tenant.
// One is supplied anyway because the fail-closed tenant-scope callback is registered in
// the fixture exactly as production registers it, and a suite that ran without a tenant
// would be relying on "none of these tables happens to be scoped" staying true.
const partialUpdateTenant = "acme"

// Local spellings of the harness's shared readings, aliased rather than
// re-implemented: a second definition here is how the family tables and the harness
// come to disagree about what "cleared" looks like.
const nullMarker = putest.NullMarker

var (
	nullStr    = putest.NullString
	boolStr    = putest.BoolString
	floatStr   = putest.FloatString
	intStr     = putest.IntString
	renderList = putest.RenderStringList
)

// configJSON renders a config map the way the family tables spell it.
//
// 🔴 IT MARSHALS THE MAP RATHER THAN RE-READING THE COLUMN'S RAW TEXT, so the seeded
// literal in a family table and this reading have to agree on key ORDER — which is why
// every config fixture in this package is a SINGLE-key object. Go's json.Marshal sorts
// map keys, so a multi-key fixture would still be deterministic, but the seeded literal
// would then have to be written in sorted order to match, and a fixture that fails for
// that reason reads as a partial-update defect.
func configJSON(cfg map[string]any) string {
	if len(cfg) == 0 {
		return nullMarker
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		panic("rendering a config map: " + err.Error())
	}
	return string(out)
}

// nullBoolStr renders a NULLABLE boolean column, keeping "cleared" distinct from
// "false". They are different rows, and a reading that conflated them would report the
// consent flag as cleared when it had merely been turned off — which for
// aiExternalEnabled is the difference between "this tenant declares nothing" and "this
// tenant was considered and declined".
func nullBoolStr(v *bool) string {
	if v == nil {
		return nullMarker
	}
	return boolStr(*v)
}

// nullIntStr / nullFloatStr render the nullable governance columns. nil is the
// load-bearing state — "no override, inherit the tier then the platform default, never
// unlimited" — so it must not read as "0".
func nullIntStr(v *int) string {
	if v == nil {
		return nullMarker
	}
	return intStr(int32(*v))
}

func nullFloatStr(v *float64) string {
	if v == nil {
		return nullMarker
	}
	return floatStr(*v)
}

// ─── field constructors core does not provide ──────────────────────────────
//
// Each is a shape core's rdb/partialupdatetest has no constructor for, built from the
// same exported Field struct rather than by widening core — which is deliberate: this
// slice consumes that package while it is still in review, and a service reaching in to
// add a constructor for its own fixture is how a shared harness acquires per-service
// special cases.

// optionalBoolField describes a NULLABLE boolean column: settable, and CLEARABLE back to
// NULL.
//
// 🔴 core has RequiredBoolField and deliberately no clearable counterpart, on the
// reasoning that folding a null to `false` is the quietest possible data loss — false is
// a value a caller could legitimately have sent. That reasoning is about a NOT NULL
// column. aiExternalEnabled's column is nullable and its three readings are distinct
// (null = never decided, false = declined, true = consented), so clearing it is a real
// state and not a fold onto a zero value.
func optionalBoolField[R any](name string, seeded bool,
	pick func(*R) *dcgraphql.OptionalBool) putest.Field {
	return putest.Field{
		Name: name, Seeded: boolStr(seeded), Replace: boolStr(!seeded), Cleared: nullMarker,
		Kind:    putest.Clearable,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalBoolOf(v == "true") },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedBool() },
	}
}

// emptiableStringField describes a NOT NULL string column whose EMPTY value is a
// legitimate state — a tier's color, where "" means "no pill". Clearable, but its
// cleared reading is "" rather than NullMarker: there is no NULL for it to reach.
//
// See patch.EmptiableString for why this is not ApplyToRequired, which would refuse the
// clear outright and remove a capability the create path has always had.
func emptiableStringField[R any](name, seeded, replace string,
	pick func(*R) *dcgraphql.OptionalString) putest.Field {
	return putest.Field{
		Name: name, Seeded: seeded, Replace: replace, Cleared: "",
		Kind:    putest.Clearable,
		Set:     func(req any, v string) { *pick(req.(*R)) = dcgraphql.OptionalStringOf(v) },
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedString() },
	}
}

// requiredStringListField describes a `[String!]` column that may be REPLACED but never
// EMPTIED — an OAuth client's redirect allowlist or its scope set.
//
// 🔴 IT IS RequiredValue, NOT Clearable, AND core's OptionalStringListField CANNOT
// express it: that constructor hard-codes Clearable and installs a SetEmpty, so the
// harness would assert the list can be emptied and that [] and null agree in doing so.
// Here they agree in being REFUSED, which is a different claim and is driven by
// ARequiredFieldRefusesAnExplicitNull plus this package's own
// TestEmptyListIsRefusedForAnOAuthClientsAllowlists — the [] spelling has no property in
// the shared harness for a required list, so it needs one here.
func requiredStringListField[R any](name string, seeded, replace []string,
	pick func(*R) *dcgraphql.OptionalStringList) putest.Field {
	if len(seeded) == 0 {
		panic("partial update: list field " + name + " is seeded empty, so \"the update " +
			"preserved it\" and \"it was never set\" are the same observation")
	}
	return putest.Field{
		Name: name, Seeded: renderList(seeded), Replace: renderList(replace),
		Kind: putest.RequiredValue,
		Set: func(req any, v string) {
			*pick(req.(*R)) = dcgraphql.OptionalStringListOf(putest.ParseStringList(v))
		},
		SetNull: func(req any) { *pick(req.(*R)) = dcgraphql.ClearedStringList() },
	}
}

// TestPartialUpdate drives every property over every converted admin-plane family.
//
// A single property or family is still addressable:
//
//	go test ./admin -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/tenant'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Service]{
		NewApi:   newPartialUpdateService,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe. A ROLE is used because it is the cheapest token-keyed
		// create in this service — one table, no references, no tier to resolve — so a
		// refusal can only be the token grammar's. An OAuth client would not do: its
		// identifier is a client_id, which has its own charset check and no Token column
		// for the grammar callback to see at all.
		CreateWithToken: func(t *testing.T, s *Service, ctx context.Context, token string) error {
			_, err := s.CreateRole(ctx, RoleInput{Scope: string(iam.ScopeSystem), Token: token})
			return err
		},
		StrictnessTables: []any{&iam.Role{}},
		ValidToken:       "a-valid_token1",
	})
}
