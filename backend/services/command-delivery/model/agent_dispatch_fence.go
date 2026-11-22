// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// agentDispatchWhere is the claim predicate that keeps a KILLED tenant's agent commands from
// being dispatched: defence in depth behind the kill consumer, which cancels them.
//
// 🔴 A KILL CAN BE LOST IN DELIVERY, AND THIS IS WHAT STILL HOLDS THEN. The kill stream caps
// redeliveries, so a kill whose application kept failing is eventually passed over by the
// broker. The consumer latches the process's feed stale when that happens; this predicate is
// what turns the latch (or any stale feed) into "no agent command leaves", and what refuses
// one whose tenant's fence is closed even when the cancellation has not landed yet.
//
//   - feed not fresh (never bound, broker unreachable, or latched): no row with a
//     proposal_token is claimable at all; ordinary commands are untouched;
//   - feed fresh: an agent row is claimable only while its tenant's fence row EXISTS and is
//     enabled. Every agent command was admitted by an execution that seeded that row, so a
//     missing row is an anomaly and is refused, not read as "never killed".
//
// The fence is read by a plain correlated subquery: no FOR SHARE / FOR UPDATE, so a claim
// never holds a fence lock and cannot invert the kill's order (proposals, then the fence,
// then commands). Under READ COMMITTED it sees the last committed fence. A refused claim reads
// as a lost one: nothing is published, the row stays where it is.
func (api *Api) agentDispatchWhere(ctx context.Context) (string, []any) {
	if !api.KillFeed.Fresh(api.KillFeed.Now()) {
		return "proposal_token IS NULL", nil
	}
	db := api.RDB.DB(ctx)
	fences := db.NamingStrategy.TableName("agent_actuation_fences")
	commands := db.NamingStrategy.TableName("commands")
	open := db.Model(&AgentActuationFence{}).Select("1").
		Where(clause.Eq{Column: clause.Column{Table: fences, Name: "tenant_id"}, Value: clause.Column{Table: commands, Name: "tenant_id"}}).
		Where(clause.Eq{Column: clause.Column{Table: fences, Name: "enabled"}, Value: true})
	return "(proposal_token IS NULL OR EXISTS (?))", []any{open}
}

// agentDispatchScope applies agentDispatchWhere as a gorm scope. It is applied to every
// CANDIDATE read as well as every claim: a candidate read that selected refused agent rows
// would spend its ORDER BY id LIMIT on them, every pass, and starve ordinary commands behind
// them (a device pulling one command at a time would never get past a blocked agent row).
func (api *Api) agentDispatchScope(ctx context.Context) func(*gorm.DB) *gorm.DB {
	return func(q *gorm.DB) *gorm.DB { return api.withAgentDispatchFence(ctx, q) }
}

// withAgentDispatchFence applies agentDispatchWhere to a claim statement.
func (api *Api) withAgentDispatchFence(ctx context.Context, q *gorm.DB) *gorm.DB {
	where, args := api.agentDispatchWhere(ctx)
	return q.Where(where, args...)
}
