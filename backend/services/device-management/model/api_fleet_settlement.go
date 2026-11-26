// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var fleetSettlementUnknown = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "device_management_bulk_change_settlement_unknown_total",
	Help: "Bulk change lines skipped because a stored vocabulary value is unknown.",
})
var fleetReconcilerLag = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "device_management_bulk_change_reconciler_lag_seconds",
	Help: "Age of the oldest undecided report for a bulk change line.",
})

func init() {
	if ledger.Served {
		prometheus.MustRegister(fleetSettlementUnknown, fleetReconcilerLag)
	}
}

const fleetSettlementBatch = 1000

// fleetSettlementFastDispatch is every dispatch state that can hold a POSTED line. A PAUSED
// operation's posted lines keep settling: scope is the line, never the header's dispatch.
const fleetSettlementFastDispatch = "('RUNNING','PAUSED','DISPATCHED','ABORTED')"

// Tenants visited per tick. A fast tick must reach every active tenant well inside the
// 30 s report window (5 s cadence: up to five ticks), or a tenant's reports fall out of
// it and wait for the five-minute sweep; 100 per tick covers 500 active tenants.
// fleetSettlementDeadlineBound caps the lines one deadline transaction reads per tenant
// (≤ 10,000). A variable only so a test can exercise the bound without 10k rows.
var fleetSettlementDeadlineBound = 10000

const (
	fleetSettlementFastTenants     = 100
	fleetSettlementDeadlineTenants = 10
)

// Shared by SQL reads and the conditional writers. No joined line/report table
// has this column, so the predicate is unambiguous in the batched queries too.
const fleetSettlementProtocol = "driver_protocol >= 1 AND driver_protocol <= ?"

// Cursor state is process-local; correctness comes from the line CAS, never this cursor.
type fleetSettlementCursor struct {
	sync.Mutex
	after string
}

func fleetSettlementTable(db *gorm.DB, model string) string {
	return db.Statement.Quote(db.NamingStrategy.TableName(model))
}

func fleetSettlementNow(db *gorm.DB) any {
	if db.Dialector.Name() == "postgres" {
		return gorm.Expr("clock_timestamp()")
	}
	return time.Now().UTC()
}

// An uncorrelated scalar subquery evaluates the volatile wall clock once per
// statement, giving PostgreSQL an index range bound for receive time/deadline.
func fleetSettlementCutoff(db *gorm.DB, ago time.Duration) any {
	if db.Dialector.Name() == "postgres" {
		if ago == 0 {
			return gorm.Expr("(SELECT clock_timestamp())")
		}
		return gorm.Expr("(SELECT clock_timestamp() - (? * interval '1 second'))", ago.Seconds())
	}
	return time.Now().UTC().Add(-ago)
}

// fleetSettlementRead selects candidates, the first log decider, the uncaptured
// tail, the gap marker and the deciding revision's document in ONE snapshot.
// Explicit tenant predicates cover every join, including historical device ids.
func fleetSettlementRead(db *gorm.DB, tenant string, after uint, limit int, fast, due bool) ([]fleetSettlementRow, error) {
	l, o := fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	r, s := fleetSettlementTable(db, "DeviceConfigurationReport"), fleetSettlementTable(db, "DeviceConfigurationState")
	v, d := fleetSettlementTable(db, "DeviceConfigurationRevision"), fleetSettlementTable(db, "Device")
	where := ""
	recent, recentJoin := "", ""
	args := []any{tenant, after, ledger.SupportedDriverProtocol}
	if due {
		where += " AND l.settlement IS NULL AND l.settle_by <= ?"
		args = append(args, fleetSettlementCutoff(db, 0))
	}
	if fast {
		// Materialize the small indexed receive-time window before joining
		// targets. max(revision) admits candidates only; it NEVER decides them.
		recent = "recent AS (SELECT device_id, max(revision) revision FROM " + r + " WHERE tenant_id=? AND created_at>=? GROUP BY device_id), "
		recentJoin = " JOIN recent ON recent.device_id=l.target_id AND recent.revision>=l.posted_revision"
		where += " AND o.dispatch IN " + fleetSettlementFastDispatch + " AND o.evidence = 'OPEN'"
		cutoff := fleetSettlementCutoff(db, 30*time.Second)
		args = append([]any{tenant, cutoff}, args...)
	}
	args = append(args, limit)
	// IS NOT DISTINCT FROM matches sameCapturedPair, including reported_at.
	tail := "s.reported_revision >= c.posted_revision AND s.reported_status IN ('APPLIED','REJECTED') AND NOT (s.reported_revision IS NOT DISTINCT FROM s.captured_revision AND s.reported_status IS NOT DISTINCT FROM s.captured_status AND s.reported_at IS NOT DISTINCT FROM s.captured_reported_at)"
	decidingRevision := "CASE WHEN r.id IS NOT NULL THEN r.revision WHEN " + tail + " THEN s.reported_revision END"
	query := `WITH ` + recent + `candidates AS (
 SELECT l.*, o.intent FROM ` + l + ` l JOIN ` + o + ` o ON o.id=l.operation_id AND o.tenant_id=l.tenant_id` + recentJoin + `
 WHERE l.tenant_id=? AND l.id>? AND (` + fleetSettlementProtocol + `)
 AND l.posting='POSTED' AND l.evidence_revision IS NULL AND l.redacted_at IS NULL` + where + `
 ORDER BY l.id LIMIT ?)
 SELECT c.*, ` + decidingRevision + ` AS deciding_revision,
 CASE WHEN r.id IS NOT NULL THEN r.status WHEN ` + tail + ` THEN s.reported_status END AS deciding_status,
 CASE WHEN r.id IS NOT NULL THEN r.errors ELSE s.reported_errors END AS deciding_errors,
 r.id AS report_id, r.source AS report_source, s.capture_gap_through,
 v.document AS deciding_document, d.disabled_at AS device_disabled_at
 FROM candidates c
 LEFT JOIN ` + s + ` s ON s.tenant_id=c.tenant_id AND s.device_id=c.target_id
 LEFT JOIN ` + r + ` r ON r.tenant_id=c.tenant_id AND r.id=(SELECT first.id FROM ` + r + ` first
 WHERE first.tenant_id=c.tenant_id AND first.device_id=c.target_id AND first.revision>=c.posted_revision ORDER BY first.revision ASC, first.id ASC LIMIT 1)
 LEFT JOIN ` + v + ` v ON v.tenant_id=c.tenant_id AND v.device_id=c.target_id AND v.revision=(` + decidingRevision + `)
 LEFT JOIN ` + d + ` d ON d.tenant_id=c.tenant_id AND d.id=c.target_id`
	var rows []fleetSettlementRow
	err := db.Raw(query, args...).Scan(&rows).Error
	return rows, err
}

type fleetSettlementRow struct {
	FleetOperationLine `gorm:"embedded"`
	Intent             string
	DecidingRevision   sql.NullInt64
	DecidingStatus     sql.NullString
	DecidingErrors     []byte
	ReportId           sql.NullInt64
	ReportSource       sql.NullString
	CaptureGapThrough  sql.NullInt64
	DecidingDocument   sql.NullString
	DeviceDisabledAt   sql.NullTime
}

type fleetSettlementDocumentKey struct{ device, revision int64 }

func (row *fleetSettlementRow) verdict(documents map[fleetSettlementDocumentKey]map[string]ledger.Raw) (ledger.Verdict, error) {
	if row.Code == "TARGET_ERASED" {
		return ledger.Verdict{}, nil
	}
	if row.Settlement.Valid && !ledger.Settlement(row.Settlement.String).Valid() {
		fleetSettlementUnknown.Inc()
		return ledger.Verdict{}, nil
	}
	if !row.PostedRevision.Valid || !row.DecidingRevision.Valid {
		return ledger.Verdict{}, nil
	}
	if !configurationVerdict(row.DecidingStatus.String) {
		fleetSettlementUnknown.Inc()
		return ledger.Verdict{}, nil
	}
	if !row.DecidingDocument.Valid {
		return ledger.Verdict{}, fmt.Errorf("bulk change line %d: deciding revision document missing", row.ID)
	}
	var image fleetValueImage
	if !row.PostedValue.Valid {
		return ledger.Verdict{}, fmt.Errorf("bulk change line %d: posted image missing", row.ID)
	}
	if err := json.Unmarshal([]byte(row.PostedValue.String), &image); err != nil {
		return ledger.Verdict{}, err
	}
	p := ledger.PostedImage{Revision: &row.PostedRevision.Int64}
	// A key that is present with a NULL value is not in the device's document (BuildConfigurationDocument
	// leaves it out), so the device is expected to report it absent, exactly as for an unset. A reversal
	// that restores such a before-image posts that image.
	if image.Present && image.Value != nil {
		b, err := canonicalConfigurationValue(image.ValueType, image.ValueType, *image.Value)
		if err != nil {
			return ledger.Verdict{}, err
		}
		raw := json.RawMessage(b)
		p.Value = &raw
	}
	var intent FleetConfigSetIntent
	if err := json.Unmarshal([]byte(row.Intent), &intent); err != nil {
		return ledger.Verdict{}, err
	}
	key := fleetSettlementDocumentKey{row.TargetId.Int64, row.DecidingRevision.Int64}
	document, ok := documents[key]
	if !ok {
		// Full sweeps may span many operations; don't retain every decoded
		// 64 KiB document until the tenant's final chunk.
		if len(documents) >= fleetSettlementBatch {
			clear(documents)
		}
		if err := json.Unmarshal([]byte(row.DecidingDocument.String), &document); err != nil {
			return ledger.Verdict{}, err
		}
		documents[key] = document
	}
	var value *ledger.Raw
	if raw, ok := document[intent.Key]; ok {
		value = &raw
	}
	report := &ledger.Report{Revision: row.DecidingRevision.Int64, Status: row.DecidingStatus.String, FromLog: row.ReportId.Valid, Source: row.ReportSource.String}
	if row.CaptureGapThrough.Valid {
		report.GapThrough = &row.CaptureGapThrough.Int64
	}
	var errs []string
	_ = json.Unmarshal(row.DecidingErrors, &errs)
	if len(errs) > 0 {
		report.Detail = errs[0]
	}
	var current *ledger.Settlement
	if row.Settlement.Valid {
		s := ledger.Settlement(row.Settlement.String)
		current = &s
	}
	return ledger.Decide(p, report, value, current), nil
}

// applyFleetSettlement is the only settlement writer. Its predicate includes
// the state read by the decision and excludes redaction and foreign protocols.
func applyFleetSettlement(db *gorm.DB, row *fleetSettlementRow, verdict ledger.Verdict, deadline bool) (int64, error) {
	if row.Code == "TARGET_ERASED" {
		return 0, nil
	}
	if row.Settlement.Valid && !ledger.Settlement(row.Settlement.String).Valid() {
		return 0, nil
	}
	if !verdict.Decided && (!deadline || row.Settlement.Valid || row.DecidingRevision.Valid) {
		return 0, nil
	}
	changes := map[string]any{}
	if verdict.Decided {
		changes["evidence_basis"], changes["evidence_revision"] = verdict.Basis, verdict.Revision
		if row.Settlement.String == "RESOLVED_CONFIRMED" || row.Settlement.String == "RESOLVED_FAILED" {
			changes["evidence_flag"] = ""
			if verdict.Contradicts {
				changes["evidence_flag"] = "EVIDENCE_CONTRADICTS_VERDICT"
			}
		} else {
			changes["settlement"], changes["code"], changes["detail"], changes["settled_at"] = string(verdict.Settlement), verdict.Code, verdict.Detail, fleetSettlementNow(db)
		}
	} else {
		code, detail := "NO_EVIDENCE", ""
		if !row.PostedRevision.Valid {
			code = "NO_REVISION"
		}
		if row.DeviceDisabledAt.Valid {
			detail = "device disabled at deadline"
		}
		changes = map[string]any{"settlement": "AMBIGUOUS", "code": code, "detail": detail, "settled_at": fleetSettlementNow(db)}
	}
	write := func(tx *gorm.DB) (int64, error) {
		op := tx.Model(&FleetOperation{}).Select("id").Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol)
		q := tx.Model(&FleetOperationLine{}).Where("id = ? AND posting = 'POSTED' AND evidence_revision IS NULL AND redacted_at IS NULL AND code <> 'TARGET_ERASED' AND operation_id IN (?)", row.ID, op)
		if row.Settlement.Valid {
			q = q.Where("settlement = ?", row.Settlement.String)
		} else {
			q = q.Where("settlement IS NULL")
		}
		res := q.UpdateColumns(changes)
		if res.Error != nil || res.RowsAffected == 0 {
			return res.RowsAffected, res.Error
		}
		if verdict.Contradicts {
			event := FleetOperationEvent{OperationId: row.OperationId, LineId: sql.NullInt64{Int64: int64(row.ID), Valid: true}, Kind: "CONTRADICTING_EVIDENCE", At: time.Now().UTC(), Revision: sql.NullInt64{Int64: verdict.Revision, Valid: true}, Status: row.DecidingStatus}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event).Error; err != nil {
				return 0, err
			}
		}
		return res.RowsAffected, nil
	}
	if verdict.Contradicts {
		var n int64
		err := db.Transaction(func(tx *gorm.DB) error { var err error; n, err = write(tx); return err })
		return n, err
	}
	return write(db)
}

func fleetEvidenceCompleteSQL(db *gorm.DB) string {
	l := fleetSettlementTable(db, "FleetOperationLine")
	o := fleetSettlementTable(db, "FleetOperation")
	return "NOT EXISTS (SELECT 1 FROM " + l + " l WHERE l.tenant_id=" + o + ".tenant_id AND l.operation_id=" + o + ".id AND l.posting='POSTED' AND (l.settlement IS NULL OR (l.settlement='AMBIGUOUS' AND l.code<>'TARGET_ERASED')))"
}

func closeFleetEvidence(db *gorm.DB, operations map[int64]bool) error {
	for id := range operations {
		res := db.Model(&FleetOperation{}).Where("id = ? AND evidence='OPEN' AND dispatch IN ('DISPATCHED','ABORTED')", id).Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol).
			Where(fleetEvidenceCompleteSQL(db)).
			UpdateColumns(map[string]any{"evidence": "CLOSED", "closed_at": fleetSettlementNow(db)})
		if res.Error != nil {
			return res.Error
		}
	}
	return nil
}

// ReconcileFleetLines is callable independently of scheduling. fast uses the
// overlapping 30-second report window; false is the keyset full-sweep seam.
func (api *Api) ReconcileFleetLines(ctx context.Context, fast bool) (int64, error) {
	return api.settleFleetLines(ctx, fast, false)
}

// DeadlineFleetLines first gives committed evidence precedence. Only a line
// without a decider gets an AMBIGUOUS deadline verdict; it remains eligible.
func (api *Api) DeadlineFleetLines(ctx context.Context) (int64, error) {
	return api.settleFleetLines(ctx, false, true)
}

func (api *Api) settleFleetLines(ctx context.Context, fast, due bool) (int64, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return 0, fmt.Errorf("bulk change settlement requires a tenant")
	}
	db := api.RDB.DB(ctx)
	var total int64
	var after uint
	undecided := 0
	var lastUndecided error
	touched := map[int64]bool{}
	documents := make(map[fleetSettlementDocumentKey]map[string]ledger.Raw)
	pass := func(tx *gorm.DB) error {
		for scanned := 0; ; {
			rows, err := fleetSettlementRead(tx, tenant, after, fleetSettlementBatch, fast, due)
			if err != nil {
				return err
			}
			decide := func(w *gorm.DB) error {
				for i := range rows {
					row := &rows[i]
					after = row.ID
					touched[row.OperationId] = true
					verdict, err := row.verdict(documents)
					if err != nil {
						// One undecodable line must not stall the tenant, nor roll back a
						// deadline transaction for every other due line. It stays awaiting
						// evidence and the pass reports it.
						undecided++
						lastUndecided = err
						continue
					}
					n, err := applyFleetSettlement(w, row, verdict, due)
					if err != nil {
						return err
					}
					total += n
				}
				return nil
			}
			// One commit per chunk, not one per line: a per-line autocommit costs a
			// round trip and a WAL flush each and measured well under the 500
			// decisions/s target. Each UPDATE is still its own CAS; the chunk's row
			// locks are taken in id order, as the deadline transaction takes them.
			if due {
				err = decide(tx)
			} else {
				before := total
				if err = tx.Transaction(decide); err != nil {
					total = before
				}
			}
			if err != nil {
				return err
			}
			scanned += len(rows)
			if len(rows) < fleetSettlementBatch || (due && scanned >= fleetSettlementDeadlineBound) || (fast && scanned >= 3000) {
				return nil
			}
		}
	}
	var err error
	if due {
		err = db.Transaction(pass)
	} else {
		err = pass(db)
	}
	if err != nil {
		return total, err
	}
	// Retry a prior pass's failed close after its last line left the awaiting
	// index. Filter ready headers before LIMIT so unresolved ones cannot starve it.
	var closable []FleetOperation
	if err := db.Select("id").Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol).
		Where("evidence='OPEN' AND dispatch IN ('DISPATCHED','ABORTED')").Where(fleetEvidenceCompleteSQL(db)).Order("id").Limit(100).Find(&closable).Error; err != nil {
		return total, err
	}
	for _, op := range closable {
		touched[int64(op.ID)] = true
	}
	// Autocommit: never close a header while retaining line locks.
	if err := closeFleetEvidence(db, touched); err != nil {
		return total, err
	}
	if undecided > 0 {
		return total, fmt.Errorf("%d bulk change lines could not be decided (last: %w)", undecided, lastUndecided)
	}
	return total, nil
}

// FleetReportCaptureComplete answers only for this operation's targets and its
// relevant history. A missing state or an uncaptured tail is incomplete.
func (api *Api) FleetReportCaptureComplete(ctx context.Context, op *FleetOperation) (bool, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return false, core.ErrNoTenant
	}
	if op.TenantId != tenant {
		return false, gorm.ErrRecordNotFound
	}
	db := api.RDB.DB(ctx)
	l, s := fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "DeviceConfigurationState")
	var count int64
	err := db.Raw(`SELECT count(*) FROM `+l+` l LEFT JOIN `+s+` s ON s.device_id=l.target_id AND s.tenant_id=l.tenant_id
 WHERE l.tenant_id=? AND l.operation_id=? AND (s.capture_complete_since IS NULL
 OR s.capture_complete_since > coalesce(l.posted_at,?)
 OR (l.posted_revision IS NOT NULL AND s.capture_gap_through >= l.posted_revision)
 OR NOT (s.reported_revision IS NOT DISTINCT FROM s.captured_revision AND s.reported_status IS NOT DISTINCT FROM s.captured_status AND s.reported_at IS NOT DISTINCT FROM s.captured_reported_at))`, tenant, op.ID, op.CreatedAt).Scan(&count).Error
	return count == 0, err
}

// Settlement passes discover ids under system context, then leave that context
// before reading evidence. A bounded number of tenants per tick, cursor plus wrap
// for fairness.
func (api *Api) settlementTenants(ctx context.Context, cursor *fleetSettlementCursor, fast, due bool) ([]string, error) {
	cursor.Lock()
	after := cursor.after
	cursor.Unlock()
	discover := func(predicate string, limit int) ([]string, error) {
		var tenants []string
		db := api.RDB.DB(core.WithSystemContext(ctx))
		q := db.Model(&FleetOperation{}).Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol)
		if fast {
			q = q.Where("dispatch IN " + fleetSettlementFastDispatch + " AND evidence='OPEN'")
		}
		if due {
			l, o := fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
			q = q.Where("EXISTS (SELECT 1 FROM "+l+" l WHERE l.tenant_id="+o+".tenant_id AND l.operation_id="+o+".id AND l.posting='POSTED' AND l.settlement IS NULL AND l.settle_by<=?)", fleetSettlementCutoff(db, 0))
		}
		err := q.Where(predicate, after).Distinct("tenant_id").Order("tenant_id").Limit(limit).Pluck("tenant_id", &tenants).Error
		return tenants, err
	}
	limit := fleetSettlementDeadlineTenants
	if fast {
		limit = fleetSettlementFastTenants
	}
	tenants, err := discover("tenant_id > ?", limit)
	if err != nil {
		return nil, err
	}
	if after != "" && len(tenants) < limit {
		more, err := discover("tenant_id <= ?", limit-len(tenants))
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, more...)
	}
	if len(tenants) > 0 {
		cursor.Lock()
		cursor.after = tenants[len(tenants)-1]
		cursor.Unlock()
	}
	return tenants, nil
}

func (api *Api) RunFleetSettlement(ctx context.Context, fast, due bool) error {
	cursor := &api.fleetSettlementFastCursor
	if due {
		cursor = &api.fleetSettlementDeadlineCursor
	}
	tenants, err := api.settlementTenants(ctx, cursor, fast, due)
	if err != nil {
		return err
	}
	failed := 0
	var last error
	var lag time.Duration
	for _, tenant := range tenants {
		tenantCtx := core.WithTenant(ctx, tenant)
		_, err := api.settleFleetLines(tenantCtx, fast, due)
		if err == nil && !due {
			var age time.Duration
			age, err = api.FleetReconcilerLag(tenantCtx)
			if age > lag {
				lag = age
			}
		}
		if err != nil {
			failed++
			last = err
		}
	}
	if !due && failed == 0 {
		fleetReconcilerLag.Set(lag.Seconds())
	}
	return core.PassResult(len(tenants), failed, last)
}

// FleetReconcilerLag measures outstanding report age within the caller's tenant.
func (api *Api) FleetReconcilerLag(ctx context.Context) (time.Duration, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return 0, core.ErrNoTenant
	}
	db := api.RDB.DB(ctx)
	r, l, o := fleetSettlementTable(db, "DeviceConfigurationReport"), fleetSettlementTable(db, "FleetOperationLine"), fleetSettlementTable(db, "FleetOperation")
	var oldest sql.NullTime
	err := db.Raw(`SELECT min(r.created_at) FROM `+r+` r JOIN `+l+` l ON l.target_id=r.device_id AND l.tenant_id=r.tenant_id AND l.posted_revision<=r.revision
 JOIN `+o+` o ON o.id=l.operation_id AND o.tenant_id=l.tenant_id
 WHERE r.tenant_id=? AND l.posting='POSTED' AND l.evidence_revision IS NULL AND l.redacted_at IS NULL
 AND (`+fleetSettlementProtocol+`)`, tenant, ledger.SupportedDriverProtocol).Scan(&oldest).Error
	if err != nil || !oldest.Valid {
		return 0, err
	}
	var now time.Time
	if db.Dialector.Name() == "postgres" {
		if err := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return 0, err
		}
	} else {
		now = time.Now()
	}
	age := now.Sub(oldest.Time)
	if age < 0 {
		age = 0
	}
	return age, nil
}

// SweepFleetSettlement runs at startup and every five minutes. It scans every
// tenant, including CLOSED operations and uncaptured state tails.
func (api *Api) SweepFleetSettlement(ctx context.Context) error {
	pass := func() error {
		var tenants []string
		// System context lists tenant IDs only; each tenant's sweep runs under core.WithTenant.
		err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&FleetOperation{}).Where(fleetSettlementProtocol, ledger.SupportedDriverProtocol).Distinct("tenant_id").Order("tenant_id").Pluck("tenant_id", &tenants).Error
		if err != nil {
			return err
		}
		// One tenant's failure must not starve the tenants after it.
		failed := 0
		var last error
		for _, tenant := range tenants {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := api.ReconcileFleetLines(core.WithTenant(ctx, tenant), false); err != nil {
				failed++
				last = err
			}
		}
		return core.PassResult(len(tenants), failed, last)
	}
	if api.RDB.Database.Dialector.Name() != "postgres" {
		return pass()
	}
	return api.RDB.SingletonPass(ctx, "device-management-bulk-change-settlement-sweep", pass)
}
