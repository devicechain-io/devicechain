// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFleetOperationCatalogDecision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
		err    error
		want   string
	}{
		{"missing", nil, nil, "NOT_INSTALLED"},
		{"always", []string{"A"}, nil, "ENFORCED"},
		{"disabled", []string{"D"}, nil, "DISABLED"},
		{"ordinary", []string{"O"}, nil, "DISABLED"},
		{"replica", []string{"R"}, nil, "DISABLED"},
		{"unreadable", nil, errors.New("catalog denied"), "UNREADABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fleetOperationEnforcement(func() ([]string, error) { return tc.states, tc.err })
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReadinessCountsOrphanedGenerationHeads(t *testing.T) {
	api, ctx := configTestApi(t)
	head := AttributeGeneration{DeviceId: 999999, Scope: "SHARED", AttrKey: "head-only", Generation: 1}
	require.NoError(t, api.RDB.DB(ctx).Create(&head).Error)
	n, err := api.CountOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}
