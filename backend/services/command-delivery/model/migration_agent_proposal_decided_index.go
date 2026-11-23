// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAgentProposalDecidedIndexSchema indexes the agent-proposal ledger for the retention purge.
//
// The purge runs every minute across every tenant and selects decided proposals older than
// the retention by decided_at. Without an index that is a full scan of the ledger each tick,
// growing with every proposal kept. The index is partial on decided rows (decided_at IS NOT
// NULL), which is exactly what the purge reads; PROPOSED rows have no decided_at and are never
// purged. It is keyed (decided_at, id) so the batch's ORDER BY id LIMIT reads in order.
//
// It is plain DDL with IF NOT EXISTS, so it is individually re-runnable and needs no snapshot
// struct: it names columns the ledger migration already created.
func NewAgentProposalDecidedIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261122120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE INDEX IF NOT EXISTS idx_agent_proposals_decided_at ` +
				`ON "command-delivery".agent_proposals (decided_at, id) ` +
				`WHERE decided_at IS NOT NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "command-delivery".idx_agent_proposals_decided_at;`).Error
		},
	}
}
