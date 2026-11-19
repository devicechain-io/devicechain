// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// Run against throwaway Postgres with DC_IT_PG{HOST,PORT,USER,PASSWORD} and
// go test -tags integration ./model -run TestIntegrationHistorySnapshotInterleaving -count=1.
func TestIntegrationHistorySnapshotInterleaving(t *testing.T) {
	api, ctx := claimsPostgres(t)
	// claimsPostgres clears states/deferrals; clear the ledger as well so the
	// unpaged attempt count remains exactly one across repeated runs.
	require.NoError(t, api.RDB.DB(core.WithSystemContext(ctx)).
		Exec("TRUNCATE TABLE \"notification-management\".notification_attempts RESTART IDENTITY").Error)
	assertHistorySnapshotInterleaving(t, api, ctx)
}
