// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func keyList(n int) []string {
	k := make([]string, n)
	for i := range k {
		k[i] = fmt.Sprintf("k-%d", i)
	}
	return k
}

// DashboardsByToken answers a key list at rdb.MaxLookupKeys and refuses one over it
// with the typed limit refusal (served as LIMIT_EXCEEDED).
func TestDashboardsByTokenIsBounded(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.DashboardsByToken(ctx, keyList(rdb.MaxLookupKeys))
	require.NoError(t, err, "a list at the bound must be answered")

	_, err = api.DashboardsByToken(ctx, keyList(rdb.MaxLookupKeys+1))
	le, ok := limit.As(err)
	require.True(t, ok, "a list over the bound must be refused with the limit refusal, got %v", err)
	require.Equal(t, rdb.MaxLookupKeys+1, le.Got)
	require.Equal(t, rdb.MaxLookupKeys, le.Max)
}

// Below the bound the lookup returns the same rows in the same order as before: the
// matching rows only, in storage (creation) order regardless of the key order, with
// unknown keys contributing nothing.
func TestDashboardsByTokenRowsAndOrder(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, tok := range []string{"a", "b", "c"} {
		_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{Token: tok, Definition: `{"schemaVersion":1}`})
		require.NoError(t, err)
	}

	found, err := api.DashboardsByToken(ctx, []string{"c", "zz", "a"})
	require.NoError(t, err)
	got := make([]string, 0, len(found))
	for _, d := range found {
		got = append(got, d.Token)
	}
	require.Equal(t, []string{"a", "c"}, got)
}
