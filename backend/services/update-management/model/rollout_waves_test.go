// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pin the pre-extraction persisted representation and assignments, including the
// implicit cohort zero when no plan is configured. Do not derive expectations
// using the shared wave implementation.
func TestFrozenCohortCompatibility(t *testing.T) {
	plan := stagedRequest().Cohorts
	stored := `[{"Name":"canary","DeviceTokens":["dev-3"],"Percentage":0,"ManualPromotion":true},{"Name":"half","DeviceTokens":null,"Percentage":50,"ManualPromotion":false},{"Name":"all","DeviceTokens":null,"Percentage":100,"ManualPromotion":false}]`
	require.Equal(t, sql.NullString{String: stored, Valid: true}, safetyJSON(plan))
	r := UpdateRollout{CohortPlan: sql.NullString{String: stored, Valid: true}}
	decoded, err := r.Cohorts()
	require.NoError(t, err)
	require.Equal(t, plan, decoded)
	got, err := freezeCohorts(r, []string{"dev-0", "dev-1", "dev-2", "dev-3"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"dev-3": 0, "dev-0": 1, "dev-1": 2, "dev-2": 2}, got)
	got, err = freezeCohorts(UpdateRollout{}, []string{"dev-1", "dev-0"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{}, got)

	for _, tc := range []struct {
		name    string
		plan    []RolloutCohort
		devices []string
		want    map[string]int
		refusal string
	}{
		{"round up in frozen order", []RolloutCohort{{Name: "first", Percentage: 34}, {Name: "all", Percentage: 100}}, []string{"dev-2", "dev-0", "dev-1"}, map[string]int{"dev-2": 0, "dev-0": 0, "dev-1": 1}, ""},
		{"outside canary", plan, []string{"dev-0", "dev-1"}, nil, `canary "dev-3" is outside the frozen fleet`},
		{"empty percentage", plan, []string{"dev-0", "dev-3"}, nil, `cohort "half" is empty for this fleet`},
		{"all canaries", []RolloutCohort{{Name: "canary", DeviceTokens: []string{"dev-0"}}, {Name: "all", Percentage: 100}}, []string{"dev-0"}, nil, `cohort "all" is empty for this fleet`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := UpdateRollout{CohortPlan: safetyJSON(tc.plan)}
			got, err := freezeCohorts(r, tc.devices)
			if tc.refusal == "" {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			} else {
				requireRefusal(t, err, CodeInvalidRollout)
				require.EqualError(t, err, tc.refusal)
				require.Nil(t, got)
			}
		})
	}
	_, err = freezeCohorts(UpdateRollout{CohortPlan: sql.NullString{String: "{", Valid: true}}, nil)
	require.ErrorContains(t, err, "unreadable cohort plan:")
}

func TestCohortValidationRefusalCompatibility(t *testing.T) {
	for _, tc := range []struct {
		plan []RolloutCohort
		want string
	}{
		{[]RolloutCohort{{Name: "none"}}, "cohorts need unique names and exactly one selector"},
		{[]RolloutCohort{{Name: "canary", DeviceTokens: []string{"a", "a"}}}, "canaries must be unique and precede percentage cohorts"},
		{[]RolloutCohort{{Name: "large", Percentage: 101}}, "percentages must increase through 100"},
		{[]RolloutCohort{{Name: "half", Percentage: 50}}, "the last cohort must cover 100 percent"},
	} {
		err := validateSafety(RolloutCreateRequest{Cohorts: tc.plan})
		requireRefusal(t, err, CodeInvalidRollout)
		require.EqualError(t, err, tc.want)
	}
}
