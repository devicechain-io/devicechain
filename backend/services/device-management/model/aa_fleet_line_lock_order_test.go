// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestFleetSettlementOrdersJoinedResults(t *testing.T) {
	api, recorder := fleetRecordedPG(t)
	_, err := fleetSettlementRead(api.RDB.DB(core.WithTenant(context.Background(), "acme")), "acme", 0, 1000, false, false)
	require.NoError(t, err)
	require.Len(t, recorder.calls, 1)
	require.True(t, strings.HasSuffix(strings.TrimSpace(recorder.calls[0].query), "ORDER BY c.id"), "candidate ordering does not survive the outer joins")
}

// Observe the lock request before SQLite drops FOR UPDATE from its SQL. This
// checks the production writer paths; PostgreSQL execution is tested separately.
func TestFleetBulkWritersLockBeforeMutation(t *testing.T) {
	for _, path := range []string{"erase", "abort", "purge", "resolve"} {
		t.Run(path, func(t *testing.T) {
			e := postFixture(t, "v1", false)
			db := e.api.RDB.DB(e.ctx)
			if path == "resolve" {
				require.NoError(t, db.Model(&e.line).Updates(map[string]any{"posting": "POSTED", "settlement": "AMBIGUOUS"}).Error)
			}
			locked := false
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("test:line-lock", func(tx *gorm.DB) {
				if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "FleetOperationLine" {
					return
				}
				lock, ok := tx.Statement.Clauses["FOR"].Expression.(clause.Locking)
				if !ok || lock.Strength != "UPDATE" {
					return
				}
				order, ok := tx.Statement.Clauses["ORDER BY"].Expression.(clause.OrderBy)
				require.True(t, ok)
				require.Equal(t, "id", order.Columns[0].Column.Name)
				require.False(t, order.Columns[0].Desc)
				locked = true
			}))
			check := func(tx *gorm.DB) {
				if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "FleetOperationLine" {
					require.True(t, locked, "bulk line mutation must first acquire ordered row locks")
				}
			}
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:locked-update", check))
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("test:locked-delete", check))
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("test:locked-insert", func(tx *gorm.DB) {
				if event, ok := tx.Statement.Dest.(*FleetOperationEvent); ok && event.Kind == "RESOLUTION" {
					require.True(t, locked, "resolution must lock lines before inserting its journal")
				}
			}))
			if path == "resolve" {
				_, err := e.api.ResolveAmbiguousFleetLines(resolveContext(e.ctx, fleetClock()), resolveRequest(e.op.Token))
				require.NoError(t, err)
				require.True(t, locked)
				return
			}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				switch path {
				case "erase":
					return redactFleetDeviceLinesOnTx(tx, e.dev.ID, false)
				case "abort":
					_, _, err := fleetAbortChanges(tx, e.op, "test", "", fleetEventAbort)
					return err
				default:
					return fleetPurgeEvidenceChunk(tx, e.op.ID)
				}
			}))
			require.True(t, locked)
		})
	}
}
