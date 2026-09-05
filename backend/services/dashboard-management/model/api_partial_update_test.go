// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE TWO THINGS ABOUT A DASHBOARD'S PARTIAL UPDATE THAT THE GENERIC HARNESS CANNOT SAY.
//
// partial_update_families_test.go drives the platform-wide properties — set one field
// and nothing else moves, clear one field and nothing else moves, name nothing and
// nothing moves — over every field this entity has. Two things are specific to this
// entity and live here:
//
//  1. `definition` is a NOT NULL column holding the dashboard's entire content, so an
//     explicit null on it is REFUSED, and refused BEFORE anything else in the same
//     request is written. The harness's required-field property drives the refusal for a
//     request that names only that field; the "and nothing else was written" half needs a
//     request that names something else too, which is what the tests below send.
//  2. the optimistic-concurrency precondition, which no other converted family has. Its
//     interesting case is the one three-state semantics newly makes reachable: an update
//     that names NO field at all.

func partialUpdateApiCtx(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	return api, ctx
}

// readDashboard reloads the row FROM THE DATABASE. Never the value an update returned: a
// resolver that mutated its in-memory copy and never persisted would satisfy every
// assertion made against its return value.
func readDashboard(t *testing.T, api *Api, ctx context.Context, token string) *Dashboard {
	t.Helper()
	found, err := api.DashboardsByToken(ctx, []string{token})
	require.NoError(t, err)
	require.Len(t, found, 1, "expected exactly one dashboard %q", token)
	return found[0]
}

// 🔴 AN EXPLICIT NULL ON definition IS REFUSED BY THE FOLD, NOT BY THE JSON VALIDATOR.
//
// The distinction is the whole test, and asserting only "there was an error" misses it. A
// fold that folded the null to "" — dcgraphql.ApplyTo instead of ApplyToRequired — would
// ALSO be refused, because "" is not valid JSON, and it would be refused by
// ErrInvalidDefinition whose own text contains the word "definition". So the obvious
// assertions (an error occurred; it mentions the field) are satisfied by a mutant that
// deletes the fold this conversion turns on, and the suite would go green having lost it.
//
// What separates them is WHICH refusal arrives. ApplyToRequired says "cannot be cleared" /
// "cannot be blank", which is a statement about the REQUEST — send a value or omit it.
// ErrInvalidDefinition says the document is not a JSON object, which is a statement about
// a document the caller never sent, and would send whoever hit it looking for a malformed
// payload that does not exist. Both refuse; only one is answerable.
//
// A whitespace-only string is refused with the null because it is the same request spelled
// differently — otherwise the API would have a second, undocumented way to reach the state
// the null was refused for.
func TestUpdateDashboard_AnExplicitNullOnDefinitionIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field util.OptionalString
		says  string
	}{
		{"explicit null", util.ClearedString(), "cannot be cleared"},
		{"whitespace only", util.OptionalStringOf("   "), "cannot be blank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := partialUpdateApiCtx(t)
			seedDashboards(t, api, ctx, "dash-a")

			_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
				Definition: tc.field,
			}, nil)
			require.Error(t, err, "a dashboard was left with no definition, successfully")
			assert.Contains(t, err.Error(), "definition",
				"the refusal must name the field the caller can act on")
			assert.Contains(t, err.Error(), tc.says,
				"the refusal did not come from the required-field fold — the request was "+
					"refused for what it produced rather than for what it asked")
			assert.NotErrorIs(t, err, ErrInvalidDefinition,
				"an unsendable request was reported as a malformed document, which sends the "+
					"caller looking for a payload they never sent")

			assert.JSONEq(t, defV1, string(readDashboard(t, api, ctx, "dash-a").Definition),
				"the refused update still moved the stored definition")
		})
	}
}

// 🔴 A REFUSED DEFINITION REFUSES THE WHOLE UPDATE, and this is the half the generic
// required-field property cannot reach: it sends only the offending field, so "nothing
// else was written" is a tautology about a request that asked for nothing else. Here the
// same request also carries a rename, which is what a real editor sends — and applying
// the name before failing on the document would leave a caller who retries having
// already half-applied their first attempt, with nothing anywhere to say so.
func TestUpdateDashboard_ARefusedDefinitionLeavesTheRenameUnwritten(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field util.OptionalString
	}{
		{"explicit null", util.ClearedString()},
		{"not json", util.OptionalStringOf("}{")},
		{"a well-formed non-object", util.OptionalStringOf(`[{"id":"w1"}]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := partialUpdateApiCtx(t)
			seedDashboards(t, api, ctx, "dash-a")

			_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
				Name:       util.OptionalStringOf("Renamed"),
				Definition: tc.field,
			}, nil)
			require.Error(t, err)

			got := readDashboard(t, api, ctx, "dash-a")
			assert.Equal(t, "Original dash-a", got.Name.String,
				"the rename was applied before the definition was refused — a retry now "+
					"starts from a half-applied first attempt")
			assert.JSONEq(t, defV1, string(got.Definition))
		})
	}
}

// The counterweight to the refusals above: an OMITTED definition is left alone, so a
// caller may rename a dashboard without resending its document. Without this, refusing
// every definition would satisfy the two tests above perfectly.
func TestUpdateDashboard_AnOmittedDefinitionIsLeftAlone(t *testing.T) {
	api, ctx := partialUpdateApiCtx(t)
	seedDashboards(t, api, ctx, "dash-a")

	_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
		Name: util.OptionalStringOf("Renamed"),
	}, nil)
	require.NoError(t, err, "a rename that did not resend the definition was refused")

	got := readDashboard(t, api, ctx, "dash-a")
	assert.Equal(t, "Renamed", got.Name.String)
	assert.JSONEq(t, defV1, string(got.Definition), "the omitted definition was overwritten")
}

// 🔴 A MALFORMED REQUEST IS REFUSED AS MALFORMED, EVEN WHEN THE PRECONDITION IS STALE.
//
// The folds and the version check are both refusals, so the ORDER they run in decides
// which error a caller carrying both problems is told about — and the two send that
// caller to opposite places. ErrConflict means "reload and re-apply", which is advice a
// client can act on and which, for a request that can never succeed, is an instruction to
// loop forever. The fold's refusal means "change what you sent".
//
// So the folds run first, and this is what says so: moving the RFC3339Nano compare above
// them passes every other test in this package and in graphql/, because no other test
// sends a request that is BOTH stale and unsendable. That combination is the missing input
// class, not a missing assertion.
//
// Its counterweight is TestUpdateOptimisticConcurrency, where a WELL-FORMED request under
// a stale precondition is still ErrConflict — without which this could be satisfied by a
// precondition that had stopped being checked at all.
func TestUpdateDashboard_AMalformedRequestIsRefusedAsMalformedNotAsAConflict(t *testing.T) {
	const stale = "2000-01-01T00:00:00Z"
	for _, tc := range []struct {
		name  string
		field util.OptionalString
		says  string
	}{
		{"explicit null", util.ClearedString(), "cannot be cleared"},
		{"whitespace only", util.OptionalStringOf("   "), "cannot be blank"},
		{"not json", util.OptionalStringOf("}{"), ErrInvalidDefinition.Error()},
		{"a well-formed non-object", util.OptionalStringOf(`[{"id":"w1"}]`), ErrInvalidDefinition.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, ctx := partialUpdateApiCtx(t)
			seedDashboards(t, api, ctx, "dash-a")
			expected := stale

			_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
				Name:       util.OptionalStringOf("Renamed"),
				Definition: tc.field,
			}, &expected)
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrConflict,
				"a request that can never succeed was reported as a conflict, which tells the "+
					"caller to reload and try again — forever")
			assert.Contains(t, err.Error(), tc.says)

			got := readDashboard(t, api, ctx, "dash-a")
			assert.Equal(t, "Original dash-a", got.Name.String,
				"the refused update still applied the rename")
			assert.JSONEq(t, defV1, string(got.Definition))
		})
	}
}

// ─── the empty update, under and without a precondition ────────────────────

// stampOf is the precondition string the GraphQL layer hands a client — built with the
// SAME function (core/graphql.FormatTime) rather than a layout re-spelled here, since
// the read formatter and UpdateDashboard's comparison are one contract.
func stampOf(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	return *util.FormatTime(readDashboard(t, api, ctx, token).UpdatedAt)
}

// 🔴 AN EMPTY UPDATE IS NOT A WAY TO BE TOLD "SUCCESS" ABOUT A ROW THAT HAS MOVED ON.
//
// Three-state semantics newly make "name no field at all" a reachable request, and the
// obvious implementation degenerates: with nothing to write there is no guarded write to
// match zero rows, so the precondition would be enforced by nothing. The version check
// therefore runs whether or not any field was named, and this is what says so.
func TestUpdateDashboard_AnEmptyUpdateUnderAStalePreconditionIsAConflict(t *testing.T) {
	api, ctx := partialUpdateApiCtx(t)
	seedDashboards(t, api, ctx, "dash-a")

	stale := "2000-01-01T00:00:00Z"
	_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{}, &stale)
	assert.ErrorIs(t, err, ErrConflict,
		"an update naming nothing was told its stale precondition still held")
}

// And with a CURRENT precondition it succeeds and writes nothing — including updated_at.
//
// 🔴 THE TIMESTAMP IS THE POINT. It is a SHARED precondition: bumping it on behalf of a
// caller who asked for no change would invalidate every other editor's baseline, and the
// second tab would be told it had a conflict with a write that wrote nothing. The value
// returned is the row AS READ, so the caller's own baseline does not move either.
func TestUpdateDashboard_AnEmptyUpdateUnderACurrentPreconditionWritesNothing(t *testing.T) {
	api, ctx := partialUpdateApiCtx(t)
	seedDashboards(t, api, ctx, "dash-a")
	before := stampOf(t, api, ctx, "dash-a")

	returned, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{}, &before)
	require.NoError(t, err, "an update naming nothing was reported as a conflict with itself")
	assert.Equal(t, before, *util.FormatTime(returned.UpdatedAt),
		"the returned baseline moved though nothing was written")

	assert.Equal(t, before, stampOf(t, api, ctx, "dash-a"),
		"an update naming nothing moved updated_at, invalidating every other editor's baseline")
	got := readDashboard(t, api, ctx, "dash-a")
	assert.Equal(t, "Original dash-a", got.Name.String)
	assert.JSONEq(t, defV1, string(got.Definition))
}

// The same, with no precondition at all: last-write-wins still writes nothing when
// nothing was named.
func TestUpdateDashboard_AnEmptyUpdateWithNoPreconditionWritesNothing(t *testing.T) {
	api, ctx := partialUpdateApiCtx(t)
	seedDashboards(t, api, ctx, "dash-a")
	before := stampOf(t, api, ctx, "dash-a")

	_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{}, nil)
	require.NoError(t, err)

	assert.Equal(t, before, stampOf(t, api, ctx, "dash-a"))
	assert.Equal(t, "Original dash-a", readDashboard(t, api, ctx, "dash-a").Name.String)
}

// 🔴 THE COUNTERWEIGHT TO EVERY PRECONDITION TEST ABOVE: a write that DOES name a field
// advances the baseline, so replaying the precondition it just consumed is a conflict.
//
// Without this the "nothing moved" assertions could all be satisfied by an updated_at
// that never moves at all — at which case the precondition would never reject anything
// and the optimistic-concurrency contract would be decoration. It also pins the one
// thing the guarded write DELEGATES: the map-form UPDATE relies on gorm to stamp
// updated_at, and if that ever stopped, two saves from the same stale view would both
// succeed.
func TestUpdateDashboard_ANonEmptyGuardedWriteAdvancesTheBaseline(t *testing.T) {
	api, ctx := partialUpdateApiCtx(t)
	seedDashboards(t, api, ctx, "dash-a")
	first := stampOf(t, api, ctx, "dash-a")

	_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
		Name: util.OptionalStringOf("Renamed"),
	}, &first)
	require.NoError(t, err)

	second := stampOf(t, api, ctx, "dash-a")
	require.NotEqual(t, first, second, "a write that changed the row did not move updated_at")

	// Replaying the consumed precondition is now a conflict.
	_, err = api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
		Name: util.OptionalStringOf("Renamed again"),
	}, &first)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Equal(t, "Renamed", readDashboard(t, api, ctx, "dash-a").Name.String,
		"the refused write was applied anyway")
}
