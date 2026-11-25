// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/attrprotocol"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	attributeWriteAttempts        = 5
	attributeWriteJitterMin       = 10 * time.Millisecond
	attributeWriteJitterMax       = 80 * time.Millisecond
	attributeWriteDeadlockRetries = 2
)

var errAttributeRowBusy = errors.New("device attribute row busy")

// AttributeWriteBusyError refuses an exhausted write without committing any of it.
// Return it directly so graphql-go sees the same Extensions hook as ConfigurationRefusal.
type AttributeWriteBusyError struct{}

func (*AttributeWriteBusyError) Error() string   { return "device attributes are busy; retry the write" }
func (*AttributeWriteBusyError) Retryable() bool { return true }
func (*AttributeWriteBusyError) Extensions() map[string]any {
	return map[string]any{"code": "ATTRIBUTE_WRITE_BUSY"}
}

// AttributePatch is applied in bytewise key, then scope order. An unset carries
// only Scope and Key. SkipUnchanged preserves restore/import's no-op semantics.
type AttributePatch []AttributeOp
type AttributeOp struct {
	Scope, Key, ValueType string
	Value                 *string
	LastUpdated           time.Time
	Unset, SkipUnchanged  bool
}
type AttributeChange struct {
	Scope, Key   string
	Before       []EntityAttribute // nil means absent; duplicates are retained, never hidden.
	After        *EntityAttribute
	RowsAffected int64
	Generation   int64
}
type AttributeReceipt struct {
	Changes      []AttributeChange
	Duplicates   bool
	RowsAffected int64
}

var (
	deviceAttributeLockWait       = promauto.NewHistogramVec(prometheus.HistogramOpts{Name: "device_attribute_lock_wait_seconds", Help: "Time spent acquiring a device attribute advisory lock."}, []string{"path"})
	deviceAttributeWriteYields    = promauto.NewCounterVec(prometheus.CounterOpts{Name: "device_attribute_write_yields_total", Help: "Device attribute row-lock yields and exhausted writes."}, []string{"path", "result"})
	deviceAttributeWriteDeadlocks = promauto.NewCounterVec(prometheus.CounterOpts{Name: "device_attribute_write_deadlocks_total", Help: "Deadlocks encountered by device attribute transactions."}, []string{"path"})
	// Test-only observation points. Nil in production; tests must not install them in parallel.
	attributeBeforeCommitHook func(*gorm.DB) error
	attributeAfterReadHook    func(*gorm.DB) error
	// attributeYieldHook runs after a busy attempt has rolled back and before the retry sleep.
	attributeYieldHook func(attempt int)
)

func init() {
	for _, path := range []string{"set", "unset", "restore", "copy", "delete", "import", "repair"} {
		deviceAttributeLockWait.WithLabelValues(path)
		deviceAttributeWriteDeadlocks.WithLabelValues(path)
		for _, result := range []string{"retried", "busy"} {
			deviceAttributeWriteYields.WithLabelValues(path, result)
		}
	}
}

type attributeTxContextKey struct{}
type attributeTxState struct {
	path   string
	owner  uint
	locked bool
	pool   gorm.ConnPool
}

func attributeSQLState(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		return state.SQLState()
	}
	return ""
}
func attributeRowError(err error) error {
	if attributeSQLState(err) == "55P03" {
		return fmt.Errorf("%w: %v", errAttributeRowBusy, err)
	}
	return err
}
func (api *Api) runDeviceAttributeTx(ctx context.Context, path string, fn func(tx *gorm.DB) error) error {
	deadlocks := 0
	for attempt := 0; attempt < attributeWriteAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		state := &attributeTxState{path: path}
		attemptCtx := context.WithValue(ctx, attributeTxContextKey{}, state)
		err := api.RDB.DB(attemptCtx).Transaction(func(tx *gorm.DB) error {
			if err := fn(tx); err != nil {
				return err
			}
			if attributeBeforeCommitHook != nil {
				return attributeBeforeCommitHook(tx)
			}
			return nil
		})
		if err == nil {
			return nil
		}
		switch {
		case errors.Is(err, errAttributeRowBusy):
			if attempt == attributeWriteAttempts-1 {
				deviceAttributeWriteYields.WithLabelValues(path, "busy").Inc()
				return &AttributeWriteBusyError{}
			}
			deviceAttributeWriteYields.WithLabelValues(path, "retried").Inc()
			if attributeYieldHook != nil {
				attributeYieldHook(attempt)
			}
		case attributeSQLState(err) == "40P01":
			deviceAttributeWriteDeadlocks.WithLabelValues(path).Inc()
			if deadlocks >= attributeWriteDeadlockRetries || attempt == attributeWriteAttempts-1 {
				return err
			}
			deadlocks++
		default:
			return err
		}
		delay := attributeWriteJitterMin + time.Duration(rand.Int64N(int64(attributeWriteJitterMax-attributeWriteJitterMin)+1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable device attribute retry exhaustion")
}

// lockDeviceAttributes calls the ONE SQL definition of the key. Missing migration
// is a hard error. Never implement or change the hash in Go.
func lockDeviceAttributes(ctx context.Context, tx *gorm.DB, id uint) error {
	if tx == nil || tx.Statement == nil {
		return errors.New("device attribute protocol requires a transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return errors.New("device attribute protocol requires a transaction")
	}
	// 🔴 ONLY INSIDE runDeviceAttributeTx. The runner is what carries the one-device state, the
	// bounded retry on a busy row and the post-commit discipline; a plain Transaction has none of
	// them, so a lock taken there is refused rather than quietly given a fresh state.
	state, _ := tx.Statement.Context.Value(attributeTxContextKey{}).(*attributeTxState)
	if state == nil {
		return errors.New("device attribute protocol requires runDeviceAttributeTx")
	}
	if state.locked && state.owner != id {
		return errors.New("device attribute transaction cannot own two devices")
	}
	if tx.Dialector.Name() != "postgres" {
		state.owner, state.locked, state.pool = id, true, tx.Statement.ConnPool
		return nil
	}
	path := state.path
	reacquire := state.locked && state.owner == id
	started := time.Now()
	err := acquireDeviceAttributeLock(tx, id)
	if !reacquire {
		// A same-owner re-acquire is free (the transaction already holds the lock); observing it
		// would skew the wait histogram toward zero.
		deviceAttributeLockWait.WithLabelValues(path).Observe(time.Since(started).Seconds())
	}
	if err != nil {
		return err
	}
	// Reacquire and mark even for the same owner: ROLLBACK TO SAVEPOINT can undo
	// both database effects without undoing the Go state. Never trust that cache.
	if err := tx.Exec(attrprotocol.MarkerSQL+deviceOwnerColumnSQL, id).Error; err != nil {
		return err
	}
	state.owner, state.locked, state.pool = id, true, tx.Statement.ConnPool
	return nil
}

func acquireDeviceAttributeLock(tx *gorm.DB, id uint) error {
	return tx.Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, id).Error
}

// deviceOwnerSetting names, transaction-locally, the device whose K_dev this transaction
// most recently took. It is set right after the lock and, like the lock, is undone by a
// ROLLBACK TO SAVEPOINT taken before it (a local setting reverts with its subtransaction),
// so assertMembershipDeviceOwner can read it instead of scanning pg_locks: that view
// copies the server's whole lock table under every lock-partition lock, on every write.
const deviceOwnerSetting = "devicechain.attr_device"

// deviceOwnerColumnSQL sets that setting. The lock helpers append it to the protocol
// marker's SELECT, so both are set by one statement right after K_dev is granted.
const deviceOwnerColumnSQL = ", set_config('" + deviceOwnerSetting + "', CAST(CAST(? AS bigint) AS text), true)"

type createdDeviceMembershipOwnerKey struct{}
type createdDeviceMembershipOwner struct {
	id   uint
	pool gorm.ConnPool
}

// lockCreatedDeviceForMembership is only for IDs inserted by the current create
// transaction, before enrollment. Those IDs are not yet visible to other writers.
// Batch creation can own several new devices; it must not relax the attribute
// runner's one-existing-device rule or manufacture an attribute writer state.
// No attributes are written here, so no writer-protocol marker is needed.
func lockCreatedDeviceForMembership(tx *gorm.DB, id uint) (*gorm.DB, error) {
	if tx == nil || tx.Statement == nil {
		return nil, errors.New("new device membership requires a transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, errors.New("new device membership requires a transaction")
	}
	if tx.Dialector.Name() == "postgres" {
		if err := acquireDeviceAttributeLock(tx, id); err != nil {
			return nil, err
		}
		if err := tx.Exec("SELECT TRUE"+deviceOwnerColumnSQL, id).Error; err != nil {
			return nil, err
		}
	}
	owner := createdDeviceMembershipOwner{id: id, pool: tx.Statement.ConnPool}
	return tx.WithContext(context.WithValue(tx.Statement.Context, createdDeviceMembershipOwnerKey{}, owner)), nil
}

// tryLockDeviceAttributes is lockDeviceAttributes without the wait: it answers false, having taken and
// marked nothing, when another transaction holds the device's lock. A fleet operation's posting transaction
// uses it so it never queues ahead of an interactive writer. The key is the same one (the try function
// repeats the hash, and a test holds the two in step). When it answers true the transaction owns the device
// and carries the writer-protocol marker exactly as if lockDeviceAttributes had run, so the primitive's own
// later acquire of the same lock is a free re-acquire.
func tryLockDeviceAttributes(ctx context.Context, tx *gorm.DB, id uint) (bool, error) {
	if tx == nil || tx.Statement == nil {
		return false, errors.New("device attribute protocol requires a transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return false, errors.New("device attribute protocol requires a transaction")
	}
	state, _ := tx.Statement.Context.Value(attributeTxContextKey{}).(*attributeTxState)
	if state == nil {
		return false, errors.New("device attribute protocol requires runDeviceAttributeTx")
	}
	if state.locked && state.owner != id {
		return false, errors.New("device attribute transaction cannot own two devices")
	}
	if tx.Dialector.Name() != "postgres" {
		state.owner, state.locked, state.pool = id, true, tx.Statement.ConnPool
		return true, nil
	}
	var got bool
	if err := tx.WithContext(tx.Statement.Context).Raw(`SELECT "device-management".dc_entity_attr_try_lock('device', ?)`, id).Scan(&got).Error; err != nil {
		return false, err
	}
	if !got {
		return false, nil
	}
	if err := tx.Exec(attrprotocol.MarkerSQL+deviceOwnerColumnSQL, id).Error; err != nil {
		return false, err
	}
	state.owner, state.locked, state.pool = id, true, tx.Statement.ConnPool
	return true, nil
}

// lockAttributeOwnerTx may precede declaration discovery, which itself reads the
// device. The primitive reuses that lock in the same attempt. A runner transaction
// never locks two devices, including copy's source and target.
func lockAttributeOwnerTx(ctx context.Context, tx *gorm.DB, entityType string, id uint) error {
	if entityType != string(entity.TypeDevice) {
		return nil
	}
	state, _ := tx.Statement.Context.Value(attributeTxContextKey{}).(*attributeTxState)
	if state != nil && state.locked {
		if state.owner != id {
			return errors.New("device attribute transaction cannot own two devices")
		}
	}
	if err := lockDeviceAttributes(ctx, tx, id); err != nil {
		return err
	}
	// Row 2: the device must still exist. On Postgres FOR KEY SHARE also makes a hard
	// DELETE of the device row — from any pod version, or from tenant purge — wait for
	// this transaction. The existence check runs on every dialect so a write never
	// creates attributes for a device deleted after its token was resolved.
	var device Device
	read := tx.Select("id")
	if tx.Dialector.Name() == "postgres" {
		read = read.Clauses(clause.Locking{Strength: "KEY SHARE"})
	}
	if err := read.First(&device, id).Error; err != nil {
		return err
	}
	if state != nil {
		state.owner, state.locked = id, true
	}
	return nil
}

// applyAttributePatchTx is the ONLY code that writes EntityAttribute rows (set/unset) on a device.
func (api *Api) applyAttributePatchTx(ctx context.Context, tx *gorm.DB, entityType string, entityId uint, patch AttributePatch) (*AttributeReceipt, error) {
	ops := slices.Clone(patch)
	slices.SortStableFunc(ops, func(a, b AttributeOp) int {
		return cmp.Or(strings.Compare(a.Key, b.Key), strings.Compare(a.Scope, b.Scope))
	})
	receipt := &AttributeReceipt{Changes: make([]AttributeChange, 0, len(ops))}
	if len(ops) == 0 {
		if err := lockAttributeOwnerTx(ctx, tx, entityType, entityId); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	pairs := make([][]any, 0, len(ops))
	for _, op := range ops {
		pairs = append(pairs, []any{op.Scope, op.Key})
	}
	before, err := readAttributeBeforeImageTx(ctx, tx, entityType, entityId, pairs)
	if err != nil {
		return nil, err
	}
	if attributeAfterReadHook != nil {
		if err := attributeAfterReadHook(tx); err != nil {
			return nil, err
		}
	}
	images := map[[2]string][]EntityAttribute{}
	for _, row := range before {
		key := [2]string{row.Scope, row.AttrKey}
		images[key] = append(images[key], row)
		if len(images[key]) > 1 {
			receipt.Duplicates = true
		}
	}
	for _, op := range ops {
		key := [2]string{op.Scope, op.Key}
		change := AttributeChange{Scope: op.Scope, Key: op.Key, Before: slices.Clone(images[key])}
		if op.Unset {
			if !op.SkipUnchanged || len(change.Before) > 0 {
				result := tx.Where("entity_type = ? AND entity_id = ? AND scope = ? AND attr_key = ?", entityType, entityId, op.Scope, op.Key).Delete(&EntityAttribute{})
				if result.Error != nil {
					return nil, result.Error
				}
				change.RowsAffected = result.RowsAffected
			}
			images[key] = nil
		} else {
			value := rdb.NullStrOf(op.Value)
			if op.SkipUnchanged && len(change.Before) == 1 && change.Before[0].ValueType == op.ValueType && change.Before[0].Value == value {
				row := change.Before[0]
				change.After = &row
			} else {
				row := &EntityAttribute{EntityType: entityType, EntityId: entityId, Scope: op.Scope, AttrKey: op.Key, ValueType: op.ValueType, Value: value, LastUpdated: op.LastUpdated}
				if err := upsertAttributeOnTx(tx, row); err != nil {
					return nil, err
				}
				change.After, change.RowsAffected = row, 1
			}
			images[key] = []EntityAttribute{*change.After}
		}
		if entityType == string(entity.TypeDevice) {
			// Restore/copy SkipUnchanged no-ops wrote nothing and must not bump.
			// An ordinary unset of an absent key is still a protocol operation.
			if !op.SkipUnchanged || change.RowsAffected > 0 {
				change.Generation, err = bumpAttributeGenerationTx(tx, entityId, op.Scope, op.Key)
			} else {
				change.Generation, _, err = attributeGeneration(tx, entityId, op.Scope, op.Key)
			}
			if err != nil {
				return nil, err
			}
		}
		receipt.RowsAffected += change.RowsAffected
		receipt.Changes = append(receipt.Changes, change)
	}
	return receipt, nil
}

// upsertAttributeOnTx lets the database serialize competing writes to a live key.
// RETURNING preserves stored identity and creation time. Only the owner calls it.
func upsertAttributeOnTx(db *gorm.DB, row *EntityAttribute) error {
	return db.Clauses(clause.OnConflict{
		Columns:     []clause.Column{{Name: "tenant_id"}, {Name: "entity_type"}, {Name: "entity_id"}, {Name: "scope"}, {Name: "attr_key"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "deleted_at IS NULL"}}},
		DoUpdates:   clause.AssignmentColumns([]string{"value_type", "value", "last_updated", "updated_at"}),
	}, clause.Returning{}).Create(row).Error
}

// Non-device families retain their transaction semantics without device locks or retries.
func (api *Api) runAttributeEntityTx(ctx context.Context, entityType, path string, fn func(*gorm.DB) error) error {
	if entityType == string(entity.TypeDevice) {
		return api.runDeviceAttributeTx(ctx, path, fn)
	}
	return api.RDB.DB(ctx).Transaction(fn)
}

// Keep lock acquisition and the before-image read adjacent: the receipt describes
// the image protected by this attempt's owner lock, never a pre-lock snapshot.
func readAttributeBeforeImageTx(ctx context.Context, tx *gorm.DB, entityType string, entityId uint, pairs [][]any) ([]EntityAttribute, error) {
	var before []EntityAttribute
	read := tx.Where("entity_type = ? AND entity_id = ?", entityType, entityId).Where("(scope, attr_key) IN ?", pairs).Order("id")
	if entityType == string(entity.TypeDevice) && tx.Dialector.Name() == "postgres" {
		read = read.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
	}
	if err := lockAttributeOwnerTx(ctx, tx, entityType, entityId); err != nil {
		return nil, err
	}
	err := read.Find(&before).Error
	return before, attributeRowError(err)
}
