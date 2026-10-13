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
	// ReportRejectedDeviceDisabled: an operator has disabled the device. A session it already had
	// open is not ended by that, so its syncs and reports are refused here, per message, until it
	// is enabled again.
	ReportRejectedDeviceDisabled = "device_disabled"
)

// ConfigurationReportRefusal is a device report that was understood and refused. It is not a
// failure of the store: redelivering the same report can never succeed.
type ConfigurationReportRefusal struct {
	Reason string
}

func (e *ConfigurationReportRefusal) Error() string {
	return fmt.Sprintf("configuration report refused: %s", e.Reason)
}

// SyncCooldown is the shortest interval between two syncs from one device that the platform acts
// on. A sync costs a transaction and a publish of up to the whole document, and a healthy device
// syncs on connect and every few hours, so a second sync within seconds is a reconnect loop or a
// fault. Ten seconds is long enough to absorb a flapping connection and short enough that a device
// that reconnects after a real change is not made to wait. A throttled sync is acknowledged and
// does nothing; the next one outside the window is served in full.
const SyncCooldown = 10 * time.Second

// ErrSyncThrottled is returned for a sync inside the device's SyncCooldown.
var ErrSyncThrottled = errors.New("configuration sync throttled")

// syncClock is the clock the cooldown reads; tests replace it.
var syncClock = func() time.Time { return time.Now().UTC() }

// SystemActorDeviceSync is the actor recorded on a revision minted because a device synced.
const SystemActorDeviceSync = "system:device-sync"

// publishMintedConfiguration delivers a freshly minted revision after its transaction has
// committed. A nil revision (nothing minted) or a missing publisher is a no-op.
//
// A DISABLED device is sent nothing: the revision is minted and stored, but its desired channel is
// held shut like every other inbound and outbound path of a quarantined device. Nothing is lost by
// that, because enabling the device publishes its latest revision (publishLatestConfiguration),
// which supersedes any minted meanwhile.
func (api *Api) publishMintedConfiguration(ctx context.Context, deviceId uint, rev *DeviceConfigurationRevision) {
	if rev == nil || api.DeviceDesiredPublisher == nil {
		return
	}
	var rows []Device
	if err := api.RDB.DB(ctx).Select("id", "token", "disabled_at").Where("id = ?", deviceId).Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		log.Warn().Err(err).Uint("device", deviceId).
			Msg("Could not resolve a device to deliver its new configuration revision; it picks the revision up at its next sync.")
		return
	}
	if rows[0].IsDisabled() {
		return
	}
	api.DeviceDesiredPublisher.PublishDesiredConfiguration(ctx, rows[0].Token, rev)
}

// publishLatestConfiguration delivers a device's latest stored configuration revision on its
// desired channel, the way a newly minted one is. It is the answer to an ENABLE: a device that
// tried to sync while disabled was refused in silence and cannot tell that from "nothing new",
// and a revision minted while it was disabled was held back, so without this it would stay on a
// stale revision until its next boot or sync. It mints nothing (the document a profile publish
// moved is still reconciled by the device's own sync) and sends nothing for a device that has no
// revision. Best-effort like every desired publish: the device's next sync repairs a miss.
func (api *Api) publishLatestConfiguration(ctx context.Context, device *Device) {
	if api.DeviceDesiredPublisher == nil || device.IsDisabled() {
		return
	}
	latest, err := latestConfigurationRevision(api.RDB.DB(ctx), device.ID)
	if err != nil {
		log.Warn().Err(err).Str("device", device.Token).
			Msg("Could not read a re-enabled device's latest configuration revision; it picks it up at its next sync.")
		return
	}
	if latest == nil {
		return
	}
	api.DeviceDesiredPublisher.PublishDesiredConfiguration(ctx, device.Token, latest)
}

// reportingDeviceId resolves the device a sync or a report names, and refuses it when an operator
// has disabled it (a *ConfigurationReportRefusal, ReportRejectedDeviceDisabled). It reads the
// column on every call: a disabled device keeps whatever session it had open, so the check is
// per message, never per connect, and enabling the device takes effect on its next message. The
// refusal comes before the cooldown, the lock and any write, so a refused message leaves no trace.
func (api *Api) reportingDeviceId(ctx context.Context, token string) (uint, error) {
	var rows []Device
	if err := api.RDB.DB(ctx).Select("id", "disabled_at").Where("token = ?", token).Limit(1).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, gorm.ErrRecordNotFound
	}
	if rows[0].IsDisabled() {
		return 0, &ConfigurationReportRefusal{Reason: ReportRejectedDeviceDisabled}
	}
	return rows[0].ID, nil
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
	deviceId, err := api.reportingDeviceId(ctx, deviceToken)
	if err != nil {
		return nil, err
	}
	var seen []DeviceConfigurationState
	if err := api.RDB.DB(ctx).Select("id", "last_sync_at").Where("device_id = ?", deviceId).Limit(1).Find(&seen).Error; err != nil {
		return nil, err
	}
	if len(seen) == 1 && seen[0].LastSyncAt.Valid && syncClock().Sub(seen[0].LastSyncAt.Time) < SyncCooldown {
		return nil, ErrSyncThrottled
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
		return touchConfigurationSync(tx, deviceId, syncClock())
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
