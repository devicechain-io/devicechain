// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// AgentProposal is one action an AI agent asked for in this area: the ledger row (agentic
// actions). In this release the actions are alarm acknowledge and clear.
//
// 🔴 IT DECLARES NO SCOPE RULE, ON PURPOSE. A model that does not implement rdb.ScopedModel is
// refused to every scoped caller, so a member limited to part of the tenant (or an agent acting for
// one) can neither read nor write the ledger. Proposals are made, read and approved only under
// unscoped grants in this release; the gate refuses a scoped caller before it reaches the store,
// and this is the storage half of the same rule.
type AgentProposal struct {
	gorm.Model
	rdb.TenantScoped
	actiongate.Record
}

// DefaultOrder pages the ledger newest first, ending on the id so the order is total.
func (AgentProposal) DefaultOrder() string {
	return "agent_proposals.created_at DESC, agent_proposals.id DESC"
}

// AuditLabel names the proposal in the journal by its server-generated token.
func (p AgentProposal) AuditLabel() string { return p.Token }

var _ rdb.AuditLabeler = AgentProposal{}
