// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Calculated-metric backfill jobs (ADR-082, S8). A job fills the calculated metrics a
// published profile version defines into stored readings that have none. This file is the job
// record and its GUARDED TRANSITIONS; the worker that runs a job is the processor/backfill
// package, and nothing creates a job in production yet but CreateBackfill's future caller.
//
// 🔴 EVERY WRITE A WORKER MAKES CARRIES ITS CLAIM EPOCH. A claim increments claim_epoch, and
// each later UPDATE names the epoch it was granted, so a worker that lost its lock (a
// failover, a terminated session) and keeps going finds zero rows to update and stops. The
// advisory lock is an optimisation; the epoch is the guarantee.

// Backfill job statuses. A job is ACTIVE while QUEUED or RUNNING, and a tenant may have one.
const (
	BackfillQueued    = "QUEUED"
	BackfillRunning   = "RUNNING"
	BackfillSucceeded = "SUCCEEDED"
	BackfillCancelled = "CANCELLED"
	BackfillFailed    = "FAILED"
)

// Backfill phases of a RUNNING job: FILL walks the devices and their windows, REFRESH then
// refreshes the rollup for the windows the continuous-aggregate policy will not reach.
const (
	BackfillPhaseFill    = "FILL"
	BackfillPhaseRefresh = "REFRESH"
)

// ErrBackfillLost is what a guarded write returns when it matched no row: the job was
// cancelled, finished, or claimed by another worker since this one last wrote. The caller
// stops; it must not retry.
var ErrBackfillLost = errors.New("the backfill job was cancelled or claimed by another worker")

// ErrBackfillActive is what CreateBackfill returns when the tenant already has an active job.
// ActiveToken names it.
type ErrBackfillActive struct{ ActiveToken string }

func (e *ErrBackfillActive) Error() string {
	return fmt.Sprintf("the tenant already has an active calculated-metric backfill %q", e.ActiveToken)
}

// CalculatedBackfill is one backfill job. The frozen Plan is the device-management door's
// response, verbatim: the worker never asks for the plan again.
//
// Every counter is exact across restarts and failovers, because a batch's rows and the
// counters that describe them commit in one transaction.
type CalculatedBackfill struct {
	rdb.TenantScoped
	Id                  int64 `gorm:"primaryKey"`
	Token               string
	ProfileToken        string
	ProfileVersionToken string
	ProfileVersion      int64
	MetricKeys          json.RawMessage `gorm:"type:jsonb"`
	Plan                json.RawMessage `gorm:"type:jsonb"`
	Warnings            json.RawMessage `gorm:"type:jsonb"`
	DeviceFilter        json.RawMessage `gorm:"type:jsonb"`
	RangeFrom           time.Time
	RangeTo             time.Time
	Status              string
	ClaimEpoch          int64
	Phase               string
	// RosterAfterId and CursorWindow are the FILL cursor: the device batch after
	// RosterAfterId is in progress, and its windows newer than CursorWindow are done (nil:
	// none). RefreshCursor is the REFRESH cursor: windows ending at or before it are done.
	RosterAfterId int64
	CursorWindow  *time.Time
	RefreshCursor *time.Time
	DevicesTotal  *int64

	DevicesDone int64
	// Samples are the samples evaluated: the readings of one event at one instant.
	Samples int64
	// Computed are the (sample, selected key) pairs that produced a value or already held
	// one; Computed = Written + AlreadyPresent.
	Computed int64
	// Written are the rows inserted, as the database counted them.
	Written int64
	// AlreadyPresent are the pairs whose row existed, at read time or by the time of the
	// insert (a live write won the race).
	AlreadyPresent int64
	// WindowsCompressed and WindowsBeyondRetention count device-batch x chunk-window pairs
	// skipped, so a long job over many devices counts a chunk once per batch.
	WindowsCompressed      int64
	WindowsBeyondRetention int64
	WindowsRefreshed       int64
	// Skipped counts the pairs that produced no value, by calcfield skip reason.
	Skipped json.RawMessage `gorm:"type:jsonb"`

	Failure           *string
	CreatedBy         *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
	CancelRequestedAt *time.Time
}

// SkippedCounts decodes Skipped.
func (j *CalculatedBackfill) SkippedCounts() (map[string]int64, error) {
	out := map[string]int64{}
	if len(j.Skipped) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(j.Skipped, &out); err != nil {
		return nil, fmt.Errorf("decoding the skip counts of backfill %q: %w", j.Token, err)
	}
	return out, nil
}

// BackfillDelta is what one committed unit of work adds to a job's counters, and where it
// leaves the cursors.
type BackfillDelta struct {
	DevicesDone            int64
	Samples                int64
	Computed               int64
	Written                int64
	AlreadyPresent         int64
	WindowsCompressed      int64
	WindowsBeyondRetention int64
	WindowsRefreshed       int64
	// Skipped is the job's whole skip map AFTER this unit, not an increment: the worker is the
	// only writer under its epoch, so an absolute value is exact and cheaper than a jsonb
	// merge. Nil leaves the stored map alone.
	Skipped map[string]int64

	// Cursors: nil leaves a cursor alone.
	Phase         *string
	RosterAfterId *int64
	CursorWindow  **time.Time // pointer to a pointer: set to nil is a reset
	RefreshCursor **time.Time
	DevicesTotal  *int64
}

// CommitBackfillProgress adds delta to the job's counters and moves its cursors, guarded by
// the claim epoch, in the caller's transaction. It matches no row — ErrBackfillLost — when the
// job was cancelled, finished or claimed since; the caller rolls its transaction back.
func CommitBackfillProgress(tx *gorm.DB, token string, epoch int64, d BackfillDelta) error {
	set := map[string]any{
		"updated_at":               gorm.Expr("now()"),
		"devices_done":             gorm.Expr("devices_done + ?", d.DevicesDone),
		"samples":                  gorm.Expr("samples + ?", d.Samples),
		"computed":                 gorm.Expr("computed + ?", d.Computed),
		"written":                  gorm.Expr("written + ?", d.Written),
		"already_present":          gorm.Expr("already_present + ?", d.AlreadyPresent),
		"windows_compressed":       gorm.Expr("windows_compressed + ?", d.WindowsCompressed),
		"windows_beyond_retention": gorm.Expr("windows_beyond_retention + ?", d.WindowsBeyondRetention),
		"windows_refreshed":        gorm.Expr("windows_refreshed + ?", d.WindowsRefreshed),
	}
	if d.Skipped != nil {
		raw, err := json.Marshal(d.Skipped)
		if err != nil {
			return err
		}
		set["skipped"] = gorm.Expr("?::jsonb", string(raw))
	}
	if d.Phase != nil {
		set["phase"] = *d.Phase
	}
	if d.RosterAfterId != nil {
		set["roster_after_id"] = *d.RosterAfterId
	}
	if d.CursorWindow != nil {
		set["cursor_window"] = *d.CursorWindow // a nil *time.Time stores NULL
	}
	if d.RefreshCursor != nil {
		set["refresh_cursor"] = *d.RefreshCursor
	}
	if d.DevicesTotal != nil {
		set["devices_total"] = *d.DevicesTotal
	}
	res := tx.Model(&CalculatedBackfill{}).
		Where("token = ? AND status = ? AND claim_epoch = ? AND cancel_requested_at IS NULL",
			token, BackfillRunning, epoch).
		Updates(set)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrBackfillLost
	}
	return nil
}

// ClaimBackfill takes a job for a worker: QUEUED or RUNNING becomes RUNNING with its epoch
// incremented, and the row as it now stands is returned. A job in any other status (or one
// that does not exist) is ErrBackfillLost. The tenant comes from the context.
func ClaimBackfill(db *gorm.DB, token string) (*CalculatedBackfill, error) {
	var job CalculatedBackfill
	res := db.Model(&job).Clauses(clause.Returning{}).
		Where("token = ? AND status IN ?", token, []string{BackfillQueued, BackfillRunning}).
		Updates(map[string]any{
			"status":      BackfillRunning,
			"claim_epoch": gorm.Expr("claim_epoch + 1"),
			"started_at":  gorm.Expr("coalesce(started_at, now())"),
			"updated_at":  gorm.Expr("now()"),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrBackfillLost
	}
	return &job, nil
}

// FinishBackfill ends a job the worker holds: status becomes SUCCEEDED or FAILED (failure
// naming why), or CANCELLED when a cancel was requested. Guarded by the epoch and RUNNING but
// NOT by cancel_requested_at, since observing the cancel is what calls it.
func FinishBackfill(db *gorm.DB, token string, epoch int64, status string, failure string) error {
	set := map[string]any{
		"status":      status,
		"finished_at": gorm.Expr("now()"),
		"updated_at":  gorm.Expr("now()"),
	}
	if failure != "" {
		set["failure"] = failure
	}
	q := db.Model(&CalculatedBackfill{}).
		Where("token = ? AND status = ? AND claim_epoch = ?", token, BackfillRunning, epoch)
	if status == BackfillCancelled {
		q = q.Where("cancel_requested_at IS NOT NULL")
	}
	res := q.Updates(set)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrBackfillLost
	}
	return nil
}

// CancelBackfill cancels a job: a QUEUED one straight to CANCELLED, a RUNNING one by recording
// the request, which the worker observes at its next commit and finishes. The job as it now
// stands is returned. A terminal job is ErrBackfillLost.
func CancelBackfill(db *gorm.DB, token string) (*CalculatedBackfill, error) {
	var job CalculatedBackfill
	res := db.Model(&job).Clauses(clause.Returning{}).
		Where("token = ? AND status = ?", token, BackfillQueued).
		Updates(map[string]any{
			"status":              BackfillCancelled,
			"cancel_requested_at": gorm.Expr("now()"),
			"finished_at":         gorm.Expr("now()"),
			"updated_at":          gorm.Expr("now()"),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 {
		return &job, nil
	}
	job = CalculatedBackfill{}
	res = db.Model(&job).Clauses(clause.Returning{}).
		Where("token = ? AND status = ?", token, BackfillRunning).
		Updates(map[string]any{
			"cancel_requested_at": gorm.Expr("coalesce(cancel_requested_at, now())"),
			"updated_at":          gorm.Expr("now()"),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrBackfillLost
	}
	return &job, nil
}

// CreateBackfill inserts a QUEUED job. A tenant's second active job violates the partial
// unique index and is reported as *ErrBackfillActive naming the one that holds the slot.
func CreateBackfill(ctx context.Context, db *gorm.DB, job *CalculatedBackfill) error {
	job.Status = BackfillQueued
	job.Phase = BackfillPhaseFill
	if len(job.Warnings) == 0 {
		job.Warnings = json.RawMessage(`[]`)
	}
	if len(job.Skipped) == 0 {
		job.Skipped = json.RawMessage(`{}`)
	}
	err := db.WithContext(ctx).Create(job).Error
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uix_calculated_backfills_tenant_active" {
		var active CalculatedBackfill
		if qerr := db.WithContext(ctx).Where("status IN ?", []string{BackfillQueued, BackfillRunning}).
			Take(&active).Error; qerr == nil {
			return &ErrBackfillActive{ActiveToken: active.Token}
		}
		return &ErrBackfillActive{}
	}
	return err
}

// RunnableBackfills lists the active jobs of every tenant, least recently touched first. It
// is the one cross-tenant read of the job table: the caller passes a system context.
func RunnableBackfills(db *gorm.DB, limit int) ([]CalculatedBackfill, error) {
	var jobs []CalculatedBackfill
	err := db.Where("status IN ?", []string{BackfillQueued, BackfillRunning}).
		Order("updated_at ASC, id ASC").Limit(limit).Find(&jobs).Error
	return jobs, err
}

// OldestActiveBackfillAge is how long the longest-active job has existed, zero when none is
// active. It feeds the operator's stuck-job alert. The caller passes a system context.
func OldestActiveBackfillAge(db *gorm.DB, now time.Time) (time.Duration, error) {
	var oldest []time.Time
	err := db.Model(&CalculatedBackfill{}).Where("status IN ?", []string{BackfillQueued, BackfillRunning}).
		Order("created_at ASC").Limit(1).Pluck("created_at", &oldest).Error
	if err != nil || len(oldest) == 0 {
		return 0, err
	}
	return now.Sub(oldest[0]), nil
}

// InsertDerivedMeasurementRows inserts derived measurement rows ON CONFLICT DO NOTHING on tx,
// under the tenant in its context, and returns how many were inserted: those the row identity
// did not already hold. It writes NO parent event (unlike the persist path): a backfilled row
// attaches to an event that already exists, which is also why a caller cannot reach the event
// anchors, the state tables or the stream through it.
func InsertDerivedMeasurementRows(tx *gorm.DB, rows []*MeasurementEvent) (int64, error) {
	return measurementColumns.Insert(tx, rows)
}
