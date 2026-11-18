// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// PendingEntityDeletion preserves a deletion until the broker acknowledges it. A lost
// fact leaves downstream shares, anchors and roster state attached to a dead entity;
// reusing its token would inherit that state. The delete and this immutable fact commit
// together, so a crash or broker outage delays delivery without losing it. A rollback
// leaves no fact. A publish acknowledged before a crash may be repeated: delivery is
// at least once, with a stable tenant-scoped broker deduplication ID.
type PendingEntityDeletion struct {
	Cascade     *datatypes.JSON
	ID          uint64    `gorm:"primaryKey;autoIncrement;index:idx_pending_entity_deletions_tenant_id,priority:2"`
	TenantId    string    `gorm:"not null;size:128;index:idx_pending_entity_deletions_tenant_id,priority:1"`
	EntityType  string    `gorm:"not null;size:64"`
	EntityId    uint      `gorm:"not null"`
	EntityToken string    `gorm:"not null;size:255"`
	DeletedTime time.Time `gorm:"not null"`
}

// TableName is NOT declared: it would bypass this area's TablePrefix, as described in
// ownership_outbox.go. The type name derives pending_entity_deletions.

// AuditExempt opts derived queue bookkeeping out of the audit journal. The journal already
// records the delete; journalling its announcement and drain adds no operator action.
func (PendingEntityDeletion) AuditExempt() bool { return true }

// entityTenantOnTx reads the tenant of the entity row about to be deleted, inside the delete's
// transaction. The fact names its tenant explicitly rather than leaving it to the tenant
// callback: that callback stamps nothing under a system context, and a fact with no tenant
// could never be published under one, so it would block every drain pass behind it. An entity
// with no tenant is refused, which rolls the delete back.
func entityTenantOnTx(tx *gorm.DB, model any, id uint) (string, error) {
	tenants := make([]string, 0, 1)
	if err := tx.Unscoped().Model(model).Where("id = ?", id).Pluck("tenant_id", &tenants).Error; err != nil {
		return "", fmt.Errorf("reading the tenant of the entity being deleted: %w", err)
	}
	if len(tenants) != 1 || tenants[0] == "" {
		return "", fmt.Errorf("the entity being deleted (id %d) has no single tenant to announce its deletion under", id)
	}
	return tenants[0], nil
}

// queueEntityDeletedOnTx records the deletion fact inside the delete's transaction. A failure
// must roll back the delete rather than lose its announcement.
//
// The caller takes one deletion time from this process's clock and stores it: every retry publishes the same
// DeletedTime, however late. It is this service's clock, not the database's, on purpose. The
// consumers order a deletion against other lifecycle times this service stamps from its own
// clock (a device's create on the roster, an attribute write), and a deletion stamped from a
// clock running ahead of those would outrank a re-creation made just after it.
func queueEntityDeletedOnTx(tx *gorm.DB, tenant string, etype entity.Type, id uint, token string, deletedTime time.Time, events []*AlarmStateChangeEvent) error {
	var cascade *datatypes.JSON
	if len(events) > 0 {
		encoded, err := json.Marshal(events)
		if err != nil {
			return fmt.Errorf("encoding deletion alarm cascade: %w", err)
		}
		value := datatypes.JSON(encoded)
		cascade = &value
	}
	if err := tx.Create(&PendingEntityDeletion{TenantId: tenant, EntityType: string(etype), EntityId: id,
		EntityToken: token, DeletedTime: deletedTime, Cascade: cascade}).Error; err != nil {
		return fmt.Errorf("queueing entity deletion: %w", err)
	}
	return nil
}

// deletionNow is the time a deletion fact is stamped with: the process clock, which
// api.ownershipNow lets a test replace.
func (api *Api) deletionNow() time.Time {
	if api.ownershipNow != nil {
		return api.ownershipNow().UTC()
	}
	return time.Now().UTC()
}

// ErrNoDeletionAlarmPublisher refuses to discard queued clears without a publisher.
var ErrNoDeletionAlarmPublisher = errors.New("no alarm publisher is wired; queued alarm clears cannot be published")

// ErrNoEntityDeletedPublisher refuses a misleading successful pass with no publisher.
var ErrNoEntityDeletedPublisher = errors.New("no entity deletion publisher is wired; queued deletions cannot be published")

// EntityDeletionDrainPageSize bounds each tenant's work in one pass.
const EntityDeletionDrainPageSize = rdb.MaxLookupKeys

// EntityDeletionDrainResult counts tenants visited, failed tenants and acknowledged facts.
type EntityDeletionDrainResult struct{ Tenants, Failed, Published int }

// DrainEntityDeletions publishes a page per tenant in insertion order. The sole cross-tenant
// read lists tenants; every fact read and delete runs under its own tenant. A tenant-local
// storage failure allows other tenants to proceed; a shared broker failure ends the pass.
func (api *Api) DrainEntityDeletions(ctx context.Context, pageSize int) (EntityDeletionDrainResult, error) {
	var res EntityDeletionDrainResult
	if api.EntityDeletedPublisher == nil {
		return res, ErrNoEntityDeletedPublisher
	}
	if pageSize < 1 || pageSize > EntityDeletionDrainPageSize {
		return res, fmt.Errorf("entity deletion drain page size must be between 1 and %d, got %d", EntityDeletionDrainPageSize, pageSize)
	}
	tenants := make([]string, 0)
	if err := api.RDB.DB(core.WithSystemContext(ctx)).Model(&PendingEntityDeletion{}).Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error; err != nil {
		return res, fmt.Errorf("listing tenants with queued deletions: %w", err)
	}
	res.Tenants = len(tenants)
	var lastErr error
	for _, tenant := range tenants {
		published, err := api.drainTenantEntityDeletions(core.WithTenant(ctx, tenant), pageSize)
		res.Published += published
		if err == nil {
			continue
		}
		res.Failed++
		lastErr = err
		var pubErr *entityDeletionPublishError
		if errors.As(err, &pubErr) {
			return res, err
		}
		log.Warn().Err(err).Str("tenant", tenant).Msg("Unable to drain queued entity deletions; they stay queued for the next pass")
	}
	return res, core.PassResult(res.Tenants, res.Failed, lastErr)
}

// entityDeletionPublishError marks a shared broker failure that ends the whole pass.
type entityDeletionPublishError struct{ err error }

func (e *entityDeletionPublishError) Error() string {
	return "publishing an entity deletion: " + e.err.Error()
}
func (e *entityDeletionPublishError) Unwrap() error { return e.err }

func (api *Api) drainTenantEntityDeletions(ctx context.Context, pageSize int) (int, error) {
	db := api.RDB.DB(ctx)
	rows := make([]PendingEntityDeletion, 0, pageSize)
	if err := db.Order("id ASC").Limit(pageSize).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("reading queued deletions: %w", err)
	}
	done := make([]uint64, 0, len(rows))
	var pubErr error
rowLoop:
	for _, row := range rows {
		var cascade []*AlarmStateChangeEvent
		if row.Cascade != nil {
			if err := json.Unmarshal(*row.Cascade, &cascade); err != nil {
				pubErr = fmt.Errorf("decoding deletion alarm cascade: %w", err)
				break
			}
		}
		for i, alarm := range cascade {
			if api.AlarmPublisher == nil {
				pubErr = ErrNoDeletionAlarmPublisher
				break rowLoop
			}
			if alarm == nil {
				pubErr = errors.New("nil alarm in deletion cascade")
				break rowLoop
			}
			if err := api.AlarmPublisher.PublishAlarmEventDedup(ctx, alarm, fmt.Sprintf("entity-deletion-%s-%d-alarm-%d", row.TenantId, row.ID, i)); err != nil {
				pubErr = &entityDeletionPublishError{err: err}
				break rowLoop
			}
		}
		event := &entityevent.EntityDeletedEvent{EntityType: entity.Type(row.EntityType), EntityId: row.EntityId, EntityToken: row.EntityToken, DeletedTime: row.DeletedTime}
		if err := api.EntityDeletedPublisher.PublishEntityDeleted(ctx, event); err != nil {
			pubErr = &entityDeletionPublishError{err: err}
			break // Later facts in this tenant must never overtake this one.
		}
		done = append(done, row.ID)
	}
	// Rows are immutable once inserted, so a plain delete by acknowledged IDs is correct.
	// Failure leaves acknowledged facts queued for safe at-least-once republication.
	if len(done) > 0 {
		if err := db.Where("id IN ?", done).Delete(&PendingEntityDeletion{}).Error; err != nil {
			return len(done), errors.Join(pubErr, fmt.Errorf("removing acknowledged deletions: %w", err))
		}
	}
	return len(done), pubErr
}

// entityDeletionDrainLockName names the drain's advisory lock. Its key is shared by old
// and new replicas during a rolling upgrade, so the name must never change.
const entityDeletionDrainLockName = "device-management-entity-deletion-drain"

// DrainEntityDeletionsLocked runs one pass under a process-independent advisory lock.
// Other replicas skip rather than publish the same queued facts concurrently.
func (api *Api) DrainEntityDeletionsLocked(ctx context.Context) error {
	var res EntityDeletionDrainResult
	err := api.RDB.SingletonPass(ctx, entityDeletionDrainLockName, func() error {
		var err error
		res, err = api.DrainEntityDeletions(ctx, EntityDeletionDrainPageSize)
		return err
	})
	if res.Published > 0 {
		log.Debug().Int("tenants", res.Tenants).Int("published", res.Published).Msg("Published queued entity deletions")
	}
	return err
}
