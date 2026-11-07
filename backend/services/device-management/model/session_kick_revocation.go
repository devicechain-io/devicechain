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

// PendingRevocationKick records that a certificate revocation that has committed owes a kick to a
// device whose live broker sessions have not yet been confirmed ended. It has the shape of
// PendingSessionKick and the same lifecycle, plus the re-sweep: the row is not deleted on the
// first confirmed sweep but rescheduled (NotBefore), and deleted when the second one is confirmed.
//
// 🔴 IT IS ITS OWN TABLE, AND THAT IS THE POINT. A rolling upgrade runs v1.1 device-management
// pods beside new ones, and a v1.1 pod's background drain reads EVERY row of pending_session_kicks
// for a tenant, treats an origin it does not know as a provisioning profile id, ignores any
// not_before, kicks the device under the reason "credential-changed" and DELETES the row once
// that is confirmed. Revocation rows kept in that table would be deleted by it after one sweep,
// and the delayed re-sweep (the only thing that ends a session admitted in the race with the
// revocation) would be lost. No column, key or origin value can hide a row from a query that
// selects the whole table for the tenant, so the rows live where that query never looks. The v1.1
// pods keep draining the old table exactly as before; only new pods drain this one. The cost is
// one more table, an appended migration, and a second small drain. The Origin column here is the
// trust anchor's id (rows of one anchor are one debt), not a profile id.
type PendingRevocationKick struct {
	TenantId    string    `gorm:"primaryKey;not null;size:128"`
	DeviceToken string    `gorm:"primaryKey;not null;size:255"`
	Origin      uint      `gorm:"primaryKey;not null;autoIncrement:false"`
	Generation  int64     `gorm:"not null"`
	RequestedAt time.Time `gorm:"not null"`
	// NotBefore is the instant the RE-SWEEP may be drained: NULL while the first sweep is still owed.
	NotBefore *time.Time
}

// TableName is NOT declared: it would bypass the area's TablePrefix; the type name derives
// pending_revocation_kicks.

// AuditExempt: bookkeeping derived from a revocation the journal already records.
func (PendingRevocationKick) AuditExempt() bool { return true }

// revocationKickOrigin is the Origin of the rows owed because certificates under trust anchor id
// were revoked: the anchor's id.
func revocationKickOrigin(anchorId uint) uint { return anchorId }

// queueRevocationKickRowsOnTx records, in tx (the revocation's transaction, the anchor locked),
// that the given devices owe a revocation kick for the anchor. Tokens are upserted in sorted order
// (two writers queueing overlapping sets take the rows in the same order). A re-queue refreshes the
// generation and the request time AND resets not_before: a debt queued again is a NEW withdrawal
// whose first sweep is due now, even when the row it lands on had moved on to its re-sweep.
func (api *Api) queueRevocationKickRowsOnTx(tx *gorm.DB, tenant string, anchorId uint, deviceTokens []string) error {
	if api.SessionKicker == nil {
		return nil
	}
	tokens := sortedUniqueTokens(deviceTokens)
	if len(tokens) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]PendingRevocationKick, len(tokens))
	for i, t := range tokens {
		rows[i] = PendingRevocationKick{TenantId: tenant, DeviceToken: t, Origin: revocationKickOrigin(anchorId),
			Generation: rand.Int64(), RequestedAt: now}
	}
	err := rdb.CreateChunked(tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "device_token"}, {Name: "origin"}},
		DoUpdates: clause.AssignmentColumns([]string{"generation", "requested_at", "not_before"}),
	}), &rows).Error
	if err != nil {
		return fmt.Errorf("recording the certificate-revocation kicks of %d device(s): %w", len(tokens), err)
	}
	return nil
}

// drainRevocationKicks asks the broker to end the sessions of the devices that owe a revocation kick
// that is DUE (a first sweep, or a re-sweep whose time has come), for one anchor (anchorId > 0) or
// all of the tenant's (0), at most limit rows when limit > 0. A confirmed first sweep reschedules
// the row; a confirmed re-sweep deletes it; both are conditional on the Generation read, so a row
// queued again meanwhile keeps a due first sweep. An unconfirmed kick leaves its rows and is
// ErrSessionsNotEnded. The same failure and time budgets as the profile queue apply.
func (api *Api) drainRevocationKicks(ctx context.Context, tenant string, limit int, anchorId uint) error {
	if api.SessionKicker == nil {
		return nil
	}
	db := api.RDB.DB(core.WithTenant(ctx, tenant))
	q := db.Where("tenant_id = ? AND (not_before IS NULL OR not_before <= ?)", tenant, time.Now().UTC())
	if anchorId > 0 {
		q = q.Where("origin = ?", revocationKickOrigin(anchorId))
	}
	q = q.Order("requested_at ASC, device_token ASC, origin ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []PendingRevocationKick
	if err := q.Find(&rows).Error; err != nil {
		return fmt.Errorf("%w: could not read the pending certificate-revocation kicks: %v", ErrSessionsNotEnded, err)
	}
	// Per device: its rows (one per anchor it owes). One kick answers all of them.
	var tokens []string
	rowsOf := map[string][]PendingRevocationKick{}
	for _, r := range rows {
		if _, ok := rowsOf[r.DeviceToken]; !ok {
			tokens = append(tokens, r.DeviceToken)
		}
		rowsOf[r.DeviceToken] = append(rowsOf[r.DeviceToken], r)
	}
	var errs []error
	failures := 0
	started := time.Now()
	for chunk := range slices.Chunk(tokens, messaging.MaxSessionKickTokens) {
		if time.Since(started) > sessionKickDrainBudget {
			errs = append(errs, fmt.Errorf("%w: the time allowed for one pass (%s) ran out; the rest stays pending",
				ErrSessionsNotEnded, sessionKickDrainBudget))
			return errors.Join(errs...)
		}
		if failures >= maxConsecutiveKickFailures {
			break
		}
		if kerr := api.endDeviceSessions(ctx, tenant, messaging.SessionKickCertificateRevoked, chunk...); kerr != nil {
			errs = append(errs, kerr)
			failures++
			continue
		}
		failures = 0
		var done []PendingRevocationKick
		for _, t := range chunk {
			done = append(done, rowsOf[t]...)
		}
		if derr := api.settleConfirmedRevocationKicks(db, tenant, done); derr != nil {
			errs = append(errs, fmt.Errorf("%w: confirmed, but could not clear the pending kick: %v", ErrSessionsNotEnded, derr))
		}
	}
	if failures >= maxConsecutiveKickFailures {
		errs = append(errs, fmt.Errorf("%w: giving up on this pass after %d failed requests in a row; the rest stays pending",
			ErrSessionsNotEnded, failures))
	}
	return errors.Join(errs...)
}

// settleConfirmedRevocationKicks settles rows whose kick was just confirmed. A row on its FIRST sweep
// is rescheduled for its RE-SWEEP, because an admission that read "not revoked" just before the
// revocation committed can still be granted after the first sweep found the broker empty; a row
// already on its re-sweep is deleted.
func (api *Api) settleConfirmedRevocationKicks(db *gorm.DB, tenant string, done []PendingRevocationKick) error {
	var first, final []PendingRevocationKick
	for _, r := range done {
		if r.NotBefore == nil {
			first = append(first, r)
		} else {
			final = append(final, r)
		}
	}
	keysOf := func(rows []PendingRevocationKick) [][]any {
		keys := make([][]any, len(rows))
		for i, r := range rows {
			keys[i] = []any{r.DeviceToken, r.Origin, r.Generation}
		}
		return keys
	}
	if len(first) > 0 {
		due := time.Now().UTC().Add(api.revocationResweepAfter())
		for chunk := range slices.Chunk(first, rdb.MaxLookupKeys) {
			if err := db.Model(&PendingRevocationKick{}).
				Where("tenant_id = ? AND (device_token, origin, generation) IN ?", tenant, keysOf(chunk)).
				Update("not_before", due).Error; err != nil {
				return err
			}
		}
	}
	for chunk := range slices.Chunk(final, rdb.MaxLookupKeys) {
		if err := db.Where("tenant_id = ? AND (device_token, origin, generation) IN ?", tenant, keysOf(chunk)).
			Delete(&PendingRevocationKick{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// dueRevocationKickRemains is whether a revocation kick of the anchor that was due at `since` is
// still owed: a first sweep not reached, or a re-sweep that had come due and was not.
func (api *Api) dueRevocationKickRemains(ctx context.Context, tenant string, anchorId uint, since time.Time) (bool, error) {
	var left []PendingRevocationKick
	err := api.RDB.DB(core.WithTenant(ctx, tenant)).
		Where("tenant_id = ? AND origin = ? AND (not_before IS NULL OR not_before <= ?)", tenant, revocationKickOrigin(anchorId), since).
		Limit(1).Find(&left).Error
	return len(left) > 0, err
}

// hasDueRevocationKicks reports whether the tenant still holds a revocation kick that is due.
func (api *Api) hasDueRevocationKicks(ctx context.Context, tenant string) (bool, error) {
	var one []PendingRevocationKick
	err := api.RDB.DB(core.WithTenant(ctx, tenant)).
		Where("tenant_id = ? AND (not_before IS NULL OR not_before <= ?)", tenant, time.Now().UTC()).
		Limit(1).Find(&one).Error
	return len(one) > 0, err
}

// tenantsOwingRevocationKicks lists the tenants that hold a pending revocation kick, ordered. Like
// tenantsOwingKicks it is a cross-tenant read under a system context that selects only tenant ids.
func (api *Api) tenantsOwingRevocationKicks(ctx context.Context) ([]string, error) {
	tenants := make([]string, 0)
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingRevocationKick{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
}

// mergeSortedTenants is the sorted union of two sorted tenant lists.
func mergeSortedTenants(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	slices.Sort(out)
	return slices.Compact(out)
}

// revocationQueueGauge is the count of the revocation queue and the age of its oldest row, for the
// same gauge and alert that cover the profile queue.
func (api *Api) revocationQueueGauge(db *gorm.DB) (n int, age time.Duration, err error) {
	var count int64
	if err = db.Model(&PendingRevocationKick{}).Count(&count).Error; err != nil {
		return 0, 0, err
	}
	if count > 0 {
		var oldest []PendingRevocationKick
		if err = db.Order("requested_at ASC").Limit(1).Find(&oldest).Error; err != nil {
			return 0, 0, err
		}
		if len(oldest) == 1 {
			age = max(time.Since(oldest[0].RequestedAt), 0)
		}
	}
	return int(count), age, nil
}
