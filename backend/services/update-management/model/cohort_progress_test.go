// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCohortProgressAndSearchIsolation(t *testing.T) {
	e := newRolloutEnv(t, 0)
	e.api.RolloutSafety = true
	ctx := tenantCtx("acme")
	plan := make([]RolloutCohort, 20)
	for i := range plan {
		plan[i] = RolloutCohort{Name: fmt.Sprintf("cohort-%d", i), Percentage: int32((i + 1) * 5)}
	}
	v, err := e.api.CreateRollout(ctx, RolloutCreateRequest{Token: "staged", ArtifactToken: "fw-2",
		GroupToken: "fleet", MaxInFlight: 1, Cohorts: plan})
	require.NoError(t, err)
	for i := range plan {
		status := TargetPending
		if i == 2 {
			status = TargetSkipped
		}
		require.NoError(t, e.api.RDB.DB(ctx).Create(&UpdateRolloutTarget{RolloutId: v.Rollout.ID,
			DeviceToken: fmt.Sprintf("device-%d", i), Cohort: i, Status: status}).Error)
	}
	require.NoError(t, e.api.RDB.DB(tenantCtx("globex")).Create(&UpdateRolloutTarget{RolloutId: v.Rollout.ID,
		DeviceToken: "foreign-target", Cohort: 2, Status: TargetSkipped}).Error)

	var grouped atomic.Int32
	db := e.api.RDB.DB(ctx)
	require.NoError(t, db.Callback().Row().After("gorm:row").Register("test:cohort-progress", func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if strings.Contains(query, "COUNT(*) AS n") && strings.Contains(query, "update_rollout_targets") {
			grouped.Add(1)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Row().Remove("test:cohort-progress")) })
	v, err = e.api.RolloutByToken(ctx, "staged")
	require.NoError(t, err)
	require.NotNil(t, v)
	require.EqualValues(t, 1, grouped.Load(), "20 cohorts and their total use one grouped query")
	require.Len(t, v.CohortProgress, 20)
	require.Equal(t, RolloutProgress{Total: 1, Pending: 1}, v.CohortProgress[1])
	require.Equal(t, RolloutProgress{Total: 1, Skipped: 1}, v.CohortProgress[2])
	var sum RolloutProgress
	for _, p := range v.CohortProgress {
		sum.Total += p.Total
		sum.Pending += p.Pending
		sum.InFlight += p.InFlight
		sum.Succeeded += p.Succeeded
		sum.Failed += p.Failed
		sum.Unknown += p.Unknown
		sum.Cancelled += p.Cancelled
		sum.Skipped += p.Skipped
	}
	require.Equal(t, v.Progress, sum)
	require.Equal(t, RolloutProgress{Total: 20, Pending: 19, Skipped: 1}, sum)

	for _, tc := range []struct {
		cohort *int32
		want   int
	}{{nil, 20}, {cohortIndex(1), 1}, {cohortIndex(2), 1}, {cohortIndex(20), 0}, {cohortIndex(2147483647), 0}} {
		found, err := e.api.RolloutTargets(ctx, "staged", RolloutTargetCriteria{Pagination: paginationAll(), Cohort: tc.cohort})
		require.NoError(t, err)
		require.NotNil(t, found)
		require.Len(t, found.Results, tc.want)
		require.EqualValues(t, tc.want, found.Pagination.TotalRecords)
		for _, target := range found.Results {
			require.NotEqual(t, "foreign-target", target.Target.DeviceToken)
			if tc.cohort != nil {
				require.Equal(t, int(*tc.cohort), target.Target.Cohort)
			}
		}
	}
	_, err = e.api.RolloutTargets(ctx, "staged", RolloutTargetCriteria{Pagination: paginationAll(), Cohort: cohortIndex(-1)})
	require.Error(t, err)
	var refusal *RefusalError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, CodeInvalidRollout, refusal.Code)
	other := core.WithTenant(ctx, "globex")
	hidden, err := e.api.RolloutByToken(other, "staged")
	require.NoError(t, err)
	require.Nil(t, hidden)
	found, err := e.api.RolloutTargets(other, "staged", RolloutTargetCriteria{Pagination: paginationAll(), Cohort: cohortIndex(2)})
	require.NoError(t, err)
	require.Nil(t, found)
}

func cohortIndex(n int32) *int32 { return &n }
