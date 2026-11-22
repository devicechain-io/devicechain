// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errStaleKill rolls back a kill found stale under the fence lock; it is not an error.
var errStaleKill = errors.New("agent kill event is stale")

// AgentKillCounts reports what one kill event did. Stale is true when the event was
// older than the fence already recorded (see killEventStale) and so changed nothing.
type AgentKillCounts struct {
	ProposalsKilled, CommandsCancelled, AlreadySent int64
	Stale                                           bool
}

// killEventStale reports whether ev is older than what the tenant's fence already records,
// so applying it again would be a REPLAY rather than a kill. The consumer's first start
// replays the stream's retained kills, and redelivery replays one: a replayed kill must
// not end the proposals and cancel the agent commands made since the tenant was re-enabled.
//
//   - ev older than the newest kill applied: that newer kill already stopped everything.
//   - ev equal to it while the fence is ENABLED again: it was applied and later lifted by a
//     newer actuation generation (reenableFence).
//
// An equal generation while the fence is still closed is NOT stale: re-applying it is the
// idempotent redelivery of the kill still in force.
//
// It is judged on the fence row LOCKED FOR UPDATE, read AFTER the kill has locked the
// proposals (the order execution uses: proposals, then the fence). An unlocked early read
// could judge "apply", then let an approval re-enable the fence and admit a command, and
// then cancel that command and close the re-enabled fence. Locked, a re-enable either
// committed before this read (and the event is judged stale) or waits for this kill.
func killEventStale(tx *gorm.DB, ev actiongate.KillEvent) (bool, error) {
	var rows []AgentActuationFence
	q := tx
	if tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 || rows[0].Epoch == 0 {
		return false, nil
	}
	f := rows[0]
	return ev.ActuationGeneration < f.KilledAtGeneration || (ev.ActuationGeneration == f.KilledAtGeneration && f.Enabled), nil
}

// ApplyKillEvent is durable stop-only housekeeping; the consumer retries on ANY
// error. SENT commands cannot be recalled. Ordinary commands are never touched.
func (api *Api) ApplyKillEvent(ctx context.Context, ev actiongate.KillEvent) error {
	counts, err := api.ApplyKillEventWithCounts(ctx, ev)
	if err == nil {
		log.Info().Str("tenant", ev.Tenant).Bool("stale", counts.Stale).Int64("proposalsKilled", counts.ProposalsKilled).Int64("commandsCancelled", counts.CommandsCancelled).Int64("alreadySent", counts.AlreadySent).Msg("Applied agent command kill")
	}
	return err
}
func (api *Api) ApplyKillEventWithCounts(ctx context.Context, ev actiongate.KillEvent) (AgentKillCounts, error) {
	var counts AgentKillCounts
	if err := ev.Validate(); err != nil {
		return counts, err
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant != ev.Tenant {
		return counts, fmt.Errorf("kill event tenant does not match context")
	}
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock proposals before the fence, as execution does. A kill that waits
		// for an executing proposal then sees its committed command below.
		// Updating the fence first would invert that order and deadlock.
		now := time.Now().UTC()
		res := tx.Model(&AgentProposal{}).Where("state = ?", string(actiongate.StateProposed)).Updates(map[string]any{"state": string(actiongate.StateKilled), "decided_at": now, "decided_by": "system:kill"})
		if res.Error != nil {
			return res.Error
		}
		counts.ProposalsKilled = res.RowsAffected
		if api.afterKillProposalsForTest != nil {
			api.afterKillProposalsForTest()
		}
		stale, err := killEventStale(tx, ev)
		if err != nil {
			return err
		}
		if stale {
			// A replay: undo the proposal update above by rolling the whole kill back.
			return errStaleKill
		}
		if err := applyKillEventDB(tx, tenant, ev); err != nil {
			return err
		}
		res = tx.Model(&Command{}).Where("proposal_token IS NOT NULL").Where("status IN ?", cancellableStatusStrings()).Updates(terminalUpdate(CommandCancelled, OutcomeNotDelivered, nil))
		if res.Error != nil {
			return res.Error
		}
		counts.CommandsCancelled = res.RowsAffected
		return tx.Model(&Command{}).Where("proposal_token IS NOT NULL AND status = ?", CommandSent.String()).Count(&counts.AlreadySent).Error
	})
	if errors.Is(err, errStaleKill) {
		return AgentKillCounts{Stale: true}, nil
	}
	if err != nil {
		return AgentKillCounts{}, err
	}
	return counts, nil
}
