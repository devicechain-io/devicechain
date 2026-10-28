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
	"github.com/rs/zerolog/log"
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
// 🔴 THE KEY NAMES WHO OWES THE KICK. One row per (device, Origin): a revoke of profile A and
// a reset of the same device are two debts, and each operation's repeat must find its own.
// Keyed by device alone, the second write replaced the first's owner, and a repeat of the first
// operation then found nothing of its own and reported success while the session was live.
//
// Generation identifies the write that queued the row last, so a kick that was confirmed for an
// earlier write never deletes the row a later write re-queued meanwhile.
type PendingSessionKick struct {
	TenantId    string `gorm:"primaryKey;not null;size:128"`
	DeviceToken string `gorm:"primaryKey;not null;size:255"`
	// Origin is the operation that owes the kick: the id of the provisioning profile whose
	// revoke queued it, or kickOriginReset for a device reset. It is not a foreign key; a
	// profile deleted after a failed revoke leaves a row the background drain still clears.
	Origin      uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation  int64     `gorm:"not null"`
	RequestedAt time.Time `gorm:"not null"`
}

// kickOriginReset is the Origin of a device reset. Profile ids start at 1, so it is no profile.
const kickOriginReset uint = 0

// TableName is NOT declared: it would bypass the area's TablePrefix; the type name derives
// pending_session_kicks.

// AuditExempt opts the queue out of the audit journal: a row is bookkeeping derived from a
// credential change the journal already records, and lives only until the kick is confirmed.
func (PendingSessionKick) AuditExempt() bool { return true }

// reason is the messaging.SessionKick* reason the kick carries to the responder.
func (r PendingSessionKick) reason() string {
	if r.Origin == kickOriginReset {
		return messaging.SessionKickCredentialDeleted
	}
	return messaging.SessionKickCredentialChanged
}

// SessionKickDrainPageSize is how many pending rows of one tenant one background pass handles.
const SessionKickDrainPageSize = 500

// queueSessionKicksOnTx records, in tx (the transaction of the credential change), that the
// given devices of tenant owe a kick to origin. With no kicker wired there are no sessions of
// ours to end and nothing is recorded. Tokens are upserted in sorted order so two writers
// queueing overlapping sets take the rows in the same order. A re-queue refreshes the
// generation and the request time, since it records a newer withdrawal.
func (api *Api) queueSessionKicksOnTx(tx *gorm.DB, tenant string, origin uint, deviceTokens []string) error {
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
		rows[i] = PendingSessionKick{TenantId: tenant, DeviceToken: t, Origin: origin, Generation: rand.Int64(), RequestedAt: now}
	}
	err := rdb.CreateChunked(tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "origin"}},
		DoUpdates: clause.AssignmentColumns([]string{"generation", "requested_at"}),
	}), &rows).Error
	if err != nil {
		return fmt.Errorf("recording the session kicks of %d device(s): %w", len(tokens), err)
	}
	return nil
}

// drainSessionKicks asks the broker to end the sessions of the pending devices of tenant that
// scope selects (at most limit rows when limit > 0), and deletes the rows whose kick was
// confirmed. A kick that could not be confirmed leaves its rows and is returned as
// ErrSessionsNotEnded, so a repeat of the operation, or the next background pass, asks again.
// It is the single place success is decided: nil means every selected device's sessions were
// confirmed ended.
//
// A device owing under several selected origins is kicked once, under the strongest reason (a
// deleted credential outranks a changed one), and every one of its selected rows is cleared by
// that one confirmation: ending the device's sessions satisfies every debt about them.
func (api *Api) drainSessionKicks(ctx context.Context, tenant string, limit int, scope func(*gorm.DB) *gorm.DB) error {
	if api.SessionKicker == nil {
		return nil
	}
	db := api.RDB.DB(core.WithTenant(ctx, tenant))
	q := scope(db.Where("tenant_id = ?", tenant)).Order("requested_at ASC, device_token ASC, origin ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []PendingSessionKick
	if err := q.Find(&rows).Error; err != nil {
		return fmt.Errorf("%w: could not read the pending session kicks: %v", ErrSessionsNotEnded, err)
	}
	// Per device: its rows, and the strongest reason among them. tokens keeps first-seen order.
	var tokens []string
	reasonOf := map[string]string{}
	rowsOf := map[string][]PendingSessionKick{}
	for _, r := range rows {
		if _, ok := reasonOf[r.DeviceToken]; !ok {
			tokens = append(tokens, r.DeviceToken)
		}
		if r.reason() == messaging.SessionKickCredentialDeleted || reasonOf[r.DeviceToken] == "" {
			reasonOf[r.DeviceToken] = r.reason()
		}
		rowsOf[r.DeviceToken] = append(rowsOf[r.DeviceToken], r)
	}
	byReason := map[string][]string{}
	for _, t := range tokens {
		byReason[reasonOf[t]] = append(byReason[reasonOf[t]], t)
	}
	var errs []error
	for reason, group := range byReason {
		for chunk := range slices.Chunk(group, messaging.MaxSessionKickTokens) {
			if kerr := api.endDeviceSessions(ctx, tenant, reason, chunk...); kerr != nil {
				errs = append(errs, kerr)
				continue
			}
			var done []PendingSessionKick
			for _, t := range chunk {
				done = append(done, rowsOf[t]...)
			}
			if derr := deleteConfirmedKicks(db, tenant, done); derr != nil {
				errs = append(errs, fmt.Errorf("%w: confirmed, but could not clear the pending kick: %v", ErrSessionsNotEnded, derr))
			}
		}
	}
	return errors.Join(errs...)
}

// deleteConfirmedKicks deletes the given rows where each still holds the Generation that was
// read, so a row re-queued while its kick was in flight survives for the next pass.
func deleteConfirmedKicks(db *gorm.DB, tenant string, rows []PendingSessionKick) error {
	for chunk := range slices.Chunk(rows, rdb.MaxLookupKeys) {
		keys := make([][]any, len(chunk))
		for i, r := range chunk {
			keys[i] = []any{r.DeviceToken, r.Origin, r.Generation}
		}
		if err := db.Where("tenant_id = ? AND (device_token, origin, generation) IN ?", tenant, keys).
			Delete(&PendingSessionKick{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// SessionKickDrainResult counts what one background pass did.
type SessionKickDrainResult struct {
	Tenants int
	Failed  int
}

// DrainSessionKicks is one background pass: for every tenant with a pending kick, retry up to a
// page of its oldest rows. It is the retry that does not depend on an operator repeating the
// exact operation, and the only one that clears a row whose profile was deleted or whose device
// nobody revisits.
func (api *Api) DrainSessionKicks(ctx context.Context, pageSize int) (SessionKickDrainResult, error) {
	var res SessionKickDrainResult
	if api.SessionKicker == nil {
		return res, nil
	}
	tenants := make([]string, 0)
	// Cross-tenant by necessity: which tenants owe a kick. Only the tenant ids are read; every
	// row is then read and deleted under that tenant (drainSessionKicks).
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingSessionKick{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, fmt.Errorf("listing tenants with pending session kicks: %w", err)
	}
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		if err := api.drainSessionKicks(ctx, tenant, pageSize, func(db *gorm.DB) *gorm.DB { return db }); err != nil {
			res.Failed++
			lastErr = err
		}
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

// observeSessionKicks tells the observer how much is owed and how long the oldest has waited.
// It runs on EVERY replica every pass, before the lock is tried: a gauge set only by the replica
// that holds the lock would freeze at its last value on a replica that then lost it, and an
// alert on the age would fire for ever over a queue that is empty.
func (api *Api) observeSessionKicks(ctx context.Context) {
	if api.SessionKickObserver == nil {
		return
	}
	// Cross-tenant by necessity: one gauge for the whole instance. Reads a count and the oldest
	// request time, nothing a tenant could be told.
	db := api.RDB.DB(core.WithSystemContext(ctx))
	var n int64
	if err := db.Model(&PendingSessionKick{}).Count(&n).Error; err != nil {
		log.Warn().Err(err).Msg("Unable to count the pending session kicks")
		return
	}
	var age time.Duration
	if n > 0 {
		var oldest []PendingSessionKick
		if err := db.Order("requested_at ASC").Limit(1).Find(&oldest).Error; err != nil {
			log.Warn().Err(err).Msg("Unable to read the oldest pending session kick")
			return
		}
		if len(oldest) == 1 {
			age = max(time.Since(oldest[0].RequestedAt), 0)
		}
	}
	api.SessionKickObserver(int(n), age)
}

// sessionKickDrainLockName names the advisory lock one background pass runs under.
const sessionKickDrainLockName = "device-management-session-kick-drain"

// DrainSessionKicksLocked is one pass as the service runs it: under an advisory lock, so one
// replica retries at a time and the others skip (core.ErrPassSkipped). Two replicas retrying at
// once would be harmless (a kick is idempotent) but would ask the broker twice.
func (api *Api) DrainSessionKicksLocked(ctx context.Context) error {
	api.observeSessionKicks(ctx)
	var res SessionKickDrainResult
	ran, err := api.RDB.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(sessionKickDrainLockName), func() error {
		var err error
		res, err = api.DrainSessionKicks(ctx, SessionKickDrainPageSize)
		return err
	})
	if !ran {
		if err != nil {
			return fmt.Errorf("acquiring the session-kick-drain lock: %w", err)
		}
		return core.ErrPassSkipped
	}
	if res.Tenants > 0 && err == nil {
		log.Debug().Int("tenants", res.Tenants).Msg("Retried pending session kicks")
	}
	return err
}
