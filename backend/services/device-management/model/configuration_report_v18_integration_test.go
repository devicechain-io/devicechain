// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
)

// Frozen v1.8.0 writer. Keep its body identical to testdata/configuration_report_v18.go.txt.
func (api *Api) applyV18ConfigurationReport(ctx context.Context, deviceToken string, revision int64,
	digest string, status ConfigurationStatus, errs []string) error {
	if !slices.Contains([]ConfigurationStatus{ConfigurationReceived, ConfigurationApplied, ConfigurationRejected}, status) {
		return fmt.Errorf("unknown configuration status %q", status)
	}
	deviceId, err := api.reportingDeviceId(ctx, deviceToken)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &ConfigurationReportRefusal{Reason: ReportRejectedUnknownDevice}
	}
	if err != nil {
		return err
	}
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceConfiguration(ctx, tx, deviceId); err != nil {
			return err
		}
		var revs []DeviceConfigurationRevision
		if err := tx.Where("device_id = ? AND revision = ?", deviceId, revision).Limit(1).Find(&revs).Error; err != nil {
			return err
		}
		if len(revs) == 0 {
			return &ConfigurationReportRefusal{Reason: ReportRejectedUnknownRevision}
		}
		if revs[0].Digest != digest {
			return &ConfigurationReportRefusal{Reason: ReportRejectedDigestMismatch}
		}
		var states []DeviceConfigurationState
		if err := tx.Where("device_id = ?", deviceId).Limit(1).Find(&states).Error; err != nil {
			return err
		}
		encoded, err := encodeReportedErrors(errs)
		if err != nil {
			return err
		}
		if len(states) == 1 {
			cur := &states[0]
			if cur.ReportedRevision.Valid {
				switch {
				case revision < cur.ReportedRevision.Int64:
					return &ConfigurationReportRefusal{Reason: ReportRejectedStale}
				case revision == cur.ReportedRevision.Int64:
					if sameReport(cur, digest, status, encoded) {
						return nil
					}
					if status == ConfigurationReceived && cur.ReportedStatus.String != string(ConfigurationReceived) {
						return &ConfigurationReportRefusal{Reason: ReportRejectedStale}
					}
				}
			}
			return tx.Model(cur).Updates(map[string]any{
				"reported_revision": sql.NullInt64{Int64: revision, Valid: true},
				"reported_digest":   sql.NullString{String: digest, Valid: true},
				"reported_status":   sql.NullString{String: string(status), Valid: true},
				"reported_errors":   encoded,
				"reported_at":       sql.NullTime{Time: time.Now().UTC(), Valid: true},
			}).Error
		}
		return tx.Create(&DeviceConfigurationState{
			DeviceId:         deviceId,
			ReportedRevision: sql.NullInt64{Int64: revision, Valid: true},
			ReportedDigest:   sql.NullString{String: digest, Valid: true},
			ReportedStatus:   sql.NullString{String: string(status), Valid: true},
			ReportedErrors:   encoded,
			ReportedAt:       sql.NullTime{Time: time.Now().UTC(), Valid: true},
		}).Error
	})
}
