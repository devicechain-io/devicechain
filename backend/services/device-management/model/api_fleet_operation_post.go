// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
)

// The posting transaction: one line, one device, one transaction. It is the ONLY code that applies a
// config.set line, and its order is the whole protocol:
//
//	 1  the operation's fence, SHARED (a control transition waits for every poster in flight)
//	 2  re-read dispatch, epoch and grantee under the fence; anything but the lease's own state rolls back
//	 3  the tenant erasure fence and the activation check of the database guard
//	 4  the device's attribute lock, by TRY (a poster never queues ahead of an interactive writer)
//	 5  re-read the line under the device lock: not PLANNED any more means another poster won
//	 6  the target must still exist and the key must still be declared by its active profile
//	 7  the before-image, the write, the membership and configuration recompute (the primitive's own path)
//	 8  the line is claimed LAST, by a conditional UPDATE; zero rows rolls the whole effect back
//	 9  the lease must still be valid at commit
//
// The effect and its before-image commit together or not at all, and a line that lost any race leaves no
// trace. A refusal the gates decide is recorded in a second short transaction under the same fence, so a
// refusal can never land after a pause. A retryable failure never changes the line's posting.

// FleetLease is the authority a batch of posts runs under: the epoch and grantee the operation had when
// the lease was granted, and the moment it lapses. A post that cannot commit before Deadline is rolled back.
type FleetLease struct {
	Epoch    int64
	Grantee  string
	Deadline time.Time
}

// FleetPostOutcome is what one attempt to post a line came to.
type FleetPostOutcome string

const (
	// FleetPosted: the effect and the claim committed together.
	FleetPosted FleetPostOutcome = "POSTED"
	// FleetPostRefused: a gate refused the line (REFUSED), and that is recorded.
	FleetPostRefused FleetPostOutcome = "REFUSED"
	// FleetPostSkipped: the target is gone (SKIPPED), and that is recorded separately from refusals.
	FleetPostSkipped FleetPostOutcome = "SKIPPED"
	// FleetPostBusy: the device's lock is held by another writer. Nothing was written; back off.
	FleetPostBusy FleetPostOutcome = "BUSY"
	// FleetPostLostRace: another poster or a cancel got the line first. Nothing was written.
	FleetPostLostRace FleetPostOutcome = "LOST_RACE"
	// FleetPostFenceRefused: the operation is not RUNNING under this lease any more. Nothing was written.
	FleetPostFenceRefused FleetPostOutcome = "FENCE_REFUSED"
	// FleetPostFenceBusy: the fence could not be taken in time (a control transition is draining). Not an attempt.
	FleetPostFenceBusy FleetPostOutcome = "FENCE_BUSY"
	// FleetPostNotActivated: the database guard is not provably in place. Hold; nothing was written.
	FleetPostNotActivated FleetPostOutcome = "NOT_ACTIVATED"
	// FleetPostLeaseExpired: the lease lapsed before the transaction could commit. Rolled back.
	FleetPostLeaseExpired FleetPostOutcome = "LEASE_EXPIRED"
	// FleetPostRetry: a retryable failure; the line stays PLANNED with its attempt recorded.
	FleetPostRetry FleetPostOutcome = "RETRY"
)

// FleetPostResult carries the outcome and, for a refusal or a retry, its code.
type FleetPostResult struct {
	Outcome FleetPostOutcome
	Code    string
}

// Codes a decided refusal carries on the line.
const (
	FleetLineKeyUndeclared = "CONFIGURATION_KEY_UNDECLARED"
	FleetLineTargetGone    = "TARGET_GONE"
)

// fleetPostTotalBudget bounds one posting transaction end to end. Per-statement timeouts alone do not
// bound a transaction (transaction_timeout is not on every supported major), so the context carries it.
const fleetPostTotalBudget = 8 * time.Second

var (
	errPostAbandoned = errors.New("the posting transaction was abandoned with a result")
	errPostDecided   = errors.New("a gate decided to refuse the line")

	// fleetEffectHook is the effect-invocation log: tests count how often the write itself runs. Nil in production.
	fleetEffectHook func()
	// fleetPostHook lets a test interleave a competing session at a named point of the posting transaction
	// ("read": after the unlocked line read; "claim": just before the claim; "claimed": just after it). Nil in production.
	fleetPostHook func(stage string, tx *gorm.DB)
	// fleetTryDeviceLock is the real try-lock; tests can model a held lock on SQLite.
	fleetTryDeviceLock = tryLockDeviceAttributes
)

// fleetValueImage is the before-image and the posted value of a line, as stored.
type fleetValueImage struct {
	Present    bool    `json:"present"`
	ValueType  string  `json:"valueType,omitempty"`
	Value      *string `json:"value,omitempty"`
	Generation int64   `json:"generation"`
}

type fleetDecision struct {
	posting, code, detail string
}

// PostFleetLine tries to apply one PLANNED line of a RUNNING operation under a lease.
func (api *Api) PostFleetLine(ctx context.Context, opID, lineID uint, lease FleetLease) (FleetPostResult, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return FleetPostResult{}, errors.New("a fleet operation needs a tenant in the context")
	}
	if lease.Deadline.IsZero() {
		// A lease with no deadline would bound nothing: it is refused, never read as "unbounded".
		return FleetPostResult{Outcome: FleetPostLeaseExpired}, nil
	}
	deadline := time.Now().Add(fleetPostTotalBudget)
	if d := lease.Deadline.Add(-500 * time.Millisecond); d.Before(deadline) {
		deadline = d
	}
	if !deadline.After(time.Now()) {
		return FleetPostResult{Outcome: FleetPostLeaseExpired}, nil
	}
	postCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var (
		result      FleetPostResult
		decided     *fleetDecision
		minted      *DeviceConfigurationRevision
		change      membershipChange
		devID       uint
		factRequest *EntityAttributeSetRequest
		factRemoved bool
		factAt      time.Time
	)
	abandon := func(o FleetPostOutcome) error {
		result = FleetPostResult{Outcome: o}
		return errPostAbandoned
	}
	err := api.runDeviceAttributeTx(postCtx, "fleet", func(tx *gorm.DB) error {
		result, decided, minted, change, devID = FleetPostResult{}, nil, nil, membershipChange{}, 0
		factRequest, factRemoved, factAt = nil, false, time.Time{}
		if err := fleetTxTimeouts(tx); err != nil {
			return err
		}
		// 1-2. The fence, shared, and the state under it.
		if err := fleetFence(tx, fleetOpKey(tenant, opID), true, "POST"); err != nil {
			if lockTimedOut(err) {
				return abandon(FleetPostFenceBusy)
			}
			return err
		}
		var op FleetOperation
		if err := tx.First(&op, opID).Error; err != nil {
			return err
		}
		if !ledger.Drivable(ledger.DriverProtocol(op.DriverProtocol)) || op.Dispatch != FleetDispatchRunning || op.ExecutionEpoch != lease.Epoch || op.Grantee != lease.Grantee {
			return abandon(FleetPostFenceRefused)
		}
		// 3. The erasure fence, then the guard.
		if err := rdb.ReadFenceIfEnforced(postCtx, tx, tenant); err != nil {
			return err
		}
		if err := fleetActivationCheck(tx); err != nil {
			if errors.Is(err, ErrFleetOperationNotActivated) {
				return abandon(FleetPostNotActivated)
			}
			return err
		}
		// 4. The device, by try. The line is read first (unlocked) only to learn which device.
		var line FleetOperationLine
		if err := tx.First(&line, lineID).Error; err != nil {
			return err
		}
		if line.OperationId != int64(opID) {
			return errors.New("the line does not belong to the operation")
		}
		if line.Posting != FleetPostingPlanned || line.RedactedAt.Valid {
			return abandon(FleetPostLostRace)
		}
		if fleetPostHook != nil {
			fleetPostHook("read", tx)
		}
		if !line.TargetId.Valid {
			return errors.New("a PLANNED line has no target")
		}
		devID = uint(line.TargetId.Int64)
		got, err := fleetTryDeviceLock(postCtx, tx, devID)
		if err != nil {
			return err
		}
		if !got {
			return abandon(FleetPostBusy)
		}
		// 5. Re-read the line under the device lock.
		if err := tx.First(&line, lineID).Error; err != nil {
			return err
		}
		if line.Posting != FleetPostingPlanned || line.RedactedAt.Valid {
			return abandon(FleetPostLostRace)
		}
		// 6. The target and the kind gate.
		var dev Device
		if err := tx.Select("id", "token").First(&dev, devID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				decided = &fleetDecision{string(ledger.PostingSkipped), FleetLineTargetGone, "the target device no longer exists"}
				return errPostDecided
			}
			return err
		}
		intent, err := fleetIntentFrom(op.Intent)
		if err != nil {
			return err
		}
		declared, _, err := activeConfigurationDeclaration(tx, devID)
		if err != nil {
			return err
		}
		if !configurationKeyDeclared(declared, intent.Key) {
			decided = &fleetDecision{FleetPostingRefused, FleetLineKeyUndeclared, "the key is not declared by the device's active profile"}
			return errPostDecided
		}
		// What the line writes: the operation's value, or, for a reversal, the original's before-image,
		// read and judged here, under the device lock, so no write can slip between the check and the effect.
		target := fleetValueImage{Present: !intent.Unset}
		if !intent.Unset {
			target.ValueType, target.Value = intent.ValueType, intent.Value
		}
		overridden := false
		var reversing *fleetReversalPost
		if intent.Reverse || line.ReversesLineId.Valid {
			rev, err := fleetReversalOnTx(tx, &op, &line, devID, intent)
			if err != nil {
				return err
			}
			if rev.decided != nil {
				decided = rev.decided
				return errPostDecided
			}
			target, overridden, reversing = rev.target, rev.changed, rev
		}
		if target.Present {
			// Persisted operations and reversal before-images can predate plan-time normalization.
			canonical, err := normalizeAttributeValue(target.ValueType, target.Value)
			if err != nil {
				decided = &fleetDecision{FleetPostingRefused, CodeConfigurationValueType, err.Error()}
				return errPostDecided
			}
			if canonical != nil {
				if err := checkBytes("attribute value", *canonical, MaxAttributeValueBytes); err != nil {
					decided = &fleetDecision{FleetPostingRefused, CodeConfigurationValueType, err.Error()}
					return errPostDecided
				}
			}
			target.Value = canonical
		}
		// 7. Before-image, effect, recompute: the primitive's own path, under the lock this transaction holds.
		priorGen, _, err := attributeGeneration(tx, devID, string(AttributeScopeShared), intent.Key)
		if err != nil {
			return err
		}
		if fleetEffectHook != nil {
			fleetEffectHook()
		}
		factAt = time.Now().UTC()
		factRequest = &EntityAttributeSetRequest{EntityType: "device", Entity: dev.Token,
			Scope: string(AttributeScopeShared), AttrKey: intent.Key, ValueType: target.ValueType, Value: target.Value}
		factRemoved = !target.Present
		patch := AttributePatch{{Scope: string(AttributeScopeShared), Key: intent.Key, LastUpdated: factAt}}
		if !target.Present {
			patch[0].Unset = true
		} else {
			patch[0].ValueType, patch[0].Value = target.ValueType, target.Value
		}
		receipt, err := api.applyAttributePatchTx(postCtx, tx, "device", devID, patch)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				decided = &fleetDecision{string(ledger.PostingSkipped), FleetLineTargetGone, "the target device no longer exists"}
				return errPostDecided
			}
			return err
		}
		var refusal *ConfigurationRefusal
		change, minted, err = api.reconcileAttributeWriteOnTx(postCtx, tx, "device", devID, intent.Key, string(AttributeScopeShared))
		if errors.As(err, &refusal) {
			decided = &fleetDecision{FleetPostingRefused, refusal.Code, refusal.Message}
			return errPostDecided
		}
		if err != nil {
			return err
		}
		if reversing != nil && !overridden {
			// Changed-since, second half: the revision walk, after the effect and under the configuration lock.
			moved, err := fleetReversalRevisionsOnTx(postCtx, tx, devID, intent.Key, reversing.original, minted)
			if err != nil {
				return err
			}
			if moved && !line.OverrideChanged {
				decided = &fleetDecision{FleetPostingRefused, FleetLineChangedSince, fleetChangedSinceDetail}
				return errPostDecided // the effect rolls back with the transaction
			}
			overridden = moved
		}
		revision := sql.NullInt64{}
		if minted != nil {
			revision = sql.NullInt64{Int64: minted.Revision, Valid: true}
		} else {
			var latest DeviceConfigurationRevision
			if lerr := tx.Where("device_id = ?", devID).Order("revision DESC").First(&latest).Error; lerr == nil {
				revision = sql.NullInt64{Int64: latest.Revision, Valid: true}
			} else if !errors.Is(lerr, gorm.ErrRecordNotFound) {
				return lerr
			}
		}
		prior := fleetValueImage{Generation: priorGen}
		if ch := receipt.Changes[0]; len(ch.Before) > 0 {
			prior.Present, prior.ValueType = true, ch.Before[0].ValueType
			if ch.Before[0].Value.Valid {
				v := ch.Before[0].Value.String
				prior.Value = &v
			}
		}
		posted := target
		posted.Generation = receipt.Changes[0].Generation
		priorJSON, err := json.Marshal(prior)
		if err != nil {
			return err
		}
		postedJSON, err := json.Marshal(posted)
		if err != nil {
			return err
		}
		// 8. The claim, LAST.
		if fleetPostHook != nil {
			fleetPostHook("claim", tx)
		}
		now := fleetTimestamp(tx)
		res := tx.Model(&FleetOperationLine{}).Where("id = ? AND posting = ?", lineID, FleetPostingPlanned).Updates(map[string]any{
			"posting": FleetPostingPosted, "prior": sql.NullString{String: string(priorJSON), Valid: true},
			"posted_value": sql.NullString{String: string(postedJSON), Valid: true}, "posted_revision": revision,
			"posted_epoch": sql.NullInt64{Int64: op.ExecutionEpoch, Valid: true}, "posted_at": now,
			"settle_by": fleetTimestampAfter(tx, time.Duration(op.SettleDeadlineS)*time.Second, time.Time{}),
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return abandon(FleetPostLostRace) // the whole effect rolls back with it
		}
		if overridden {
			// The line was applied over a change made since the original, because a person overrode it.
			// The journal row comes after the claim, which locked the line: lines first, then events.
			at, err := fleetMaintenanceNow(tx)
			if err != nil {
				return err
			}
			if err := tx.Create(&FleetOperationEvent{OperationId: int64(op.ID), LineId: sql.NullInt64{Int64: int64(lineID), Valid: true},
				Epoch: sql.NullInt64{Int64: op.ExecutionEpoch, Valid: true}, At: at, Kind: fleetEventOverrideChanged}).Error; err != nil {
				return err
			}
		}
		if fleetPostHook != nil {
			fleetPostHook("claimed", tx)
		}
		// 9. The lease must still be good at the moment of commit.
		if time.Now().After(lease.Deadline) {
			return abandon(FleetPostLeaseExpired)
		}
		result = FleetPostResult{Outcome: FleetPosted}
		return nil
	})
	switch {
	case err == nil:
		api.publishMintedConfiguration(ctx, devID, minted)
		api.afterMembershipRecompute(ctx, "device", devID, change)
		if factRemoved {
			api.emitDeviceAttribute(ctx, &DeviceAttributeEvent{DeviceToken: factRequest.Entity, AttrKey: factRequest.AttrKey,
				Scope: factRequest.Scope, Removed: true, UpdatedAt: factAt})
		} else {
			api.emitDeviceAttributeForSet(ctx, factRequest, factAt)
		}
		return result, nil
	case errors.Is(err, errPostAbandoned):
		if result.Outcome == FleetPostBusy {
			// A try-lock miss is a retryable posting attempt too. Without backoff a busy
			// head can spend every pass's permits before later due lines are selected.
			if rerr := api.RecordFleetLineRetry(ctx, lineID, "DEVICE_BUSY", time.Time{}); rerr != nil {
				return FleetPostResult{}, rerr
			}
			result.Code = "DEVICE_BUSY"
		}
		return result, nil
	case errors.Is(err, errPostDecided):
		return api.recordFleetRefusal(ctx, tenant, opID, lineID, lease, *decided)
	}
	if code, retryable := fleetRetryableCode(err); retryable {
		if rerr := api.RecordFleetLineRetry(ctx, lineID, code, time.Time{}); rerr != nil {
			return FleetPostResult{}, rerr
		}
		return FleetPostResult{Outcome: FleetPostRetry, Code: code}, nil
	}
	return FleetPostResult{}, err
}

func configurationKeyDeclared(declared []ConfigurationKey, key string) bool {
	for _, k := range declared {
		if k.Key == key {
			return true
		}
	}
	return false
}

// fleetTxTimeouts bounds each statement and the idle time of one posting transaction. PostgreSQL only.
func fleetTxTimeouts(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, kv := range [][2]string{{"lock_timeout", "2s"}, {"statement_timeout", "3s"}, {"idle_in_transaction_session_timeout", "5s"}} {
		if err := tx.Exec("SELECT set_config(?, ?, true)", kv[0], kv[1]).Error; err != nil {
			return err
		}
	}
	return nil
}

// fleetRetryableCode names a failure that leaves a line PLANNED to try again: a deadlock, a
// serialization failure, a lock or statement timeout, the transaction's own deadline, a busy row,
// or a lost connection. After an uncertain commit, the next post re-reads the line's durable claim.
// Anything else is an error the caller sees, never a REFUSED line (a retryable error is never a refusal).
func fleetRetryableCode(err error) (string, bool) {
	var busy *AttributeWriteBusyError
	switch {
	case errors.As(err, &busy):
		return "ATTRIBUTE_ROW_BUSY", true
	case errors.Is(err, context.DeadlineExceeded):
		return "POST_DEADLINE", true
	case connectionLost(err), strings.HasPrefix(attributeSQLState(err), "08"):
		return "CONNECTION_LOST", true
	}
	switch attributeSQLState(err) {
	case "57P01", "57P02", "57P03": // server shutdown, crash or temporarily unavailable
		return "CONNECTION_LOST", true
	case "40P01":
		return "DEADLOCK", true
	case "40001":
		return "SERIALIZATION", true
	case "55P03":
		return "LOCK_TIMEOUT", true
	case "57014":
		return "STATEMENT_TIMEOUT", true
	}
	return "", false
}

// recordFleetRefusal records a decided refusal in its own short transaction, under the same fence and
// state check as a post: a refusal can never overwrite a CANCELLED line or land after a pause.
func (api *Api) recordFleetRefusal(ctx context.Context, tenant string, opID, lineID uint, lease FleetLease, d fleetDecision) (FleetPostResult, error) {
	var result FleetPostResult
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		result = FleetPostResult{}
		if err := fleetTxTimeouts(tx); err != nil {
			return err
		}
		if err := fleetFence(tx, fleetOpKey(tenant, opID), true, "POST"); err != nil {
			if lockTimedOut(err) {
				result = FleetPostResult{Outcome: FleetPostFenceBusy}
				return nil
			}
			return err
		}
		var op FleetOperation
		if err := tx.First(&op, opID).Error; err != nil {
			return err
		}
		if !ledger.Drivable(ledger.DriverProtocol(op.DriverProtocol)) || op.Dispatch != FleetDispatchRunning || op.ExecutionEpoch != lease.Epoch || op.Grantee != lease.Grantee {
			result = FleetPostResult{Outcome: FleetPostFenceRefused}
			return errPostAbandoned
		}
		res := tx.Model(&FleetOperationLine{}).Where("id = ? AND posting = ?", lineID, FleetPostingPlanned).
			Updates(map[string]any{"posting": d.posting, "code": d.code, "detail": d.detail})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			result = FleetPostResult{Outcome: FleetPostLostRace}
			return nil
		}
		result = FleetPostResult{Outcome: FleetPostRefused, Code: d.code}
		if d.posting == string(ledger.PostingSkipped) {
			result.Outcome = FleetPostSkipped
		}
		return nil
	})
	if errors.Is(err, errPostAbandoned) {
		return result, nil
	}
	if err != nil {
		if lockTimedOut(err) {
			return FleetPostResult{Outcome: FleetPostFenceBusy}, nil
		}
		return FleetPostResult{}, err
	}
	return result, nil
}

// fleetRetryDelay is the backoff after the nth failed attempt: one second doubling to a minute.
func fleetRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Second
	for i := 1; i < attempts && d < time.Minute; i++ {
		d *= 2
	}
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

// RecordFleetLineRetry notes a retryable failure on a line: one more attempt, the code, and when it may be
// tried again (a jittered backoff). It never changes the line's posting, and its predicate keeps it from
// touching a line that was cancelled, refused or redacted meanwhile.
// The operation's runtime row carries the same code and when it was met: the last error the engine met
// on this operation, which BulkChange.lastErrorCode serves. Each is its own autocommit statement, so the
// runtime row stays outside every lock order.
// now is a SQLite fixture input only; PostgreSQL schedules from its own clock.
func (api *Api) RecordFleetLineRetry(ctx context.Context, lineID uint, code string, now time.Time) error {
	db := api.RDB.DB(ctx)
	var line FleetOperationLine
	if err := db.Select("attempts", "operation_id").First(&line, lineID).Error; err != nil {
		return err
	}
	delay := fleetRetryDelay(int(line.Attempts) + 1)
	delay += time.Duration(rand.Int64N(int64(delay)/4 + 1))
	recorded := db.Model(&FleetOperationLine{}).Where("id = ? AND posting = ?", lineID, FleetPostingPlanned).
		Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "last_error_code": code,
			"next_attempt_at": fleetTimestampAfter(db, delay, now)})
	if recorded.Error != nil || recorded.RowsAffected == 0 {
		return recorded.Error
	}
	at := fleetTimestamp(db)
	if !now.IsZero() && db.Dialector.Name() != "postgres" {
		at = now
	}
	// Best effort, like the driver's heartbeat: the line's own record above is what retries run on, and a
	// diagnostic that could not be written must not turn a recorded retry into a failed post.
	if err := db.Model(&FleetOperationRuntime{}).Where("operation_id = ?", line.OperationId).
		Updates(map[string]any{"last_error_code": code, "last_error_at": at}).Error; err != nil {
		log.Warn().Err(err).Str("code", code).Msg("Unable to record a bulk change's last error; the line's own record stands")
	}
	return nil
}
