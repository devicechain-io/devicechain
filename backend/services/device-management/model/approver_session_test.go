// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func twoSessionApprovalRig(t *testing.T) (*proposalRig, *AgentProposal, actiongate.Approver, actiongate.Approver) {
	t.Helper()
	r := newProposalRig(t)
	r.decider.act.RequiredApprovals = 2
	r.decider.exec = execApprove(2, "a", "b")
	r.decider.liveSids = map[string]bool{"a1": true, "a2": true, "b1": true}
	a, b := approver("a", time.Now()), approver("b", time.Now())
	a.Sid, b.Sid = "a1", "b1"
	p := r.propose(t, rigAgent, AlarmProposalRequest{Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "session-test"}).Proposal
	return r, p, a, b
}

func TestApprovalRecordsTheApproverSession(t *testing.T) {
	r, p, a, _ := twoSessionApprovalRig(t)
	got, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), a, p.Token, p.Digest)
	require.NoError(t, err)
	approvals, err := actiongate.ParseApprovals(got.Approvals)
	require.NoError(t, err)
	require.Equal(t, a.Sid, approvals[0].Sid)
	require.Contains(t, string(got.Approvals), `"sid":"a1"`)
}

func lapsedApprovalRig(t *testing.T) (*proposalRig, *AgentProposal, actiongate.Approver) {
	t.Helper()
	r, p, a, b := twoSessionApprovalRig(t)
	ctx := tenantCtx(rigTenant)
	_, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	delete(r.decider.liveSids, "a1")
	_, err = r.api.ApproveAgentProposal(ctx, b, p.Token, p.Digest)
	require.NoError(t, err)
	requireUnexecuted(t, r, p, ctx)
	return r, p, a
}

func TestApproverSignOutLapsesTheirApprovalBeforeExecution(t *testing.T) {
	lapsedApprovalRig(t)
}

func TestReapprovalFromNewSessionReplacesTheLapsedEntry(t *testing.T) {
	r, p, a := lapsedApprovalRig(t)
	a.Sid = "a2"
	got, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), a, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	approvals, err := actiongate.ParseApprovals(got.Approvals)
	require.NoError(t, err)
	require.Len(t, approvals, 2)
	require.Equal(t, []actiongate.ApproverSession{{Principal: "b", Sid: "b1"}, {Principal: "a", Sid: "a2"}}, actiongate.ApproverSessions(approvals))
}

func TestFastPathRequiresTheSameSession(t *testing.T) {
	r, p, a, _ := twoSessionApprovalRig(t)
	ctx := tenantCtx(rigTenant)
	first, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	unchanged, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, first.Revision, unchanged.Revision, "same a1 sign-in answers unchanged")
	a.Sid = "a2"
	next, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, first.Revision+1, next.Revision)
	approvals, err := actiongate.ParseApprovals(next.Approvals)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	require.Equal(t, "a2", approvals[0].Sid)
}

func TestApprovalWithoutSidIsNotCountedOnceEnforced(t *testing.T) {
	r, p, a, b := twoSessionApprovalRig(t)
	ctx := tenantCtx(rigTenant)
	// A real legacy row: its approval predates the JSON session binding.
	legacy, err := json.Marshal([]actiongate.Approval{{Sub: a.Principal, Amr: a.Amr, MfaTime: a.MfaTime, At: time.Now()}})
	require.NoError(t, err)
	require.NoError(t, r.db.WithContext(ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("approvals", string(legacy)).Error)
	_, err = r.api.ApproveAgentProposal(ctx, b, p.Token, p.Digest)
	require.NoError(t, err)
	requireUnexecuted(t, r, p, ctx)
	got, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
}

func TestCommitPointNamesEveryApproverSession(t *testing.T) {
	r, p, a, b := twoSessionApprovalRig(t)
	ctx := tenantCtx(rigTenant)
	_, err := r.api.ApproveAgentProposal(ctx, a, p.Token, p.Digest)
	require.NoError(t, err)
	r.decider.execReqs = nil
	got, err := r.api.ApproveAgentProposal(ctx, b, p.Token, p.Digest)
	require.NoError(t, err)
	require.Len(t, r.decider.execReqs, 2)
	approvals, err := actiongate.ParseApprovals(got.Approvals)
	require.NoError(t, err)
	require.Equal(t, actiongate.ApproverSessions(approvals), r.decider.execReqs[1].ApproverSessions)
}

func TestConnectionLostClassifier(t *testing.T) {
	for _, err := range []error{driver.ErrBadConn, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.EPIPE, pgconn.ErrConnClosed} {
		require.True(t, connectionLost(err))
		require.True(t, connectionLost(fmt.Errorf("wrapped: %w", err)))
	}
	require.False(t, connectionLost(nil))
	require.False(t, connectionLost(errors.New("conn closed")), "message text is not evidence")
	require.False(t, connectionLost(&pgconn.PgError{Code: "23505"}))
}

func TestConnectionLostAfterCommitPointIsPolicyUnavailable(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.api.afterDecisionForTest = func(*gorm.DB) error { return pgconn.ErrConnClosed }
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	requireUnexecuted(t, r, p, ctx)
}

func TestConnectionLostBeforeCommitPointIsNotRemapped(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	r.api.beforeFenceForTest = func(*gorm.DB) error { return pgconn.ErrConnClosed }
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, pgconn.ErrConnClosed)
	require.NotErrorIs(t, err, actiongate.ErrPolicyUnavailable)
	requireUnexecuted(t, r, p, ctx)
}
