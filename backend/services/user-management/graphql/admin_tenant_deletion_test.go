// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/purge"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantDeletionQueriesFailClosed holds both new queries to the plane's rule: an
// unauthenticated caller is refused, a caller with the wrong authority is refused.
//
// It deliberately stops at the authority check rather than going on to a success case. The
// success leg would need an admin Service in context — these resolvers do reach it — and the
// behaviour behind it is covered where the ledger lives, in the admin package. What this file
// is for is the gate.
func TestTenantDeletionQueriesFailClosed(t *testing.T) {
	r := &AdminResolver{}

	t.Run("tenantDeletion", func(t *testing.T) {
		args := struct {
			Token string
			Epoch *string
		}{Token: "acme"}

		_, err := r.TenantDeletion(context.Background(), args)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)

		_, err = r.TenantDeletion(adminCtx(string(auth.UserWrite)), args)
		assert.ErrorIs(t, err, auth.ErrForbidden,
			"a deletion record names a tenant and what was erased for it; it must not be readable "+
				"by an identity holding only unrelated authorities")
	})

	t.Run("tenantDeletions", func(t *testing.T) {
		var args struct {
			Completed *bool
			Limit     *int32
			Offset    *int32
		}

		_, err := r.TenantDeletions(context.Background(), args)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)

		_, err = r.TenantDeletions(adminCtx(string(auth.UserWrite)), args)
		assert.ErrorIs(t, err, auth.ErrForbidden)
	})
}

// TestABadEpochIsRejectedBeforeAnyLookup pins that a malformed epoch argument is an argument
// error rather than a lookup that quietly matches nothing.
//
// It runs with a valid authority and NO admin service in context: if the resolver reached the
// service it would panic on the nil type assertion, so surviving proves the parse happens
// first. That is worth pinning because the alternative — parsing to the zero time and querying
// with it — returns "no such record", which reads to a caller as "this deletion does not
// exist" when what actually happened is that they sent a bad timestamp.
func TestABadEpochIsRejectedBeforeAnyLookup(t *testing.T) {
	bad := "not-a-timestamp"
	_, err := (&AdminResolver{}).TenantDeletion(adminCtx(string(auth.TenantRead)), struct {
		Token string
		Epoch *string
	}{Token: "acme", Epoch: &bad})

	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrForbidden)
}

// TestTheDeletionWireContractIsStable pins the field names and nullability the console binds
// to, through introspection rather than through the Go resolver names.
//
// The two can disagree: graphql-go maps `rowsErased` to a method called `RowsErased`, so a
// renamed SCHEMA field still compiles and still has a resolver — it simply stops being the
// field the console asks for. Introspection is the only place that mismatch shows up.
func TestTheDeletionWireContractIsStable(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "TenantDeletion") { fields { name type { kind name } } } }`, "", nil)
	require.Empty(t, res.Errors)

	var out struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Type struct {
					Kind string  `json:"kind"`
					Name *string `json:"name"`
				} `json:"type"`
			} `json:"fields"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))

	kinds := map[string]string{}
	for _, f := range out.Type.Fields {
		kinds[f.Name] = f.Type.Kind
	}

	// Non-null: a deletion record always has these, and a console rendering them has nothing
	// above to inherit a null from.
	for _, f := range []string{"token", "epoch", "rowsErased", "stores", "blockedBy", "awaiting"} {
		assert.Equalf(t, "NON_NULL", kinds[f], "%s must be non-null", f)
	}
	// Nullable, and each null MEANS something the console renders differently.
	assert.Equal(t, "SCALAR", kinds["completedAt"],
		"null completedAt is what 'still in flight' looks like")
	assert.Equal(t, "SCALAR", kinds["elapsesAt"],
		"null elapsesAt is what 'this wait is not a window' looks like, which is the STORES case")
}

// TestTheDeletionWaitEnumMatchesTheGoVocabulary keeps the schema's enum and purge.Wait from
// drifting apart.
//
// 🔴 THEY ARE DECLARED IN TWO PLACES AND NOTHING ELSE MAKES THEM AGREE. The resolver returns
// `string(progress.Awaiting)` — an unexported Go constant — straight into a GraphQL enum. Add
// a fourth wait in Go and the schema keeps validating; the resolver then emits a value the
// enum does not declare, and graphql-go fails the FIELD at request time, on exactly the
// deletion in the state nobody tested.
func TestTheDeletionWaitEnumMatchesTheGoVocabulary(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "DeletionWait") { enumValues { name } } }`, "", nil)
	require.Empty(t, res.Errors)

	var out struct {
		Type struct {
			EnumValues []struct {
				Name string `json:"name"`
			} `json:"enumValues"`
		} `json:"__type"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))

	declared := map[string]bool{}
	for _, v := range out.Type.EnumValues {
		declared[v.Name] = true
	}
	for _, w := range []purge.Wait{purge.WaitStores, purge.WaitSettle, purge.WaitTokenHold, purge.WaitNone} {
		assert.Truef(t, declared[string(w)],
			"purge.Wait %q is not declared in the DeletionWait enum, so a deletion in that state "+
				"would fail the awaiting field at request time", w)
	}
	assert.Len(t, out.Type.EnumValues, 4,
		"the enum declares a value Go can never produce; a console would branch on a state that "+
			"cannot occur, and the dead branch would never be noticed")
}

// TestBlockedByIsAnEmptySliceRatherThanNil pins the serialization of "nothing is blocking".
//
// The field is [String!]!, so a nil slice would render as null against a non-null type — a
// GraphQL error on the healthiest possible deletion. It is a one-line guard in the resolver
// and exactly the kind that gets tidied away by someone who sees `if x == nil { return []T{} }`
// and reads it as redundant.
func TestBlockedByIsAnEmptySliceRatherThanNil(t *testing.T) {
	r := &AdminTenantDeletionResolver{progress: purge.Progress{Awaiting: purge.WaitNone}}
	require.NotNil(t, r.BlockedBy())
	assert.Empty(t, r.BlockedBy())
}

// TestTheEpochRendersIdenticallyOnBothSurfaces is the reason formatPurgeTime exists as one
// function.
//
// The console correlates the purging badge on a tenant with the record in the deletion
// history, and the epoch is what joins them. Two call sites formatting "the same way" is the
// arrangement in which one later gains a nanosecond and the join silently stops matching.
func TestTheEpochRendersIdenticallyOnBothSurfaces(t *testing.T) {
	epoch := time.Date(2026, 8, 4, 12, 34, 56, 789_000_000, time.UTC)

	onTenant := (&AdminTenantResolver{M: iam.Tenant{PurgeEpoch: &epoch}}).PurgeEpoch()
	onRecord := (&AdminTenantDeletionResolver{M: iam.TenantPurge{Epoch: epoch}}).Epoch()

	require.NotNil(t, onTenant)
	assert.Equal(t, *onTenant, onRecord,
		"the epoch is half of a deletion's identity; the two surfaces that publish it must "+
			"produce the same string or a client cannot join them")
}

// TestAZeroTimestampRendersAsNull covers the ledger line that has never been attempted.
// Rendering Go's zero time would put "0001-01-01T00:00:00Z" in front of an operator as if
// something had happened then.
func TestAZeroTimestampRendersAsNull(t *testing.T) {
	r := &AdminTenantDeletionStoreResolver{M: iam.TenantPurgeStore{Store: "rdb"}}
	assert.Nil(t, r.AttemptedAt())
	assert.Nil(t, r.CleanSince())
	assert.Nil(t, r.Retaining())
	assert.Nil(t, r.LastError())
	assert.Nil(t, r.Note())
}

// TestTheEpochSurvivesAFormatParseRoundTrip drives the REAL formatter, which is the thing the
// admin-package round-trip test cannot do from another package.
//
// 🔑 That other test formats with `Format(time.RFC3339Nano)` written out in the test body — a
// reconstruction of this function, not a call to it — so dropping precision HERE leaves it
// green. This one is the assertion that bites: the API publishes the epoch as half of an
// identifier and looks a record up by exact match, so anything this function loses is an
// identifier that no longer identifies.
func TestTheEpochSurvivesAFormatParseRoundTrip(t *testing.T) {
	// A sub-second component, because that is the precision at risk. The epoch is
	// time.Now().UTC() at the cut and is truncated nowhere.
	epoch := time.Date(2026, 8, 4, 12, 34, 56, 123_456_000, time.UTC)

	published := formatPurgeTime(epoch)
	parsed, err := time.Parse(time.RFC3339Nano, published)
	require.NoError(t, err)

	assert.True(t, parsed.Equal(epoch),
		"the epoch this API publishes must parse back to the same instant; %q lost precision "+
			"against %v, so a caller handing it back would match no record", published, epoch)
}
