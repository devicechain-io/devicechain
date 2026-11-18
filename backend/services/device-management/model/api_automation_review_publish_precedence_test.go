// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtectedPublishExpiredApprovedReview(t *testing.T) {
	r := newAutomationReviewRig(t, true)
	protectReviewRig(t, r, DefaultReviewPolicySettings())
	r.changeRule(t, "author")
	row := approveRigReview(t, r, r.run(t), "reviewer")
	require.Equal(t, ReviewStateApproved, row.State)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Update("expires_at", reviewNow()).Error)

	var before, after int64
	db := r.api.RDB.DB(r.ctx)
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Count(&before).Error)
	_, err := publishRigReview(r, row)
	reviewErrorCode(t, err, "REVIEW_EXPIRED")
	require.NoError(t, db.Model(&DeviceProfileVersion{}).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestProtectedPublishExpiryPreservesEarlierRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		updates    map[string]any
	}{
		{"consumed", "REVIEW_ALREADY_USED", map[string]any{"state": ReviewStateConsumed}},
		{"pending", "REVIEW_NOT_APPROVED", map[string]any{"state": ReviewStateRan}},
		{"rejected", "REVIEW_NOT_APPROVED", map[string]any{"state": ReviewStateRejected}},
		{"stored expired", "REVIEW_NOT_APPROVED", map[string]any{"state": ReviewStateExpired}},
		{"failed", "REVIEW_NOT_APPROVED", map[string]any{"outcome": ReviewOutcomeFailed}},
		{"inconclusive", "REVIEW_NOT_APPROVED", map[string]any{"outcome": ReviewOutcomeInconclusive}},
		{"unsupported kind", "REVIEW_NOT_APPROVED", map[string]any{"kind": ReviewKindUnreviewable}},
		{"digest", "DIGEST_MISMATCH", map[string]any{"review_digest": "corrupt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAutomationReviewRig(t, true)
			protectReviewRig(t, r, DefaultReviewPolicySettings())
			r.changeRule(t, "author")
			row := approveRigReview(t, r, r.run(t), "reviewer")
			tc.updates["expires_at"] = reviewNow()
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(row).Updates(tc.updates).Error)
			_, err := publishRigReview(r, row)
			reviewErrorCode(t, err, tc.code)
		})
	}
}
