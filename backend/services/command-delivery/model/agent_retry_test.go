// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Both callers can miss the optimistic read. The locked re-probe must return
// the winner even when its insert consumed the very last open slot.
func TestCommandLockedRetryAtCounterCap(t *testing.T) {
	r := newCommandRig(t)
	template := r.pending(t)
	for i := 1; i < actiongate.DefaultOpenPerClient-1; i++ {
		row := *template
		row.ID = 0
		row.Token = fmt.Sprintf("open-%d", i)
		row.RequestId = row.Token
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(&row).Error)
	}
	winner := *template
	winner.ID = 0
	winner.Token = "concurrent-winner"
	winner.RequestId = "racing"
	fired := false
	name := "insert-between-request-probes"
	require.NoError(t, r.api.RDB.Database.Callback().Query().After("gorm:query").Register(name, func(db *gorm.DB) {
		if !fired && db.Statement.Table == "agent_proposals" && strings.Contains(db.Statement.SQL.String(), "request_id") {
			fired = true
			require.NoError(t, r.api.RDB.DB(r.ctx).Create(&winner).Error)
		}
	}))
	t.Cleanup(func() { r.api.RDB.Database.Callback().Query().Remove(name) })
	out := r.propose(t, commandReq("racing"))
	require.True(t, fired)
	require.Equal(t, ProposalStatusExisting, out.Status)
	require.NotNil(t, out.Proposal)
	require.Equal(t, winner.Token, out.Proposal.Token)
}

func TestCommandRetryIgnoresChangedDefinition(t *testing.T) {
	r := newCommandRig(t)
	req := commandReq("retry")
	first := r.propose(t, req)
	r.targets.target.DefinitionFingerprint = "new-published-definition"
	again := r.propose(t, req)
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, first.Digest, again.Digest)
	require.Equal(t, 1, r.targets.calls)
}

func TestCommandLedgerReadIsolationAndFilters(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	_, err := r.api.AgentProposalByToken(r.ctx, p.Token, &actiongate.Proposer{Client: "other", Principal: rigAgent.Principal})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = r.api.AgentProposalByToken(core.WithTenant(r.ctx, "other-tenant"), p.Token, &rigAgent)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	bad := "ALARM_ACK"
	_, err = r.api.AgentProposals(r.ctx, AgentProposalSearchCriteria{Kind: &bad}, nil)
	require.Error(t, err)
	bad = "UNKNOWN"
	_, err = r.api.AgentProposals(r.ctx, AgentProposalSearchCriteria{State: &bad}, nil)
	require.Error(t, err)
	kind := "COMMAND_SEND"
	out, err := r.api.AgentProposals(r.ctx, AgentProposalSearchCriteria{Kind: &kind, Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}, &rigAgent)
	require.NoError(t, err)
	require.Len(t, out.Results, 1)
}
