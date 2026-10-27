// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PendingSessionKick records that a device's authority was withdrawn by a write that has
// committed, and that its live broker sessions have not yet been confirmed ended.
//
// 🔑 WHY IT IS A ROW AND NOT A RETURN VALUE: a revoke or a reset commits first and kicks
// second. The kick can fail (no responder, a broker restart), and the caller is then told to
// repeat the operation. The repeat finds nothing left to change (the credentials are already
// disabled, or already deleted), so without a record of the unfinished kick it would report
// success and never ask the broker again, leaving an open session publishing until its JWT
// expires. The row is written in the SAME transaction as the credential change, so a change
// that committed always has its kick on record; it is deleted only once a kick was confirmed.
//
// One row per device. Generation identifies the write that queued it last, so a kick that was
// confirmed for an earlier write never deletes the row a later write re-queued meanwhile.
type PendingSessionKick struct {
	TenantId    string `gorm:"primaryKey;not null;size:128"`
	DeviceToken string `gorm:"primaryKey;not null;size:255"`
	// Reason is the messaging.SessionKick* reason the kick carries to the responder.
	Reason string `gorm:"not null;size:64"`
	// ProvisioningProfileId is the profile whose revoke queued the row, so repeating that
	// revoke retries it. Nil for a reset, which is retried by device.
	ProvisioningProfileId *uint
	Generation            int64     `gorm:"not null"`
	RequestedAt           time.Time `gorm:"not null"`
}

// TableName is NOT declared: it would bypass the area's TablePrefix; the type name derives
// pending_session_kicks.

// AuditExempt opts the queue out of the audit journal: a row is bookkeeping derived from a
// credential change the journal already records, and lives only until the kick is confirmed.
func (PendingSessionKick) AuditExempt() bool { return true }

// queueSessionKicksOnTx records, in tx (the transaction of the credential change), that the
// given devices of tenant need their sessions ended. With no kicker wired there are no
// sessions of ours to end and nothing is recorded. Tokens are upserted in sorted order so two
// writers queueing overlapping sets take the rows in the same order.
func (api *Api) queueSessionKicksOnTx(tx *gorm.DB, tenant, reason string, profileId *uint, deviceTokens []string) error {
	if api.SessionKicker == nil {
		return nil
	}
	tokens := make([]string, 0, len(deviceTokens))
	for _, t := range deviceTokens {
		if t != "" && !slices.Contains(tokens, t) {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	slices.Sort(tokens)
	now := time.Now().UTC()
	rows := make([]PendingSessionKick, len(tokens))
	for i, t := range tokens {
		rows[i] = PendingSessionKick{TenantId: tenant, DeviceToken: t, Reason: reason,
			ProvisioningProfileId: profileId, Generation: rand.Int64(), RequestedAt: now}
	}
	err := rdb.CreateChunked(tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "provisioning_profile_id", "generation"}),
	}), &rows).Error
	if err != nil {
		return fmt.Errorf("recording the session kicks of %d device(s): %w", len(tokens), err)
	}
	return nil
}

// drainSessionKicks asks the broker to end the sessions of every pending device of tenant that
// scope selects, and deletes the rows whose kick was confirmed. A kick that could not be
// confirmed leaves its rows and is returned as ErrSessionsNotEnded, so a repeat of the
// operation asks again. It is the single place success is decided: nil means every selected
// device's sessions were confirmed ended.
func (api *Api) drainSessionKicks(ctx context.Context, tenant string, scope func(*gorm.DB) *gorm.DB) error {
	if api.SessionKicker == nil {
		return nil
	}
	db := api.RDB.DB(core.WithTenant(ctx, tenant))
	var rows []PendingSessionKick
	if err := scope(db.Where("tenant_id = ?", tenant)).
		Order("requested_at ASC, device_token ASC").Find(&rows).Error; err != nil {
		return fmt.Errorf("%w: could not read the pending session kicks: %v", ErrSessionsNotEnded, err)
	}
	byReason := map[string][]PendingSessionKick{}
	for _, r := range rows {
		byReason[r.Reason] = append(byReason[r.Reason], r)
	}
	var errs []error
	for reason, group := range byReason {
		for chunk := range slices.Chunk(group, messaging.MaxSessionKickTokens) {
			tokens := make([]string, len(chunk))
			keys := make([][]any, len(chunk))
			for i, r := range chunk {
				tokens[i] = r.DeviceToken
				keys[i] = []any{r.DeviceToken, r.Generation}
			}
			if kerr := api.endDeviceSessions(ctx, tenant, reason, tokens...); kerr != nil {
				errs = append(errs, kerr)
				continue
			}
			if derr := db.Where("tenant_id = ? AND (device_token, generation) IN ?", tenant, keys).
				Delete(&PendingSessionKick{}).Error; derr != nil {
				errs = append(errs, fmt.Errorf("%w: confirmed, but could not clear the pending kick: %v", ErrSessionsNotEnded, derr))
			}
		}
	}
	return errors.Join(errs...)
}
