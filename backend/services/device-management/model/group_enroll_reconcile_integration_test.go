// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// Real migrations and Postgres session/family locks, via the shared TOCTOU
// harness (DC_IT_PGHOST/PORT/USER/PASSWORD). No reachable server is needed to vet.
func TestMembershipEnrollmentReconcilePostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(6) // two session locks plus repair transactions
	base, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var contexts []context.Context
	var fixtures []enrollmentRepairFixture
	for i, pin := range []string{"rule", "access"} {
		ctx := core.WithTenant(base, fmt.Sprintf("enroll-repair-%d-%d", time.Now().UnixNano(), i))
		_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "sensor"})
		require.NoError(t, err)
		contexts = append(contexts, ctx)
		fixtures = append(fixtures, seedEnrollmentRepair(t, api, ctx, pin))
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; results <- api.ReconcileMembershipEnrollment(base) }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-base.Done():
			t.Fatal(base.Err())
		}
	}
	for i, ctx := range contexts {
		assertEnrollmentRepair(t, api, ctx, fixtures[i])
		other := fixtures[1-i]
		var rows int64
		require.NoError(t, api.RDB.DB(ctx).Model(&EntityGroupMembership{}).Where("group_id = ?", other.group.ID).Count(&rows).Error)
		require.Zero(t, rows, "another tenant's group is invisible")
		require.False(t, isMemberOf(t, api, ctx, "device", other.missing[0], fixtures[i].group.ID, fixtures[i].version), "never enroll another tenant's device")
	}
}
