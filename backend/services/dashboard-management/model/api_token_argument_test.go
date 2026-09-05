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

// WHICH DASHBOARD AN UPDATE WRITES. The `token` argument, and nothing else.
//
// # 🔴 THE RULE THIS FILE USED TO DRIVE IS GONE, AND SO ARE THE TESTS THAT DROVE IT
//
// updateDashboard shared its create input, so it carried the token TWICE: once as the
// argument saying which dashboard, once inside the payload. The payload one used to be
// written onto the row, so an empty token — legal, since `token: String!` admits "" —
// blanked the dashboard's token and left it addressable by nothing, successfully. That
// was fixed by RECONCILING the two (dcgraphql.ErrPayloadTokenDisagrees): the argument
// named the record and a disagreeing payload was refused.
//
// The conversion to DashboardUpdateRequest removes the field instead. There is no
// second token to disagree, so a request naming another dashboard is UNREPRESENTABLE
// rather than refused, and the two tests that drove the refusal — one for a
// disagreement, one for an empty payload token — would now be asserting something the
// type system says. They are deleted rather than left iterating over a state that can no
// longer be built: a test that cannot fail is the most convincing kind of green there is.
//
// Nothing was lost with the field. Unlike a connector, a notification channel or an AI
// provider — each of which keys a stored secret by the record's immutable id and takes
// the RENAME rule so the binding survives — a dashboard has no secret, nothing
// references it by token, and no test ever pinned a rename as intended. The one labelled
// "rename" sent the SAME token. A dashboard has no rename channel today, and this
// conversion did not invent one.
//
// What is left here is the half the type system CANNOT say: that the token argument is
// what the guarded write reloads by. The exhaustiveness guard over this service's whole
// update surface lives in partial_update_guard_test.go.

const defV1 = `{"schemaVersion":1,"widgets":[]}`

func seedDashboards(t *testing.T, api *Api, ctx context.Context, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
			Token: tok, Name: strp("Original " + tok), Definition: defV1,
		})
		require.NoError(t, err, "seed %s", tok)
	}
}

// 🔴 THE GUARDED-WRITE PATH RELOADS, AND IT USED TO RELOAD BY THE PAYLOAD TOKEN — which,
// once the reconcile made that token legitimately empty, reported a dashboard that had
// just been written successfully as not-found. The field is gone now, so the reload has
// nothing else it could use; this is what keeps that true. It is the half a test on the
// unconditional path cannot see, because the two paths return through different code.
//
// TWO dashboards are seeded, because with one "the other was untouched" is vacuous and a
// lookup falling back to "the only row there is" would pass.
func TestUpdateDashboard_TheGuardedPathReloadsByTheArgument(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDashboards(t, api, ctx, "dash-a", "dash-b")

	found, err := api.DashboardsByToken(ctx, []string{"dash-a"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	stamp := *util.FormatTime(found[0].UpdatedAt)

	updated, err := api.UpdateDashboard(ctx, "dash-a", &DashboardUpdateRequest{
		Name: util.OptionalStringOf("Renamed"),
	}, &stamp)
	require.NoError(t, err, "the guarded write was refused")
	assert.Equal(t, "dash-a", updated.Token)
	assert.Equal(t, "Renamed", updated.Name.String)

	other, err := api.DashboardsByToken(ctx, []string{"dash-b"})
	require.NoError(t, err)
	require.Len(t, other, 1)
	assert.Equal(t, "Original dash-b", other[0].Name.String,
		"the update reached a dashboard its argument did not name")
}
