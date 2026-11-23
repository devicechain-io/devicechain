// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/devicechain-io/dc-update-management/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const cohortProgressFields = `total pending inFlight succeeded failed unknown cancelled skipped`

// cohortProgressPlane seeds all outcome categories, including a missing attempt and an
// assignment owned by another tenant. Twenty cohorts exercise the batching boundary.
func cohortProgressPlane(t *testing.T) *plane {
	t.Helper()
	p, _ := rolloutPlane(t)
	p.api.RolloutSafety = true
	ctx := p.ctx("acme", auth.ArtifactWrite)
	plan := make([]model.RolloutCohort, 20)
	for i := range plan {
		plan[i] = model.RolloutCohort{Name: fmt.Sprintf("cohort-%d", i), Percentage: int32((i + 1) * 5)}
	}
	v, err := p.api.CreateRollout(ctx, model.RolloutCreateRequest{Token: "staged", ArtifactToken: "fw-1",
		GroupToken: "fleet", MaxInFlight: 1, Cohorts: plan})
	require.NoError(t, err)
	addTarget := func(cohort int, status string, assignment uint) {
		t.Helper()
		target := model.UpdateRolloutTarget{RolloutId: v.Rollout.ID, Cohort: cohort, Status: status,
			DeviceToken: fmt.Sprintf("target-%d-%s-%d", cohort, strings.ToLower(status), assignment)}
		if assignment != 0 {
			target.AssignmentId = sql.NullInt64{Int64: int64(assignment), Valid: true}
		}
		require.NoError(t, p.api.RDB.DB(ctx).Create(&target).Error)
	}
	for i := range plan {
		if i != 1 && i != 2 {
			addTarget(i, model.TargetSkipped, 0)
		}
	}
	addTarget(1, model.TargetPending, 0)
	addTarget(1, model.TargetCancelled, 0)
	for i, state := range []ota.State{ota.StateUpdated, ota.StateFailed, ota.StateTimedOut, ota.StateUnknown, ota.StateCancelled, ota.StateQueued} {
		assignment := model.UpdateAssignment{Token: fmt.Sprintf("assignment-%d", i), DeviceToken: fmt.Sprintf("device-%d", i),
			ArtifactId: v.Artifact.ID, Component: "firmware"}
		require.NoError(t, p.api.RDB.DB(ctx).Create(&assignment).Error)
		attempt := model.UpdateAttempt{Token: fmt.Sprintf("attempt-%d", i), AssignmentId: assignment.ID,
			DeviceToken: assignment.DeviceToken, Component: "firmware", State: string(state)}
		require.NoError(t, p.api.RDB.DB(ctx).Create(&attempt).Error)
		addTarget(2, model.TargetDispatched, assignment.ID)
	}
	addTarget(2, model.TargetDispatched, 0)
	other := p.ctx("globex", auth.ArtifactWrite)
	foreignAssignment := model.UpdateAssignment{Token: "foreign", DeviceToken: "foreign-device", Component: "firmware", ArtifactId: v.Artifact.ID}
	require.NoError(t, p.api.RDB.DB(other).Create(&foreignAssignment).Error)
	require.NoError(t, p.api.RDB.DB(other).Create(&model.UpdateAttempt{Token: "foreign-attempt", AssignmentId: foreignAssignment.ID,
		DeviceToken: "foreign-device", Component: "firmware", State: string(ota.StateUpdated)}).Error)
	addTarget(2, model.TargetDispatched, foreignAssignment.ID)
	// A foreign target sharing the rollout id must affect neither progress nor search.
	require.NoError(t, p.api.RDB.DB(other).Create(&model.UpdateRolloutTarget{RolloutId: v.Rollout.ID,
		DeviceToken: "foreign-target", Cohort: 2, Status: model.TargetPending}).Error)
	return p
}

func TestCohortProgressPartitionsRolloutInOneGroupedRead(t *testing.T) {
	p := cohortProgressPlane(t)
	var grouped atomic.Int32
	db := p.api.RDB.DB(p.ctx("acme", auth.ArtifactRead))
	require.NoError(t, db.Callback().Row().After("gorm:row").Register("test:cohort-progress", func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if strings.Contains(query, "COUNT(*) AS n") && strings.Contains(query, "update_rollout_targets") {
			grouped.Add(1)
		}
	}))
	t.Cleanup(func() { require.NoError(t, db.Callback().Row().Remove("test:cohort-progress")) })
	resp := p.exec(p.ctx("acme", auth.ArtifactRead), `{ updateRollout(token: "staged") {
	  progress { `+cohortProgressFields+` } cohorts { name progress { `+cohortProgressFields+` } }
	} }`, nil)
	require.Empty(t, resp.Errors)
	var got struct {
		UpdateRollout struct {
			Progress map[string]int
			Cohorts  []struct {
				Name     string
				Progress map[string]int
			}
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &got))
	require.Len(t, got.UpdateRollout.Cohorts, 20)
	sum := map[string]int{}
	for i, c := range got.UpdateRollout.Cohorts {
		expected := map[string]int{"total": 1, "pending": 0, "inFlight": 0, "succeeded": 0, "failed": 0, "unknown": 0, "cancelled": 0, "skipped": 1}
		if i == 1 {
			expected["total"], expected["pending"], expected["cancelled"], expected["skipped"] = 2, 1, 1, 0
		}
		if i == 2 {
			expected["total"], expected["inFlight"], expected["succeeded"], expected["failed"] = 8, 3, 1, 2
			expected["unknown"], expected["cancelled"], expected["skipped"] = 1, 1, 0
		}
		require.Equal(t, expected, c.Progress, "cohort %d must count only its own targets", i)
		for field, n := range c.Progress {
			sum[field] += n
		}
	}
	require.Equal(t, got.UpdateRollout.Progress, sum)
	require.EqualValues(t, 1, grouped.Load(), "20 cohorts and the rollout total share one aggregate query")
	resp = p.exec(p.ctx("globex", auth.ArtifactRead), `{ updateRollout(token: "staged") { cohorts { progress { total } } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateRollout":null}`, string(resp.Data))
}

func TestCohortTargetFilterOnTheWire(t *testing.T) {
	p := cohortProgressPlane(t)
	query := `query($criteria: UpdateRolloutTargetSearchCriteria!) {
	  updateRolloutTargets(token: "staged", criteria: $criteria) {
	    results { deviceToken cohort outcome } pagination { totalRecords }
	  }
	}`
	for _, tc := range []struct {
		name    string
		cohort  any
		outcome string
		want    int
	}{
		{"null", nil, "", 28}, {"first", 1, "", 2}, {"second", 2, "", 8},
		{"combined", 2, "FAILED", 2}, {"combined-empty", 1, "SUCCEEDED", 0},
		{"outside-plan", 20, "", 0}, {"large", 2147483647, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			criteria := map[string]any{"pageNumber": 1, "pageSize": 100, "cohort": tc.cohort}
			if tc.outcome != "" {
				criteria["outcome"] = tc.outcome
			}
			resp := p.exec(p.ctx("acme", auth.ArtifactRead), query, map[string]any{"criteria": criteria})
			require.Empty(t, resp.Errors)
			var got struct {
				UpdateRolloutTargets struct {
					Results []struct {
						DeviceToken, Outcome string
						Cohort               int
					}
					Pagination struct{ TotalRecords int }
				}
			}
			require.NoError(t, json.Unmarshal(resp.Data, &got))
			require.Len(t, got.UpdateRolloutTargets.Results, tc.want)
			require.Equal(t, tc.want, got.UpdateRolloutTargets.Pagination.TotalRecords)
			for _, target := range got.UpdateRolloutTargets.Results {
				require.NotEqual(t, "foreign-target", target.DeviceToken)
				if tc.cohort != nil {
					require.Equal(t, tc.cohort, target.Cohort)
				}
				if tc.outcome != "" {
					require.Equal(t, tc.outcome, target.Outcome)
				}
			}
		})
	}
	resp := p.exec(p.ctx("acme", auth.ArtifactRead), query, map[string]any{"criteria": map[string]any{
		"pageNumber": 1, "pageSize": 100, "cohort": -1}})
	require.Equal(t, model.CodeInvalidRollout, code(resp))
	resp = p.exec(p.ctx("globex", auth.ArtifactRead), query, map[string]any{"criteria": map[string]any{
		"pageNumber": 1, "pageSize": 100, "cohort": 2}})
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"updateRolloutTargets":null}`, string(resp.Data))
}

func TestDraftCohortsHaveZeroProgress(t *testing.T) {
	p, _ := rolloutPlane(t)
	p.api.RolloutSafety = true
	resp := p.exec(p.ctx("acme", auth.ArtifactWrite), `mutation { createUpdateRollout(request: {
	  token: "draft", artifactToken: "fw-1", groupToken: "fleet", maxInFlight: 1,
	  cohorts: [{name: "all", percentage: 100}]
	}) { cohorts { progress { `+cohortProgressFields+` } } } }`, nil)
	require.Empty(t, resp.Errors)
	require.JSONEq(t, `{"createUpdateRollout":{"cohorts":[{"progress":{"total":0,"pending":0,"inFlight":0,"succeeded":0,"failed":0,"unknown":0,"cancelled":0,"skipped":0}}]}}`, string(resp.Data))
}
