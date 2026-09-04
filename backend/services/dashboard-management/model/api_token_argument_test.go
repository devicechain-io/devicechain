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

// WHICH DASHBOARD AN UPDATE WRITES.
//
// updateDashboard still takes its create input, so it carries the token twice: once
// as the argument that says which dashboard, once inside the payload. It used to
// write the payload one onto the row, so an empty token — legal, since
// `token: String!` admits "" — blanked the dashboard's token and left it addressable
// by nothing, successfully.
//
// It takes the RECONCILE rather than the rename rule, unlike its versioned siblings
// (connector, ai provider) and unlike a notification channel. Those three each key a
// secret by the record's immutable id and say in their own comments that a rename is
// meant to keep it bound; a dashboard has no secret, nothing references it by token,
// and nothing pins a rename — the test in api_test.go labelled "rename" sends the
// SAME token, which is a relabel waiting to mislead someone.

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

// TWO dashboards, because with one "the other was untouched" is vacuous and a lookup
// falling back to "the only row there is" would pass.
func TestUpdateDashboard_ADisagreeingPayloadTokenIsRefused(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDashboards(t, api, ctx, "dash-a", "dash-b")

	_, err := api.UpdateDashboard(ctx, "dash-a", &DashboardCreateRequest{
		Token: "dash-b", Name: strp("Hijacked"), Definition: defV1,
	}, nil)
	require.Error(t, err, "an update whose payload named a different dashboard was accepted")

	for _, tok := range []string{"dash-a", "dash-b"} {
		found, ferr := api.DashboardsByToken(ctx, []string{tok})
		require.NoError(t, ferr)
		require.Len(t, found, 1, "%s missing after a refused update", tok)
		assert.Equal(t, "Original "+tok, found[0].Name.String, "the refused update still changed %s", tok)
	}
}

// The defect that shipped: an empty payload token blanked the row.
func TestUpdateDashboard_AnEmptyPayloadTokenDoesNotBlankTheRow(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDashboards(t, api, ctx, "dash-a")

	updated, err := api.UpdateDashboard(ctx, "dash-a", &DashboardCreateRequest{
		Name: strp("Renamed"), Definition: defV1,
	}, nil)
	require.NoError(t, err, "an update with no payload token was refused")
	assert.Equal(t, "dash-a", updated.Token)

	found, ferr := api.DashboardsByToken(ctx, []string{"dash-a"})
	require.NoError(t, ferr)
	require.Len(t, found, 1, "the dashboard is no longer findable by its own token")
	assert.Equal(t, "Renamed", found[0].Name.String, "the edit did not apply")
}

// 🔴 THE GUARDED-WRITE PATH RELOADS, and it used to reload by the PAYLOAD token.
// With the reconcile in place that token is legitimately empty, so reloading by it
// would report a dashboard that was just written successfully as not-found. This is
// the half a test on the unconditional path cannot see — the two paths return through
// different code.
func TestUpdateDashboard_TheGuardedPathReloadsByTheArgument(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedDashboards(t, api, ctx, "dash-a")

	found, err := api.DashboardsByToken(ctx, []string{"dash-a"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	stamp := *util.FormatTime(found[0].UpdatedAt)

	updated, err := api.UpdateDashboard(ctx, "dash-a", &DashboardCreateRequest{
		Name: strp("Renamed"), Definition: defV1,
	}, &stamp)
	require.NoError(t, err, "the guarded write with no payload token was refused")
	assert.Equal(t, "dash-a", updated.Token)
	assert.Equal(t, "Renamed", updated.Name.String)
}
