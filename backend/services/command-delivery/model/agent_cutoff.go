// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"gorm.io/datatypes"
)

// A command cutoff also bounds approval expiry, so a passed cutoff may already
// be overdue. Transition deliberately excludes overdue decisions; this stop-only
// CAS can record the command-specific reason, just as ExpireDue stops overdue rows.
// It cannot revive a terminal row or create an effect.
func (api *Api) refuseCommandCutoff(ctx context.Context, p *AgentProposal, by string) (*AgentProposal, error) {
	res := api.RDB.DB(ctx).Model(&AgentProposal{}).Where("token = ? AND state = ? AND revision = ?", p.Token, string(actiongate.StateProposed), p.Revision).Updates(map[string]any{"state": string(actiongate.StateRefused), "decided_at": time.Now().UTC(), "decided_by": by, "reasons": datatypes.JSON(`["COMMAND_CUTOFF_PASSED"]`)})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, actiongate.ErrNotPending
	}
	return api.AgentProposalByToken(ctx, p.Token, nil)
}
