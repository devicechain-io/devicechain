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
	if r.Origin == kickOriginDeviceDisabled {
		return messaging.SessionKickDeviceDisabled
	}
	if r.Origin == kickOriginDeviceDeleted {
		return messaging.SessionKickDeviceDeleted
	}
	return messaging.SessionKickCredentialChanged
}

// SessionKickDrainPageSize is how many pending rows of one tenant one background pass handles.
const SessionKickDrainPageSize = 500

// sortedUniqueTokens is the non-empty tokens of the input, sorted and deduplicated, in
// O(n log n): a fleet-sized kick (every device of a CA) must not pay for a membership scan per
// token. The input is not modified.
func sortedUniqueTokens(in []string) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t != "" {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// queueSessionKicksOnTx records, in tx (the transaction of the credential change), that the
// given devices of tenant owe a kick to origin. With no kicker wired there are no sessions of
// ours to end and nothing is recorded. Tokens are upserted in sorted order so two writers
// queueing overlapping sets take the rows in the same order. A re-queue refreshes the
// generation and the request time, since it records a newer withdrawal.
func (api *Api) queueSessionKicksOnTx(tx *gorm.DB, tenant string, origin uint, deviceTokens []string) error {
	if api.SessionKicker == nil {
		return nil
	}
	tokens := sortedUniqueTokens(deviceTokens)
	if len(tokens) == 0 {
		return nil
	}
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
	var err error
	if rows, err = api.dropWithdrawnDeviceDisabledKicks(db, tenant, rows); err != nil {
		return fmt.Errorf("%w: could not re-check the disabled devices: %v", ErrSessionsNotEnded, err)
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
	failures := 0
	started := time.Now()
	for reason, group := range byReason {
		for chunk := range slices.Chunk(group, messaging.MaxSessionKickTokens) {
			// A time budget as well as a failure budget: a responder that answers, slowly, would
			// otherwise hold this call for (requests x its latency). The rest stays pending.
			if time.Since(started) > sessionKickDrainBudget {
				errs = append(errs, fmt.Errorf("%w: the time allowed for one pass (%s) ran out; the rest stays pending",
					ErrSessionsNotEnded, sessionKickDrainBudget))
				return errors.Join(errs...)
			}
			// 🔴 A KICK STORM AGAINST A DEAD RESPONDER. A fleet-sized debt is thousands of
			// requests, and each failed one waits out its attempts and backoff: carrying on past
			// the first few failures would hold this call (and the lock of the background pass)
			// for hours to learn what three failures already said. The rows stay; the next pass
			// asks again.
			if failures >= maxConsecutiveKickFailures {
				break
			}
			if kerr := api.endDeviceSessions(ctx, tenant, reason, chunk...); kerr != nil {
				errs = append(errs, kerr)
				failures++
				continue
			}
			failures = 0
			var done []PendingSessionKick
			for _, t := range chunk {
				done = append(done, rowsOf[t]...)
			}
			if derr := deleteConfirmedKicks(db, tenant, done); derr != nil {
				errs = append(errs, fmt.Errorf("%w: confirmed, but could not clear the pending kick: %v", ErrSessionsNotEnded, derr))
			}
		}
	}
	if failures >= maxConsecutiveKickFailures {
		errs = append(errs, fmt.Errorf("%w: giving up on this pass after %d failed requests in a row; the rest stays pending",
			ErrSessionsNotEnded, failures))
	}
	return errors.Join(errs...)
}

// sessionKickDrainBudget is how long one drain may keep asking the broker, and
// sessionKickPassBudget how long one background pass may keep paging across a tenant's queue.
// Variables so a test can shrink them.
var (
	sessionKickDrainBudget = 90 * time.Second
	sessionKickPassBudget  = 5 * time.Minute
)

// maxConsecutiveKickFailures is how many kick requests in a row one drain may see fail before it
// stops asking and leaves the remaining rows for the next pass.
const maxConsecutiveKickFailures = 3

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

// tenantsOwingKicks lists the tenants that hold a pending session kick matching scope, ordered.
//
// 🔴 THE ONE CROSS-TENANT READ OF THE KICK QUEUE. It runs under a system context because the
// answer to "which tenants owe a kick" cannot be scoped to one, and it selects only the tenant
// ids: no kick row, device token or reason leaves it, and it writes nothing. What follows is
// scoped by explicit predicates, not by the context: drainSessionKicks filters tenant_id on every
// read and delete (core.WithTenant keeps a system marker already on the context, so the
// automatic predicate is NOT what protects those statements). Its callers are the background
// drain (scope: everything) and a repeated DeleteDevice from a system context (scope: one
// device's delete debt), which has no tenant on its context and no device row left to name one.
func (api *Api) tenantsOwingKicks(ctx context.Context, scope func(*gorm.DB) *gorm.DB) ([]string, error) {
	tenants := make([]string, 0)
	if err := scope(api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingSessionKick{})).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return nil, err
	}
	return tenants, nil
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
	tenants, err := api.tenantsOwingKicks(ctx, func(db *gorm.DB) *gorm.DB { return db })
	if err != nil {
		return res, fmt.Errorf("listing tenants with pending session kicks: %w", err)
	}
	revTenants, err := api.tenantsOwingRevocationKicks(ctx)
	if err != nil {
		return res, fmt.Errorf("listing tenants with pending certificate-revocation kicks: %w", err)
	}
	tenants = mergeSortedTenants(tenants, revTenants)
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		// A fleet-sized debt (a revoked CA) is more than one page: keep paging while every page
		// is confirmed and more is due, to a bound, so one tenant cannot hold the pass for ever.
		passStarted := time.Now()
		for page := 0; page < maxDrainPagesPerTenant && time.Since(passStarted) < sessionKickPassBudget; page++ {
			if err := api.drainSessionKicks(ctx, tenant, pageSize, func(db *gorm.DB) *gorm.DB { return db }); err != nil {
				res.Failed++
				lastErr = err
				break
			}
			more, merr := api.hasDueKicks(ctx, tenant)
			if merr != nil || !more {
				break
			}
		}
		// The certificate-revocation queue is its own table (see PendingRevocationKick).
		passStarted = time.Now()
		for page := 0; page < maxDrainPagesPerTenant && time.Since(passStarted) < sessionKickPassBudget; page++ {
			if err := api.drainRevocationKicks(ctx, tenant, pageSize, 0); err != nil {
				res.Failed++
				lastErr = err
				break
			}
			more, merr := api.hasDueRevocationKicks(ctx, tenant)
			if merr != nil || !more {
				break
			}
		}
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

// maxDrainPagesPerTenant bounds the pages one background pass drains for one tenant.
const maxDrainPagesPerTenant = 20

// hasDueKicks reports whether the tenant still holds a pending kick that is due.
func (api *Api) hasDueKicks(ctx context.Context, tenant string) (bool, error) {
	var one []PendingSessionKick
	err := api.RDB.DB(core.WithTenant(ctx, tenant)).Where("tenant_id = ?", tenant).Limit(1).Find(&one).Error
	return len(one) > 0, err
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
	// The certificate-revocation queue counts too: the same alert covers it.
	rn, rage, rerr := api.revocationQueueGauge(db)
	if rerr != nil {
		log.Warn().Err(rerr).Msg("Unable to read the pending certificate-revocation kicks")
		return
	}
	api.SessionKickObserver(int(n)+rn, max(age, rage))
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

// dropWithdrawnDeviceDisabledKicks removes, and returns the rows without, the device-disabled
// debts of devices that are ENABLED now: a device whose row EXISTS and is not disabled. A device
// whose row is gone (deleted, soft or hard) keeps its debt: a disabled device that was then
// deleted still has a session to end, and a missing row is never read as "enabled".
//
// 🔴 THE DEBT IS CHECKED AGAIN AT THE MOMENT IT IS COLLECTED. Re-enabling a device deletes its
// row, but a drain that had already read the row would still ask the broker, and a replica
// still running a release that does not clear the row on enable leaves it for the drain. Either
// would end the session of a device that has just reconnected. The window between this check
// and the broker's answer remains, and is the width of one request.
func (api *Api) dropWithdrawnDeviceDisabledKicks(db *gorm.DB, tenant string, rows []PendingSessionKick) ([]PendingSessionKick, error) {
	var tokens []string
	for _, r := range rows {
		if r.Origin == kickOriginDeviceDisabled {
			tokens = append(tokens, r.DeviceToken)
		}
	}
	if len(tokens) == 0 {
		return rows, nil
	}
	enabledNow := map[string]bool{}
	for chunk := range slices.Chunk(tokens, rdb.MaxLookupKeys) {
		var enabled []string
		if err := db.Unscoped().Model(&Device{}).
			Where("tenant_id = ? AND token IN ? AND disabled_at IS NULL AND deleted_at IS NULL", tenant, chunk).
			Pluck("token", &enabled).Error; err != nil {
			return nil, err
		}
		for _, t := range enabled {
			enabledNow[t] = true
		}
	}
	kept := rows[:0:0]
	var gone []PendingSessionKick
	for _, r := range rows {
		if r.Origin == kickOriginDeviceDisabled && enabledNow[r.DeviceToken] {
			gone = append(gone, r)
			continue
		}
		kept = append(kept, r)
	}
	if len(gone) > 0 {
		if err := deleteConfirmedKicks(db, tenant, gone); err != nil {
			return nil, err
		}
	}
	return kept, nil
}
