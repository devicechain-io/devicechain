// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The background settlement passes are measured here by what their plans do, not in
// milliseconds: the journal rows a statement reads (fleet_operation_lines rows and index entries,
// every node and loop), with its shared buffers alongside. Both are properties of the plan, they
// do not move with the host's load, and the row count is what grows when a plan stops being
// bounded (an in-memory sort of the whole set costs few buffers and every row). Every measurement
// is EXPLAIN (ANALYZE, BUFFERS) of the very statement the pass issues, captured from its own gorm
// session.

// sqlCapture records the statements a session runs, with their arguments inlined.
type sqlCapture struct {
	logger.Interface
	statements []string
}

func (c *sqlCapture) LogMode(logger.LogLevel) logger.Interface { return c }

func (c *sqlCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, _ := fc()
	c.statements = append(c.statements, statement)
}

func capturing(db *gorm.DB) (*gorm.DB, *sqlCapture) {
	c := &sqlCapture{Interface: logger.Discard}
	return db.Session(&gorm.Session{Logger: c}), c
}

// planCost is what one execution of a statement did.
type planCost struct {
	// LineRows counts the fleet_operation_lines rows and index entries the plan's nodes produced
	// or filtered away, times their loops.
	LineRows int64
	// Buffers is the shared buffers touched (hit + read, every node and subplan; planning excluded).
	Buffers int64
	// Ms is the execution time, logged for the reader and never asserted: it moves with the host.
	Ms float64
}

type planNode struct {
	Relation string     `json:"Relation Name"`
	Index    string     `json:"Index Name"`
	NodeType string     `json:"Node Type"`
	Rows     float64    `json:"Actual Rows"`
	Loops    float64    `json:"Actual Loops"`
	Filtered float64    `json:"Rows Removed by Filter"`
	Recheck  float64    `json:"Rows Removed by Index Recheck"`
	Hit      int64      `json:"Shared Hit Blocks"`
	Read     int64      `json:"Shared Read Blocks"`
	Plans    []planNode `json:"Plans"`
}

func (n planNode) lineRows() int64 {
	var own float64
	// A bitmap index scan names only its index; every other scan names its relation.
	if n.Relation == "fleet_operation_lines" || (n.NodeType == "Bitmap Index Scan" && strings.Contains(n.Index, "fleet_operation_lines")) {
		own = (n.Rows + n.Filtered + n.Recheck) * n.Loops
	}
	total := int64(own)
	for _, child := range n.Plans {
		total += child.lineRows()
	}
	return total
}

// measure runs statement under EXPLAIN (ANALYZE, BUFFERS) and reports what it did.
func measure(t *testing.T, db *gorm.DB, statement string) planCost {
	t.Helper()
	var out string
	require.NoError(t, db.Raw("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+statement).Row().Scan(&out))
	var plans []struct {
		Plan planNode `json:"Plan"`
		Ms   float64  `json:"Execution Time"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &plans))
	require.Len(t, plans, 1)
	if os.Getenv("BCPERF") != "" {
		var text []string
		require.NoError(t, db.Raw("EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+statement).Pluck("QUERY PLAN", &text).Error)
		t.Logf("%s\n%s", statement, strings.Join(text, "\n"))
	}
	return planCost{LineRows: plans[0].Plan.lineRows(), Buffers: plans[0].Plan.Hit + plans[0].Plan.Read, Ms: plans[0].Ms}
}

// costFixture is one tenant shaped like the P-1 caps tenant (a history of aborted 10,000-target
// changes whose posted lines a deadline settled AMBIGUOUS, one change with lines just past their
// deadline, one RUNNING change with lines posted and not yet due) and noise tenants whose changes
// settled from evidence. Lines per change are scaled down; the shapes are the drill's.
type costFixture struct {
	db      *gorm.DB
	prefix  string
	active  string
	devices int64
}

const (
	costLinesPerOp  = 2000
	costPostedPerOp = 1480 // 74 %, as the caps arms posted
	costDuePerOp    = 300
)

func newCostFixture(t *testing.T, api *Api) *costFixture {
	t.Helper()
	prefix := fmt.Sprintf("bccost%d-", time.Now().UnixNano())
	f := &costFixture{db: api.RDB.DB(core.WithSystemContext(context.Background())), prefix: prefix, active: prefix + "active", devices: 7_000_000_000 + time.Now().UnixNano()%1_000_000*10_000}
	t.Cleanup(func() {
		for _, table := range []string{"fleet_operation_lines", "fleet_operation_runtime", "fleet_operations"} {
			require.NoError(t, f.db.Exec(`DELETE FROM "device-management"."`+table+`" WHERE tenant_id LIKE ?`, prefix+"%").Error)
		}
	})
	return f
}

// addOperations inserts n operations for tenant, each with costLinesPerOp lines, through literal
// SQL so a fixture of several hundred thousand lines takes seconds. kind picks the line shape.
func (f *costFixture) addOperations(t *testing.T, tenant, kind string, n int, protocol int) {
	t.Helper()
	dispatch, evidence := "ABORTED", "OPEN"
	switch kind {
	case "running":
		dispatch = "RUNNING"
	case "noise":
		dispatch, evidence = "DISPATCHED", "CLOSED"
	}
	require.NoError(t, f.db.Exec(`INSERT INTO "device-management"."fleet_operations" (created_at, updated_at, tenant_id, token, kind, name, dispatch, evidence, request_digest, driver_protocol, intent, target_kind, target_count, max_posts_per_second, eff_posts_per_second, settle_deadline_s, initiator)
 SELECT now(), now(), ?, ? || k || '-' || md5(random()::text), 'config.set', 'cost', ?, ?, 'x', ?, '{"key":"k"}', 'DEVICE_LIST', ?, 50, 25, 3600, 't' FROM generate_series(1, ?) k`,
		tenant, kind, dispatch, evidence, protocol, costLinesPerOp, n).Error)
	// Each operation's lines target its own block of synthetic devices; the journal has no device FK.
	lines := `INSERT INTO "device-management"."fleet_operation_lines" (tenant_id, operation_id, ordinal, target_id, posting, settlement, code, posted_value, posted_revision, posted_at, settle_by, settled_at, evidence_basis, evidence_revision)
 SELECT o.tenant_id, o.id, g, ? + o.id * 10000 + g, %s FROM "device-management"."fleet_operations" o CROSS JOIN generate_series(1, ?) g
 WHERE o.tenant_id = ? AND o.token LIKE ? AND NOT EXISTS (SELECT 1 FROM "device-management"."fleet_operation_lines" x WHERE x.operation_id = o.id)`
	var shape string
	switch kind {
	case "history": // posted lines past their deadline, settled AMBIGUOUS, still awaiting evidence
		shape = fmt.Sprintf(`CASE WHEN g <= %[1]d THEN 'POSTED' ELSE 'CANCELLED' END, CASE WHEN g <= %[1]d THEN 'AMBIGUOUS' END, CASE WHEN g <= %[1]d THEN 'NO_EVIDENCE' ELSE '' END,
 CASE WHEN g <= %[1]d THEN '{"present":true,"valueType":"STRING","value":"v"}' END, CASE WHEN g <= %[1]d THEN 5 END, CASE WHEN g <= %[1]d THEN now() - interval '2 hours' END,
 CASE WHEN g <= %[1]d THEN now() - interval '1 hour' END, CASE WHEN g <= %[1]d THEN now() - interval '1 hour' END, NULL, NULL`, costPostedPerOp)
	case "due": // posted lines just past their deadline, not yet settled
		shape = fmt.Sprintf(`CASE WHEN g <= %[1]d THEN 'POSTED' ELSE 'CANCELLED' END, NULL, '', CASE WHEN g <= %[1]d THEN '{"present":true,"valueType":"STRING","value":"v"}' END,
 CASE WHEN g <= %[1]d THEN 5 END, CASE WHEN g <= %[1]d THEN now() - interval '1 hour' END, CASE WHEN g <= %[1]d THEN now() - interval '10 seconds' END, NULL, NULL, NULL`, costDuePerOp)
	case "running": // posted and not yet due, then planned
		shape = fmt.Sprintf(`CASE WHEN g <= %[1]d THEN 'POSTED' ELSE 'PLANNED' END, NULL, '', CASE WHEN g <= %[1]d THEN '{"present":true,"valueType":"STRING","value":"v"}' END,
 CASE WHEN g <= %[1]d THEN 5 END, CASE WHEN g <= %[1]d THEN now() END, CASE WHEN g <= %[1]d THEN now() + interval '1 hour' END, NULL, NULL, NULL`, costPostedPerOp/2)
	case "noise": // settled from evidence and closed
		shape = `'POSTED', 'CONFIRMED', '', '{"present":true,"valueType":"STRING","value":"v"}', 5, now() - interval '1 day', now() - interval '23 hours', now() - interval '23 hours', 'REPORT_LOG', 5`
	default:
		t.Fatalf("unknown fixture kind %q", kind)
	}
	require.NoError(t, f.db.Exec(fmt.Sprintf(lines, shape), f.devices, costLinesPerOp, tenant, kind+"%").Error)
	if kind == "running" {
		require.NoError(t, f.db.Exec(`INSERT INTO "device-management"."fleet_operation_runtime" (operation_id, tenant_id) SELECT id, tenant_id FROM "device-management"."fleet_operations" o
 WHERE tenant_id = ? AND dispatch = 'RUNNING' AND NOT EXISTS (SELECT 1 FROM "device-management"."fleet_operation_runtime" r WHERE r.operation_id = o.id)`, tenant).Error)
	}
}

func (f *costFixture) analyze(t *testing.T) {
	t.Helper()
	for _, table := range []string{"fleet_operations", "fleet_operation_lines", "fleet_operation_runtime"} {
		require.NoError(t, f.db.Exec(`ANALYZE "device-management"."`+table+`"`).Error)
	}
}

// fleetDueTenantsBeforeForTest is the deadline discovery this change replaced, kept verbatim as
// the measurement's "before" and as the reference its answers are compared against.
func fleetDueTenantsBeforeForTest(db *gorm.DB, after string, wrap bool, limit int) ([]string, error) {
	var tenants []string
	q := db.Model(&FleetOperation{}).Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol)
	l, o := fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	q = q.Where("EXISTS (SELECT 1 FROM "+l+" l WHERE l.tenant_id="+o+".tenant_id AND l.operation_id="+o+".id AND l.posting='POSTED' AND l.settlement IS NULL AND l.settle_by<=?)", fleetSettlementCutoff(db, 0))
	predicate := "tenant_id > ?"
	if wrap {
		predicate = "tenant_id <= ?"
	}
	err := q.Where(predicate, after).Distinct("tenant_id").Order("tenant_id").Limit(limit).Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// measureCall runs call on a capturing session and measures the one statement it issued.
func measureCall(t *testing.T, db *gorm.DB, call func(*gorm.DB) error) planCost {
	t.Helper()
	session, capture := capturing(db)
	require.NoError(t, call(session))
	require.Len(t, capture.statements, 1, "one statement")
	return measure(t, db, capture.statements[0])
}

// withoutSkipScan runs fn in a transaction with TimescaleDB's SkipScan off. set_config accepts the
// name whether or not the extension is loaded, so this is a no-op on stock PostgreSQL.
func withoutSkipScan(t *testing.T, db *gorm.DB, fn func(tx *gorm.DB)) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('timescaledb.enable_skipscan', 'off', true)").Error; err != nil {
			return err
		}
		fn(tx)
		return nil
	}))
}

// onlyPrefixed keeps this fixture's tenants: the shared database holds other tests' rows too.
func onlyPrefixed(tenants []string, prefix string) []string {
	out := []string{}
	for _, t := range tenants {
		if strings.HasPrefix(t, prefix) {
			out = append(out, t)
		}
	}
	return out
}

// The deadline pass's tenant discovery costs the same however many operations the instance
// retains. The discovery it replaced probed every retained operation's lines, every 30 s on
// every replica, whether or not anything was due; the drill measured it at 47 ms per call growing
// to 270 ms as one tenant's history grew. Measured here before and after on the same rows, at two
// history sizes, with the answers compared tenant for tenant.
func TestSettlementDeadlineDiscoveryCostIsFlatInRetainedHistoryOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	f := newCostFixture(t, api)
	for i := range 10 {
		f.addOperations(t, fmt.Sprintf("%snoise%03d", f.prefix, i), "noise", 2, 1)
	}
	f.addOperations(t, f.active, "running", 2, 1)
	f.addOperations(t, f.active, "due", 1, 1)
	// A tenant whose only due line belongs to an operation this release does not drive is never
	// discovered, and one with only not-yet-due lines is not either.
	f.addOperations(t, f.prefix+"newer", "due", 1, int(ledger.SupportedDriverProtocol)+1)
	f.addOperations(t, f.prefix+"notdue", "running", 1, 1)

	var before, after [2]planCost
	for i, history := range []int{4, 24} {
		var have int64
		require.NoError(t, f.db.Model(&FleetOperation{}).Where("tenant_id = ? AND token LIKE 'history%'", f.active).Count(&have).Error)
		f.addOperations(t, f.active, "history", history-int(have), 1)
		f.analyze(t)
		var got, want []string
		// The relational database is stock PostgreSQL. The test image carries TimescaleDB, whose
		// SkipScan would let the replaced DISTINCT stop at each tenant's first due operation, so
		// both sides are measured with it off: the plans production runs.
		withoutSkipScan(t, f.db, func(tx *gorm.DB) {
			after[i] = measureCall(t, tx, func(db *gorm.DB) (err error) { got, err = fleetDueTenants(db, "", "", 1000); return })
			before[i] = measureCall(t, tx, func(db *gorm.DB) (err error) {
				want, err = fleetDueTenantsBeforeForTest(db, "", false, 1000)
				return
			})
		})
		require.Equal(t, want, got, "the new discovery answers exactly what the old one did")
		require.Equal(t, []string{f.active}, onlyPrefixed(got, f.prefix))
		t.Logf("history %2d changes: discovery before %+v, after %+v", history, before[i], after[i])
	}
	// The before side is the instrument's negative control: on the same rows the old plan must
	// read more, and more again as the history grows, or this test could not tell them apart.
	require.Greater(t, before[1].LineRows, before[0].LineRows+5000, "the replaced discovery grew with retained history")
	require.Greater(t, before[1].LineRows, 10*after[1].LineRows, "before/after on the same rows")
	// The guarantee: flat in retained history.
	require.Equal(t, after[0].LineRows, after[1].LineRows, "discovery must not grow with retained operations")

	// Cursor wrap: (after, inf) then (-inf, after], exactly as before.
	for _, from := range []string{f.active, f.prefix + "a", f.prefix + "zzz"} {
		for _, wrap := range []bool{false, true} {
			lo, hi := from, ""
			if wrap {
				lo, hi = "", from
			}
			got, err := fleetDueTenants(f.db, lo, hi, 1000)
			require.NoError(t, err)
			want, err := fleetDueTenantsBeforeForTest(f.db, from, wrap, 1000)
			require.NoError(t, err)
			require.Equal(t, append([]string{}, want...), append([]string{}, got...), "after %q wrap %v", from, wrap)
		}
	}
	// The limit holds.
	got, err := fleetDueTenants(f.db, "", "", 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// fleetSweepPageBeforeForTest is the page selection this change replaced (the operations joined
// first, the page cut after), kept as the measurement's "before".
func fleetSweepPageBeforeForTest(db *gorm.DB, tenant string, after uint, limit int) ([]uint, error) {
	l, o := fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	var ids []uint
	err := db.Raw(`SELECT l.id FROM `+l+` l JOIN `+o+` o ON o.id=l.operation_id AND o.tenant_id=l.tenant_id
 WHERE l.tenant_id=? AND l.id>? AND (`+fleetSettlementProtocol+`)
 AND l.posting='POSTED' AND l.evidence_revision IS NULL AND l.redacted_at IS NULL
 ORDER BY l.id LIMIT ?`, tenant, after, ledger.SupportedDriverProtocol, limit).Scan(&ids).Error
	return ids, err
}

// A settlement sweep page reads about the rows it returns, however many lines the tenant holds
// awaiting evidence. Before, each page read every awaiting line of the tenant and sorted them to
// keep a thousand, so a sweep of N lines did that N/1000 times. Measured on the same rows: the
// read as it is now; then, inside a rolled-back transaction that drops
// ix_fleet_operation_lines_tenant_awaiting, the replaced page selection (the state before this
// change) and the read as it is now without its index.
func TestSettlementSweepPageCostIsBoundedOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	// Two layouts of the same rows, because which plan a join-first read gets depends on them.
	// Interleaved: the tenant's history alternates with other tenants' settled changes, four of
	// theirs to each of its own, as ids are handed out on a shared instance. Contiguous: one
	// tenant's changes created back to back, the others' after. Either way a plan that is not
	// tenant-led reads far more than a page, by id or by operation.
	for _, interleaved := range []bool{true, false} {
		t.Run(fmt.Sprintf("interleaved=%v", interleaved), func(t *testing.T) { sweepPageCost(t, api, interleaved) })
	}
}

func sweepPageCost(t *testing.T, api *Api, interleaved bool) {
	f := newCostFixture(t, api)
	for i := range 24 {
		f.addOperations(t, f.active, "history", 1, 1)
		if interleaved {
			f.addOperations(t, fmt.Sprintf("%snoise%03d", f.prefix, i), "noise", 4, 1)
		}
	}
	if !interleaved {
		for i := range 24 {
			f.addOperations(t, fmt.Sprintf("%snoise%03d", f.prefix, i), "noise", 4, 1)
		}
	}
	f.addOperations(t, f.active, "running", 2, 1)
	// A newer release's operation's lines are never a sweep candidate, and do not shorten a page.
	f.addOperations(t, f.active, "history", 1, int(ledger.SupportedDriverProtocol)+1)
	f.analyze(t)
	var awaiting int64
	require.NoError(t, f.db.Model(&FleetOperationLine{}).Where("tenant_id = ? AND posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL", f.active).Count(&awaiting).Error)
	require.Greater(t, awaiting, int64(30000))

	// The first page, and one from the middle of the keyset walk.
	var middle uint
	require.NoError(t, f.db.Model(&FleetOperationLine{}).Select("id").Where("tenant_id = ? AND posting = 'POSTED'", f.active).
		Order("id").Offset(int(awaiting/2)).Limit(1).Scan(&middle).Error)
	starts := []uint{0, middle}
	page := func(db *gorm.DB, from uint) ([]uint, planCost) {
		var rows []fleetSettlementRow
		cost := measureCall(t, db, func(db *gorm.DB) (err error) {
			rows, err = fleetSettlementRead(db, f.active, from, fleetSettlementBatch, false, false)
			return
		})
		ids := make([]uint, len(rows))
		for i := range rows {
			ids[i] = rows[i].ID
		}
		return ids, cost
	}
	var now, before, unindexed [2]planCost
	pages := make([][]uint, len(starts))
	for i, from := range starts {
		pages[i], now[i] = page(f.db, from)
		require.Len(t, pages[i], fleetSettlementBatch)
		// The guarantee: a page reads the page.
		require.LessOrEqual(t, now[i].LineRows, int64(fleetSettlementBatch), "page %d read more than a page: %+v", i, now[i])
	}
	// Before this change there was no tenant-led index and the page was cut after the join.
	rollback := errors.New("roll back the dropped index")
	require.ErrorIs(t, f.db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`DROP INDEX "device-management".ix_fleet_operation_lines_tenant_awaiting`).Error)
		for i, from := range starts {
			var want []uint
			before[i] = measureCall(t, tx, func(db *gorm.DB) (err error) {
				want, err = fleetSweepPageBeforeForTest(db, f.active, from, fleetSettlementBatch)
				return
			})
			require.Equal(t, want, pages[i], "the page is the same lines, in the same order")
			_, unindexed[i] = page(tx, from)
		}
		return rollback
	}), rollback)
	t.Logf("%d awaiting lines, pages from the start and the middle: before %+v, now %+v, now without the index %+v", awaiting, before, now, unindexed)
	for i := range starts {
		// Negative controls: the replaced selection, and this read without its index, each read
		// several pages' worth of lines to return one.
		require.GreaterOrEqual(t, before[i].LineRows, int64(4*fleetSettlementBatch))
		require.GreaterOrEqual(t, unindexed[i].LineRows, int64(4*fleetSettlementBatch))
	}
}
