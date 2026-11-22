// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestProposalIsNeverALegacyDetection(t *testing.T) {
	for _, kind := range []string{"PROPOSAL", "UNKNOWN"} {
		c := Command{CauseKind: sql.NullString{String: kind, Valid: true}}
		require.Nil(t, c.Cause())
	}
}

func TestCommandCauseDetailsKnownVariantsOnly(t *testing.T) {
	for _, tc := range []struct {
		kind, token, want     string
		validKind, validToken bool
	}{
		{"", "", `null`, false, false},
		{"UNKNOWN", "p", `null`, true, true},
		{"PROPOSAL", "", `null`, true, false},
		{"PROPOSAL", "p", `{"Kind":"PROPOSAL","ProposalId":"p","Detection":null}`, true, true},
	} {
		t.Run(tc.kind+"/"+tc.token, func(t *testing.T) {
			c := Command{CauseKind: sql.NullString{String: tc.kind, Valid: tc.validKind}}
			b, err := json.Marshal(map[string]any{"ProposalToken": sql.NullString{String: tc.token, Valid: tc.validToken}, "Metadata": map[string]any{"proposalId": "forged"}})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(b, &c))
			got, err := json.Marshal(c.CauseDetails())
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(got))
		})
	}
}

func TestPublicCauseRefusesProposal(t *testing.T) {
	_, err := validCause(core.WithTenant(context.Background(), "A"), &CommandCauseInput{
		Kind: "PROPOSAL", RuleId: "A/profile@v1/rule", DetectionKind: "threshold", DetectedAt: "2026-11-20T10:00:00Z",
	})
	require.Error(t, err)
	var rejected *EnqueueRejected
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, RejectCauseInvalid, rejected.Code)
}

func TestExistingEnqueuePathsLeaveProposalTokenNull(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "single", DeviceToken: "device", Name: "reboot"})
	require.NoError(t, err)
	api.BatchValidator = &countingValidator{}
	batch, err := api.CreateCommandBatch(ctx, batchRequest("batch", deviceTokens(2)))
	require.NoError(t, err)
	require.Equal(t, 2, batch.Accepted)
	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Command{}).Where("proposal_token IS NULL").Count(&count).Error)
	require.EqualValues(t, 3, count)
}
