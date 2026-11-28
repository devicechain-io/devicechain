// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exercise both production callers of fleetEvidenceCompleteSQL on an isolated
// instance. Each operation has 10k lines; a separate operation's open backlog
// ensures that a tenant-only index cannot make the per-operation probes cheap.
func TestFleetEvidenceCloseUsesPartialIndexOnPostgres(t *testing.T) {
	api := NewApi(driverPGManager(t))
	f := newCostFixture(t, api)
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".fleet_operations
 (created_at, updated_at, tenant_id, token, kind, name, dispatch, evidence, request_digest, driver_protocol,
 intent, target_kind, target_count, max_posts_per_second, eff_posts_per_second, settle_deadline_s, initiator)
 SELECT now(), now(), ?, token, 'config.set', 'close cost', 'DISPATCHED', 'OPEN', 'x', 1,
 '{"key":"k"}', 'DEVICE_LIST', 10000, 50, 25, 3600, 't'
 FROM unnest(ARRAY['complete','erased','ambiguous-tail','unsettled-erased-tail','backlog']) token`, f.active).Error)
	// Redacted lines are outside the evidence sweep, but the close predicate
	// deliberately checks them. In particular, NULL settlement still blocks
	// closure even with TARGET_ERASED, while AMBIGUOUS/TARGET_ERASED does not.
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".fleet_operation_lines
 (tenant_id, operation_id, ordinal, target_id, posting, settlement, code, redacted_at)
 SELECT o.tenant_id, o.id, g, ? + o.id * 10000 + g, 'POSTED',
 CASE WHEN o.token='unsettled-erased-tail' AND g=10000 THEN NULL
      WHEN o.token='complete' THEN 'CONFIRMED' ELSE 'AMBIGUOUS' END,
 CASE WHEN o.token='backlog' OR (o.token='ambiguous-tail' AND g=10000) THEN 'NO_EVIDENCE'
      ELSE 'TARGET_ERASED' END, now()
 FROM "device-management".fleet_operations o CROSS JOIN generate_series(1,10000) g
 WHERE o.tenant_id=?`, f.devices, f.active).Error)
	for range 2 {
		require.NoError(t, schema.NewFleetLineEvidenceCloseIndexSchema().Migrate(f.db), "replay over a populated journal")
	}
	vacuumLedger(t, f)

	session, capture := capturing(api.RDB.Database)
	ctx := core.WithTenant(context.Background(), f.active)
	n, err := NewApi(&rdb.RdbManager{Database: session}).ReconcileFleetLines(ctx, true)
	require.NoError(t, err)
	require.Zero(t, n, "redacted lines do not enter the evidence sweep")
	var closed []string
	require.NoError(t, api.RDB.DB(ctx).Model(&FleetOperation{}).Where("evidence='CLOSED'").Order("token").Pluck("token", &closed).Error)
	require.Equal(t, []string{"complete", "erased"}, closed)

	// Reopen the fixture so EXPLAIN ANALYZE executes the subquery of each
	// captured UPDATE. Roll it back after measuring so every plan sees OPEN.
	require.NoError(t, f.db.Exec(`UPDATE "device-management".fleet_operations SET evidence='OPEN', closed_at=NULL WHERE tenant_id=?`, f.active).Error)
	selects, updates := 0, 0
	rollback := errors.New("rollback close measurement")
	for _, statement := range capture.statements {
		if !strings.Contains(statement, "NOT EXISTS (SELECT 1 FROM") || !strings.Contains(statement, "fleet_operation_lines") {
			continue
		}
		if strings.HasPrefix(statement, "SELECT") {
			selects++
		} else if strings.HasPrefix(statement, "UPDATE") {
			updates++
		} else {
			continue
		}
		err := f.db.Transaction(func(tx *gorm.DB) error {
			var plan string
			require.NoError(t, tx.Raw("EXPLAIN (FORMAT JSON) "+statement).Row().Scan(&plan))
			require.Contains(t, plan, `"Index Name": "ix_fleet_operation_lines_evidence_close"`, statement)
			cost := measure(t, tx, statement)
			t.Logf("close check: %d line rows/index entries, %d buffers, %.3f ms: %s", cost.LineRows, cost.Buffers, cost.Ms, statement)
			require.LessOrEqual(t, cost.LineRows, int64(10), "probe blockers per operation, not 10k settled/erased lines or another operation's backlog")
			return rollback
		})
		require.ErrorIs(t, err, rollback)
	}
	require.Equal(t, 1, selects, "the production close-retry SELECT was measured")
	require.Equal(t, 2, updates, "both production conditional close UPDATEs were measured")
}
