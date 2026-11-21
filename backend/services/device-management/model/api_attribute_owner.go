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
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	path := "repair"
	if state, ok := tx.Statement.Context.Value(attributeTxContextKey{}).(*attributeTxState); ok {
		path = state.path
	}
	started := time.Now()
	err := tx.WithContext(tx.Statement.Context).Exec(`SELECT "device-management".dc_entity_attr_lock('device', ?)`, id).Error
	deviceAttributeLockWait.WithLabelValues(path).Observe(time.Since(started).Seconds())
	return err
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
		return nil
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
