// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Parent before configuration: delete holds the attribute advisory lock, then
// the device FOR UPDATE, and never takes the configuration lock. Attribute
// writers hold advisory -> device KEY SHARE -> ... -> configuration. Report and
// sync never request the attribute advisory lock, so neither can close a cycle
// with those paths. Holding KEY SHARE through commit also serializes capture
// with a delete from any pod version. Existence is rechecked on every dialect.
func lockConfigurationParent(tx *gorm.DB, deviceID uint) error {
	read := tx.Select("id")
	if tx.Dialector.Name() == "postgres" {
		read = read.Clauses(clause.Locking{Strength: "KEY SHARE"})
	}
	var device Device
	return read.First(&device, deviceID).Error
}

// sameCapturedPair reports whether the last writer of the state row was a capturing one. The
// pair alone is not enough: a non-capturing (previous-release) pod can accept REJECTED r7 and
// then APPLIED r7 after a capture of APPLIED r7, restoring the very pair while hiding a verdict.
// It always rewrites reported_at, so the reported_at a capture left is compared too.
func sameCapturedPair(cur *DeviceConfigurationState) bool {
	return cur.ReportedAt.Valid == cur.CapturedReportedAt.Valid &&
		(!cur.ReportedAt.Valid || cur.ReportedAt.Time.Equal(cur.CapturedReportedAt.Time)) &&
		cur.ReportedRevision.Valid == cur.CapturedRevision.Valid &&
		(!cur.ReportedRevision.Valid || cur.ReportedRevision.Int64 == cur.CapturedRevision.Int64) &&
		cur.ReportedStatus.Valid == cur.CapturedStatus.Valid &&
		(!cur.ReportedStatus.Valid || cur.ReportedStatus.String == cur.CapturedStatus.String)
}

func configurationVerdict(status string) bool {
	return status == string(ConfigurationApplied) || status == string(ConfigurationRejected)
}

// captureConfigurationReport runs only after acceptance, in the report's own
// transaction. A hidden intermediate verdict from an older pod cannot be
// reconstructed: history before CaptureCompleteSince is degraded. Backfill uses
// receive time now, never the older projection's ReportedAt.
// reportedAt is the reported_at the state row holds once this transaction commits: unchanged for
// an identical report, now for every other accepted one.
func captureConfigurationReport(tx *gorm.DB, cur *DeviceConfigurationState, revision int64, digest string, status ConfigurationStatus, errs datatypes.JSON, now time.Time, reportedAt sql.NullTime) error {
	if !sameCapturedPair(cur) {
		if cur.ReportedRevision.Valid && (!cur.CaptureGapThrough.Valid || cur.CaptureGapThrough.Int64 < cur.ReportedRevision.Int64) {
			cur.CaptureGapThrough = cur.ReportedRevision
		}
		cur.CaptureCompleteSince = sql.NullTime{Time: now, Valid: true}
		if cur.ReportedRevision.Valid && configurationVerdict(cur.ReportedStatus.String) {
			if err := insertConfigurationVerdict(tx, cur.DeviceId, cur.ReportedRevision.Int64, cur.ReportedDigest.String, cur.ReportedStatus.String, cur.ReportedErrors, "STATE_BACKFILL", now); err != nil {
				return err
			}
		}
	}
	if configurationVerdict(string(status)) {
		if err := insertConfigurationVerdict(tx, cur.DeviceId, revision, digest, string(status), errs, "REPORT", now); err != nil {
			return err
		}
	}
	cur.CapturedRevision = sql.NullInt64{Int64: revision, Valid: true}
	cur.CapturedStatus = sql.NullString{String: string(status), Valid: true}
	cur.CapturedReportedAt = reportedAt
	if !cur.CaptureCompleteSince.Valid {
		cur.CaptureCompleteSince = sql.NullTime{Time: now, Valid: true}
	}
	return nil
}

func insertConfigurationVerdict(tx *gorm.DB, deviceID uint, revision int64, digest, status string, errs datatypes.JSON, source string, now time.Time) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "revision"}, {Name: "status"}},
		DoNothing: true,
	}).Create(&DeviceConfigurationReport{
		DeviceId: deviceID, Revision: revision, Digest: digest, Status: status,
		Errors: errs, Source: source, CreatedAt: now,
	}).Error
}

func configurationCaptureFields(cur *DeviceConfigurationState) map[string]any {
	return map[string]any{
		"captured_revision":      cur.CapturedRevision,
		"captured_status":        cur.CapturedStatus,
		"capture_gap_through":    cur.CaptureGapThrough,
		"capture_complete_since": cur.CaptureCompleteSince,
		"captured_reported_at":   cur.CapturedReportedAt,
	}
}
