// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDashboardNameRefusesOversizedInputBeforeQueries(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	queries := 0
	cb := api.RDB.Database.Callback().Query()
	require.NoError(t, cb.Before("gorm:query").Register("test:dashboard_name_queries", func(*gorm.DB) {
		queries++
	}))
	t.Cleanup(func() { require.NoError(t, cb.Remove("test:dashboard_name_queries")) })

	for _, tc := range []struct{ name, text string }{
		{"ASCII", strings.Repeat("x", 257)},
		{"UTF8", strings.Repeat("\u00e9", 129)},
		{"whitespace", strings.Repeat(" ", 257)},
		{"padded", strings.Repeat(" ", 256) + "pump"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries = 0
			found, err := api.Dashboards(ctx, DashboardSearchCriteria{Name: &tc.text})
			var refusal *limit.Error
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, "name bytes", refusal.What)
			require.Equal(t, len(tc.text), refusal.Got)
			require.Equal(t, 256, refusal.Max)
			require.Equal(t, "LIMIT_EXCEEDED", refusal.Extensions()["code"])
			require.Nil(t, found)
			require.Zero(t, queries, "refusal must precede COUNT and data queries")
		})
	}

	// Prove the observer sees both ordinary COUNT and data queries.
	queries = 0
	found, err := api.Dashboards(ctx, DashboardSearchCriteria{})
	require.NoError(t, err)
	require.Empty(t, found.Results)
	require.GreaterOrEqual(t, queries, 2)
}

func TestDashboardNameAcceptsByteBoundary(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	for _, tc := range []struct{ token, name string }{
		{"ascii-boundary", strings.Repeat("x", 256)},
		{"utf8-boundary", strings.Repeat("\u00e9", 128)},
	} {
		t.Run(tc.token, func(t *testing.T) {
			require.Len(t, tc.name, 256)
			_, err := api.CreateDashboard(ctx, &DashboardCreateRequest{
				Token: tc.token, Name: &tc.name, Definition: `{"schemaVersion":1}`,
			})
			require.NoError(t, err)
			found, err := api.Dashboards(ctx, DashboardSearchCriteria{Name: &tc.name})
			require.NoError(t, err)
			require.Len(t, found.Results, 1)
			require.Equal(t, tc.token, found.Results[0].Token)
		})
	}
}
