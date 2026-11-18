// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func TestEffectiveReviewState(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	policy := DefaultReviewPolicySettings()
	for _, tc := range []struct {
		name, state, want string
		approved, expires *time.Time
	}{
		{"valid", ReviewStateApproved, ReviewStateApproved, &past, &future},
		{"stored boundary", ReviewStateApproved, ReviewStateExpired, &past, &now},
		{"past stored expiry", ReviewStateApproved, ReviewStateExpired, &past, &past},
		{"missing approval", ReviewStateApproved, ReviewStateExpired, nil, &future},
		{"missing expiry", ReviewStateApproved, ReviewStateExpired, &past, nil},
		{"pending", ReviewStateRan, ReviewStateRan, nil, nil},
		{"rejected", ReviewStateRejected, ReviewStateRejected, &past, &past},
		{"consumed", ReviewStateConsumed, ReviewStateConsumed, &past, &past},
		{"expired", ReviewStateExpired, ReviewStateExpired, &past, &future},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := &AutomationReview{State: tc.state, ApprovedAt: tc.approved, ExpiresAt: tc.expires}
			require.Equal(t, tc.want, effectiveReviewState(row, policy, now))
			require.Equal(t, tc.state, row.State, "deriving state must not mutate the ledger")
		})
	}
	at := now.Add(-DefaultReviewApprovalTTL)
	row := &AutomationReview{State: ReviewStateApproved, ApprovedAt: &at, ExpiresAt: &future}
	require.Equal(t, ReviewStateApproved, effectiveReviewState(row, policy, now.Add(-time.Nanosecond)))
	require.Equal(t, ReviewStateExpired, effectiveReviewState(row, policy, now))
}

func TestAutomationReviewExpiryReadsFiltersAndPublish(t *testing.T) {
	for _, scenario := range []string{"stored boundary", "policy boundary", "missing approval", "missing expiry", "stored expired"} {
		t.Run(scenario, func(t *testing.T) {
			rig := newAutomationReviewRig(t, true)
			protectReviewRig(t, rig, DefaultReviewPolicySettings())
			rig.changeRule(t, "author")
			row := approveRigReview(t, rig, rig.run(t), "reviewer")
			now := row.ApprovedAt.Add(time.Second).UTC()
			oldNow := reviewNow
			reviewNow = func() time.Time { return now }
			t.Cleanup(func() { reviewNow = oldNow })
			db := rig.api.RDB.DB(rig.ctx)

			// A newer valid approval must be excluded before paging EXPIRED results.
			live := *row
			live.ID, live.Token, live.RequestId = 0, "acr_live", "live"
			require.NoError(t, db.Create(&live).Error)
			updates := map[string]any{}
			switch scenario {
			case "stored boundary":
				updates["expires_at"] = now
			case "policy boundary":
				updates["approved_at"] = now.Add(-900 * time.Second)
				want := DefaultReviewPolicySettings()
				want.ApprovalTtlSeconds = 900
				protectReviewRig(t, rig, want)
			case "missing approval":
				updates["approved_at"] = nil
			case "missing expiry":
				updates["expires_at"] = nil
			case "stored expired":
				updates["state"] = ReviewStateExpired
			}
			require.NoError(t, db.Model(row).Updates(updates).Error)
			got, err := rig.api.AutomationReviewByToken(rig.ctx, row.Token)
			require.NoError(t, err)
			require.Equal(t, ReviewStateExpired, got.State)
			// Retrying run by request ID returns the same effective state.
			require.Equal(t, ReviewStateExpired, rig.run(t).State)

			for _, state := range []string{ReviewStateExpired, ReviewStateApproved} {
				page, err := rig.api.AutomationReviews(rig.ctx, AutomationReviewSearchCriteria{
					ProfileToken: "profile", State: &state, Pagination: rdb.Pagination{PageNumber: 1, PageSize: 1},
				})
				require.NoError(t, err)
				require.EqualValues(t, 1, page.Pagination.TotalRecords)
				require.Len(t, page.Results, 1)
				require.Equal(t, state, page.Results[0].State)
				wantToken := row.Token
				if state == ReviewStateApproved {
					wantToken = live.Token
				}
				require.Equal(t, wantToken, page.Results[0].Token)
			}
			page, err := rig.api.AutomationReviews(rig.ctx, AutomationReviewSearchCriteria{
				ProfileToken: "profile", Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
			})
			require.NoError(t, err)
			require.Len(t, page.Results, 2)
			states := map[string]string{}
			for _, review := range page.Results {
				states[review.Token] = review.State
			}
			require.Equal(t, ReviewStateExpired, states[row.Token])
			require.Equal(t, ReviewStateApproved, states[live.Token])

			ctx, person := reviewIdentity("reviewer")
			_, err = rig.api.ApproveAutomationReview(ctx, person, row.Token, row.ReviewDigest)
			reviewErrorCode(t, err, "REVIEW_NOT_PENDING")
			_, err = rig.api.RejectAutomationReview(ctx, person, row.Token, nil)
			reviewErrorCode(t, err, "REVIEW_NOT_PENDING")
			var before, after int64
			require.NoError(t, db.Model(&DeviceProfileVersion{}).Count(&before).Error)
			_, err = publishRigReview(rig, row)
			if scenario == "stored expired" {
				// Preserve the existing refusal for a terminal ledger state.
				reviewErrorCode(t, err, "REVIEW_NOT_APPROVED")
			} else {
				reviewErrorCode(t, err, "REVIEW_EXPIRED")
			}
			require.NoError(t, db.Model(&DeviceProfileVersion{}).Count(&after).Error)
			require.Equal(t, before, after)
			// Reads and refused publish do not persist the derived state or a revision.
			var stored AutomationReview
			require.NoError(t, db.Where("token = ?", row.Token).First(&stored).Error)
			want := ReviewStateApproved
			if scenario == "stored expired" {
				want = ReviewStateExpired
			}
			require.Equal(t, want, stored.State)
			require.Equal(t, row.Revision, stored.Revision)
		})
	}
}
