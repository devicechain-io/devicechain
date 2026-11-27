// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errLagIsolationRollback = errors.New("rolled back")

// withOnlyTheseAwaiting runs fn with an Api over one transaction in which the only lines awaiting
// evidence are those of tenants named with prefix, and rolls it back. The reconciler lag is an
// instance-wide reading and the integration database is shared by every test, so a bound asserted
// on it must not depend on what the tests before it left behind.
func withOnlyTheseAwaiting(t *testing.T, api *Api, prefix string, fn func(iso *Api, tx *gorm.DB)) {
	t.Helper()
	err := api.RDB.DB(core.WithSystemContext(context.Background())).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE "device-management".fleet_operation_lines SET evidence_revision = -1
 WHERE tenant_id NOT LIKE ? AND posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL`, prefix+"%").Error; err != nil {
			return err
		}
		fn(NewApi(&rdb.RdbManager{Database: tx}), tx)
		return errLagIsolationRollback
	})
	require.ErrorIs(t, err, errLagIsolationRollback)
}

// fleetReconcilerLagBeforeForTest is the per-tenant measurement this change replaced, kept as the
// oracle the instance-wide one is compared with: the instance's lag is the largest tenant's.
func fleetReconcilerLagBeforeForTest(db *gorm.DB, tenant string) (time.Duration, error) {
	r, l, o := fleetSettlementTable(db, "DeviceConfigurationReport"), fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	var age *float64
	err := db.Raw(`SELECT EXTRACT(EPOCH FROM clock_timestamp() - min(r.created_at)) FROM `+r+` r JOIN `+l+` l ON l.target_id=r.device_id AND l.tenant_id=r.tenant_id AND l.posted_revision<=r.revision
 JOIN `+o+` o ON o.id=l.operation_id AND o.tenant_id=l.tenant_id
 WHERE r.tenant_id=? AND l.posting='POSTED' AND l.evidence_revision IS NULL AND l.redacted_at IS NULL
 AND (`+fleetSettlementProtocol+`)`, tenant, ledger.SupportedDriverProtocol).Scan(&age).Error
	if err != nil || age == nil {
		return 0, err
	}
	return time.Duration(*age * float64(time.Second)), nil
}

// reportScan is what the plan's scans of the report log did, summed over them: how many times
// they ran, the shared buffers they touched, the rows their filters removed, and (the largest
// over them) the rows one run read, returned or filtered, as EXPLAIN averages it per loop.
type reportScan struct {
	loops, buffers, filtered int64
	perLoop                  float64
}

func reportScans(n planNode) reportScan {
	var out reportScan
	if n.Relation == "device_configuration_reports" {
		out = reportScan{loops: int64(n.Loops), buffers: n.Hit + n.Read, filtered: int64(n.Filtered * n.Loops), perLoop: n.Rows + n.Filtered}
	}
	for _, child := range n.Plans {
		c := reportScans(child)
		out.loops, out.buffers, out.filtered = out.loops+c.loops, out.buffers+c.buffers, out.filtered+c.filtered
		out.perLoop = max(out.perLoop, c.perLoop)
	}
	return out
}

// The reconciler lag is one statement per replica every 15 s, so what it reads matters: it must
// read the lines awaiting evidence and, for each, the first report at or after its posted revision,
// and nothing that grows with the report log's retained history, decided or not. Measured with EXPLAIN (ANALYZE,
// BUFFERS) on the statement the measurement issues, at two sizes of retained (decided) history,
// beside an AMBIGUOUS backlog with no reports and one tenant whose reports are waiting, and then for
// lines whose devices hold a long history of undecided reports. Its answer
// is compared with the per-tenant measurement it replaced, taken over every tenant.
func TestReconcilerLagReadsTheAwaitingLinesNotTheReportHistoryOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	f := newCostFixture(t, api)
	t.Cleanup(func() {
		require.NoError(t, f.db.Exec(`DELETE FROM "device-management".device_configuration_reports WHERE tenant_id LIKE ?`, f.prefix+"%").Error)
	})
	// The AMBIGUOUS backlog: posted lines past their deadline, no report for any of them.
	f.addOperations(t, f.active, "history", 6, 1)
	// The waiting tenant: one RUNNING change with posted lines, three of whose devices reported.
	waiting := f.prefix + "waiting"
	inserted := time.Now()
	f.addOperations(t, waiting, "running", 1, 1)
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".device_configuration_reports (created_at, tenant_id, device_id, revision, digest, status, source)
 SELECT now() - (interval '1 second' * (100 + 10 * k)), l.tenant_id, l.target_id, 5, 'x', 'APPLIED', 'REPORT'
 FROM (SELECT tenant_id, target_id, row_number() OVER (ORDER BY id) k FROM "device-management".fleet_operation_lines
 WHERE tenant_id = ? AND posting = 'POSTED') l WHERE l.k <= 3`, waiting).Error)
	// A newer release's change, with a waiting report older than all of them: not this release's to settle.
	newer := f.prefix + "newer"
	f.addOperations(t, newer, "running", 1, 99)
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".device_configuration_reports (created_at, tenant_id, device_id, revision, digest, status, source)
 SELECT now() - interval '1 hour', tenant_id, target_id, 5, 'x', 'APPLIED', 'REPORT' FROM "device-management".fleet_operation_lines
 WHERE tenant_id = ? AND posting = 'POSTED' LIMIT 1`, newer).Error)

	noise := 0
	addHistory := func(tenants, revisions int) {
		t.Helper()
		for i := 0; i < tenants; i++ {
			tenant := fmt.Sprintf("%snoise%03d", f.prefix, noise)
			noise++
			f.addOperations(t, tenant, "noise", 1, 1)
			// Decided history: every line's device reported revisions 1..n, all older than any waiting one.
			require.NoError(t, f.db.Exec(`INSERT INTO "device-management".device_configuration_reports (created_at, tenant_id, device_id, revision, digest, status, source)
 SELECT now() - interval '2 days', l.tenant_id, l.target_id, g, 'x', 'APPLIED', 'REPORT'
 FROM "device-management".fleet_operation_lines l CROSS JOIN generate_series(1, ?) g WHERE l.tenant_id = ?`, revisions, tenant).Error)
		}
		f.analyze(t)
		require.NoError(t, f.db.Exec(`ANALYZE "device-management".device_configuration_reports`).Error)
	}

	type reading struct {
		lines   int64
		reports reportScan
		buffers int64
		ms      float64
		lag     time.Duration
		since   time.Duration // since the waiting reports were written, when the lag was read
	}
	read := func(scope string, inserted time.Time) reading {
		t.Helper()
		var out reading
		withOnlyTheseAwaiting(t, api, scope, func(iso *Api, tx *gorm.DB) {
			session, capture := capturing(tx)
			lagApi := NewApi(&rdb.RdbManager{Database: session})
			var err error
			out.since = time.Since(inserted)
			out.lag, err = lagApi.FleetReconcilerLag(context.Background())
			require.NoError(t, err)
			require.Len(t, capture.statements, 1, "one statement")
			cost := measure(t, tx, capture.statements[0])
			var text string
			require.NoError(t, tx.Raw("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+capture.statements[0]).Row().Scan(&text))
			var plan []struct {
				Plan planNode `json:"Plan"`
			}
			require.NoError(t, json.Unmarshal([]byte(text), &plan))
			out.lines, out.reports, out.buffers, out.ms = cost.LineRows, reportScans(plan[0].Plan), cost.Buffers, cost.Ms

			// The oracle: the per-tenant read it replaced, over every tenant there is.
			var tenants []string
			require.NoError(t, tx.Raw(`SELECT DISTINCT tenant_id FROM "device-management".fleet_operation_lines WHERE tenant_id LIKE ?`, scope+"%").Scan(&tenants).Error)
			var want time.Duration
			for _, tenant := range tenants {
				age, err := fleetReconcilerLagBeforeForTest(tx, tenant)
				require.NoError(t, err)
				want = max(want, age)
			}
			require.InDelta(t, want.Seconds(), out.lag.Seconds(), 1, "the instance-wide lag is the largest tenant's")

			// And the gauge publishes it.
			require.NoError(t, iso.MeasureFleetReconcilerLag(context.Background()))
			require.InDelta(t, out.lag.Seconds(), reconcilerLag(), 1)
		})
		return out
	}

	// History and the waiting tenant's change are drivable; the newer release's lines await too, but
	// its change is filtered out before any report is probed.
	drivable := int64(6*costPostedPerOp + costPostedPerOp/2)
	awaiting := drivable + costPostedPerOp/2
	addHistory(4, 5)
	small := read(f.prefix, inserted)
	addHistory(16, 5)
	large := read(f.prefix, inserted)
	t.Logf("reconciler lag: %d awaiting lines (%d of drivable changes); history 4 -> 20 noise tenants: line rows %d -> %d, "+
		"report probes %d -> %d (%d -> %d buffers), statement buffers %d -> %d, %.1f -> %.1f ms",
		awaiting, drivable, small.lines, large.lines, small.reports.loops, large.reports.loops, small.reports.buffers, large.reports.buffers,
		small.buffers, large.buffers, small.ms, large.ms)

	// The waiting tenant's oldest report was 130 s old when it was written; the newer release's
	// hour-old one is not counted.
	require.InDelta(t, 130+small.since.Seconds(), small.lag.Seconds(), 3)
	require.InDelta(t, 130+large.since.Seconds(), large.lag.Seconds(), 3)
	for _, r := range []reading{small, large} {
		// The lines read are the awaiting lines: the partial index's entries, once each.
		require.Equal(t, awaiting, r.lines, "line rows read are the awaiting lines")
		// One probe of the report log per awaiting line of a drivable change, reading only that
		// device's reports at or after its posted revision: nothing filtered away, a few buffers each.
		require.Equal(t, drivable, r.reports.loops, "one report probe per awaiting drivable line")
		require.Zero(t, r.reports.filtered, "a probe reads no report it then discards")
		require.LessOrEqual(t, r.reports.buffers, 5*drivable, "a probe is an index descent")
	}
	// Five times the decided history, the same probes.
	require.Equal(t, small.reports.loops, large.reports.loops, "report probes must not grow with decided history")

	// UNDECIDED history: a change whose lines stay awaiting while every device keeps reporting newer
	// revisions (settlement failing, say). Each line's range at or after its posted revision is then
	// deepHistory reports long, and grows with every report; the probe must still read about one.
	// Read on its own, so EXPLAIN's per-loop average is this tenant's probes alone.
	const deepHistory = 100
	deep := f.prefix + "deep"
	deepInserted := time.Now()
	f.addOperations(t, deep, "running", 1, 1)
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management".device_configuration_reports (created_at, tenant_id, device_id, revision, digest, status, source)
 SELECT CASE WHEN g = 5 THEN now() - interval '200 seconds' ELSE now() - interval '50 seconds' END, l.tenant_id, l.target_id, g, 'x', 'APPLIED', 'REPORT'
 FROM "device-management".fleet_operation_lines l CROSS JOIN generate_series(5, ?) g WHERE l.tenant_id = ? AND l.posting = 'POSTED'`, 4+deepHistory, deep).Error)
	f.analyze(t)
	require.NoError(t, f.db.Exec(`ANALYZE "device-management".device_configuration_reports`).Error)
	undecided := read(deep, deepInserted)
	deepLines := int64(costPostedPerOp / 2)
	t.Logf("reconciler lag, %d awaiting lines each with %d undecided reports at or after its posted revision: report probes %d, "+
		"%.1f report rows per probe, %d buffers, %.1f ms", deepLines, deepHistory, undecided.reports.loops, undecided.reports.perLoop,
		undecided.reports.buffers, undecided.ms)
	// Each line's decider (revision 5) was 200 s old when written; the newer revisions are younger.
	require.InDelta(t, 200+undecided.since.Seconds(), undecided.lag.Seconds(), 3)
	require.Equal(t, deepLines, undecided.lines)
	require.Equal(t, deepLines, undecided.reports.loops, "one report probe per awaiting line")
	require.LessOrEqual(t, undecided.reports.perLoop, 3.0, "a probe reads its decider, not the device's whole range of undecided reports")
}

// The throughput run's lag bound, on the instance-wide measurement the gauge publishes.
func assertReconcilerLagBound(t *testing.T, api *Api, prefix string) {
	t.Helper()
	withOnlyTheseAwaiting(t, api, prefix, func(iso *Api, _ *gorm.DB) {
		lag, err := iso.FleetReconcilerLag(context.Background())
		require.NoError(t, err)
		require.Less(t, lag, 15*time.Second, "reconciler lag at the end of the 10-tenant run")
		require.NoError(t, iso.MeasureFleetReconcilerLag(context.Background()))
		require.False(t, math.IsNaN(reconcilerLag()))
		require.Less(t, reconcilerLag(), 15.0)
	})
}
