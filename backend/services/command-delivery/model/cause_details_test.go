// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectionCauseDetailsPreservesLegacyProjection(t *testing.T) {
	for _, kind := range []string{CauseKindAlarm, CauseKindRule} {
		c := Command{CauseKind: sql.NullString{Valid: true, String: kind}, CauseRuleId: sql.NullString{Valid: true, String: "A/p@1/r"}, CauseDetectionKind: sql.NullString{Valid: true, String: "threshold"}, CauseDetectedAt: sql.NullString{Valid: true, String: "2026-11-20T10:00:00.123456789Z"}}
		details := c.CauseDetails()
		require.NotNil(t, details)
		require.Equal(t, kind, details.Kind)
		require.Nil(t, details.ProposalId)
		require.Equal(t, c.Cause(), details.Detection)
	}
	c := Command{CauseKind: sql.NullString{Valid: true, String: CauseKindProposal}, ProposalToken: sql.NullString{Valid: true, String: "p"}}
	require.Nil(t, c.Cause())
}
