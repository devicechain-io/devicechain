// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// This file is the delivery loop around the per-device configuration revisions
// (api_configuration.go): the platform pushes a minted revision to the device, the device
// announces itself with a sync when it connects, and the device reports what it did with a
// revision. The wire is the device-desired / device-reports pair of per-device channels; this
// file is the storage half of it, and the processor package owns the transport.

// DeviceDesiredPublisher delivers a configuration revision to its device on the device-desired
// channel. It is best-effort and at-most-once: a revision that never reaches the device is
// repaired by the device's next sync, which re-delivers the current one. An implementation
// never returns an error because a failed delivery must never fail the write that minted the
// revision, which is the source of truth; it logs and counts instead. It must be safe for
// concurrent use.
type DeviceDesiredPublisher interface {
	PublishDesiredConfiguration(ctx context.Context, deviceToken string, rev *DeviceConfigurationRevision)
}

// Reasons a device's configuration report is refused. They are the label values of the
// rejected-reports counter, so they are a closed vocabulary.
const (
	// ReportRejectedUnknownDevice: the subject names no device in the tenant.
	ReportRejectedUnknownDevice = "unknown_device"
	// ReportRejectedUnknownRevision: the device has no such revision, including one numbered
	// beyond the latest minted.
	ReportRejectedUnknownRevision = "unknown_revision"
	// ReportRejectedDigestMismatch: the revision exists but the report names another digest.
	ReportRejectedDigestMismatch = "digest_mismatch"
	// ReportRejectedStale: the report is older than what is already recorded, or a RECEIVED
	// arriving after the same revision's outcome. Reports are at-least-once and may reorder, so
	// this is expected traffic rather than a fault, and it is refused only so that it is counted.
	ReportRejectedStale = "stale"
)

// ConfigurationReportRefusal is a device report that was understood and refused. It is not a
// failure of the store: redelivering the same report can never succeed.
type ConfigurationReportRefusal struct {
	Reason string
}

func (e *ConfigurationReportRefusal) Error() string {
	return fmt.Sprintf("configuration report refused: %s", e.Reason)
}

// SystemActorDeviceSync is the actor recorded on a revision minted because a device synced.
const SystemActorDeviceSync = "system:device-sync"

// publishMintedConfiguration delivers a freshly minted revision after its transaction has
// committed. A nil revision (nothing minted) or a missing publisher is a no-op.
func (api *Api) publishMintedConfiguration(ctx context.Context, deviceId uint, rev *DeviceConfigurationRevision) {
	if rev == nil || api.DeviceDesiredPublisher == nil {
		return
	}
	var rows []Device
	if err := api.RDB.DB(ctx).Select("id", "token").Where("id = ?", deviceId).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		log.Warn().Err(err).Uint("device", deviceId).
			Msg("Could not resolve a device to deliver its new configuration revision; it picks the revision up at its next sync.")
		return
	}
	api.DeviceDesiredPublisher.PublishDesiredConfiguration(ctx, rows[0].Token, rev)
}

// SyncDeviceConfiguration answers a device's sync. Under the device's configuration lock it
// mints a revision when the document the device's current declaration and SHARED attributes
// produce differs from the latest one (this is how a profile publish reaches a device, since
// that does not fan out), records that the device was seen, and then delivers the current
// revision. haveRevision is what the device says it holds; it is recorded nowhere and decides
// nothing, because the device converges by the (revision, digest) it reports, and a sync that
// is answered with the current revision every time is idempotent on the device.
//
// A device with nothing to deliver (no revision, and an empty document) is told nothing. The
// error is gorm.ErrRecordNotFound for a device the tenant does not have.
func (api *Api) SyncDeviceConfiguration(ctx context.Context, deviceToken string, haveRevision int64) (*DeviceConfigurationRevision, error) {
	deviceId, err := api.deviceIdByToken(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	var current *DeviceConfigurationRevision
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceConfiguration(ctx, tx, deviceId); err != nil {
			return err
		}
		latest, err := latestConfigurationRevision(tx, deviceId)
		if err != nil {
			return err
		}
		current = latest
		doc, versionId, err := computeDeviceConfiguration(tx, deviceId)
		if err != nil {
			return err
		}
		stale := (latest == nil && string(doc.Canonical) != "{}") || (latest != nil && latest.Digest != doc.Digest)
		switch {
		case !stale:
		case len(doc.Canonical) > MaxConfigurationDocumentBytes:
			// Never mint over the cap; the read API shows the device stale and the device keeps
			// the last revision it was given.
			log.Warn().Str("device", deviceToken).Int("bytes", len(doc.Canonical)).
				Msg("Not minting a configuration revision at sync: the document is over its size limit.")
		default:
			rev, _, err := mintConfigurationRevision(tx, deviceId, doc, versionId, SystemActorDeviceSync)
			if err != nil {
				return err
			}
			current = rev
		}
		return touchConfigurationSync(tx, deviceId, time.Now().UTC())
	})
	if err != nil {
		return nil, err
	}
	if current != nil && api.DeviceDesiredPublisher != nil {
		api.DeviceDesiredPublisher.PublishDesiredConfiguration(ctx, deviceToken, current)
	}
	return current, nil
}

// touchConfigurationSync records that the device synced, creating the state row when the device
// has never reported. It touches no reported field.
func touchConfigurationSync(tx *gorm.DB, deviceId uint, at time.Time) error {
	res := tx.Model(&DeviceConfigurationState{}).Where("device_id = ?", deviceId).
		Update("last_sync_at", sql.NullTime{Time: at, Valid: true})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	return tx.Create(&DeviceConfigurationState{
		DeviceId: deviceId, LastSyncAt: sql.NullTime{Time: at, Valid: true},
	}).Error
}

// ApplyDeviceConfigurationReport records a device's report on one revision. The device is
// named by the subject the report arrived on, never by the report. The report is accepted only
// when the device has that revision and the digest matches it; anything else is a
// *ConfigurationReportRefusal and the state row is left exactly as it was.
//
// Reports are at-least-once and may arrive out of order, so recording is monotonic: a report
// for an older revision than the recorded one is stale, and a RECEIVED never overwrites the
// APPLIED or REJECTED outcome of the same revision. An identical repeat is a successful no-op.
func (api *Api) ApplyDeviceConfigurationReport(ctx context.Context, deviceToken string, revision int64,
	digest string, status ConfigurationStatus, errs []string) error {
	if !slices.Contains([]ConfigurationStatus{ConfigurationReceived, ConfigurationApplied, ConfigurationRejected}, status) {
		return fmt.Errorf("unknown configuration status %q", status)
	}
	deviceId, err := api.deviceIdByToken(ctx, deviceToken)
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

// encodeReportedErrors stores the error list; nothing at all for an empty one.
func encodeReportedErrors(errs []string) (datatypes.JSON, error) {
	if len(errs) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(errs)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

func sameReport(cur *DeviceConfigurationState, digest string, status ConfigurationStatus, errs datatypes.JSON) bool {
	return cur.ReportedDigest.String == digest && cur.ReportedStatus.String == string(status) &&
		string(cur.ReportedErrors) == string(errs)
}
