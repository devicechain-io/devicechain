// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Api struct {
	RDB *rdb.RdbManager
}

// Create a new API instance.
func NewApi(rdb *rdb.RdbManager) *Api {
	api := &Api{}
	api.RDB = rdb
	return api
}

// Interface for event management API (used for mocking)
type EventManagementApi interface {
	CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error)
	CreateMeasurementEvent(ctx context.Context, request *MeasurementEventCreateRequest) (*MeasurementEvent, error)
	CreateAlertEvent(ctx context.Context, request *AlertEventCreateRequest) (*AlertEvent, error)

	// Batch creates persist all of a message's events of one type in a single
	// multi-row INSERT (ADR-022 E5). They run on the *gorm.DB they are handed so
	// that a caller can supply a transaction-bound handle (see PersistInTx); the
	// tenant-scope create callback fires on the batch destination, stamping the
	// tenant onto every row.
	CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error)
	CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error)
	CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error)

	// CreateEventAnchors persists an event's anchor set (ADR-013) on the given db
	// handle (a transaction), so the event is queryable by each of the device's
	// tracked-relationship dimensions.
	CreateEventAnchors(ctx context.Context, db *gorm.DB, anchors []*EventAnchor) error

	// PersistInTx runs fn inside a single database transaction whose handle
	// carries the supplied (tenant-scoped) context, so a message's events are
	// committed all-or-nothing (ADR-022 E5).
	PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error

	// EventExistsByAltId reports whether a resolved event with the given
	// alternateId was already persisted for the tenant in context, backing
	// idempotent ingestion of a redelivered message.
	EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error)

	Events(ctx context.Context, criteria EventSearchCriteria) (*EventSearchResults, error)
	LocationEvents(ctx context.Context, criteria EventSearchCriteria) (*LocationEventSearchResults, error)
	MeasurementEvents(ctx context.Context, criteria EventSearchCriteria) (*MeasurementEventSearchResults, error)
	AlertEvents(ctx context.Context, criteria EventSearchCriteria) (*AlertEventSearchResults, error)
}

// PersistInTx opens one transaction whose handle is bound to the supplied
// context so the tenant-scope GORM callbacks (which read the tenant from the
// statement context) still fire on every statement inside the transaction. The
// inserts performed by fn either all commit or all roll back, making a single
// message's events atomic (ADR-022 E5).
//
// This makes a message's writes all-or-nothing. Idempotency on the at-least-once
// consume path is layered on top via EventExistsByAltId: a redelivered resolved
// event carrying an alternateId is detected and skipped inside the transaction
// (PersistEvent), with the (tenant_id, alt_id, occurred_time) partial unique index
// as the race backstop. Events without an alternateId are still re-inserted on
// redelivery — supplying a stable alternateId is what opts an event into dedup.
func (api *Api) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

// EventExistsByAltId reports whether an event with the given alternateId already
// exists for the tenant in context at occurred (the dedup key components beyond
// tenant_id, which the global query callback applies). It backs idempotent
// ingestion: a redelivered resolved event is detected and skipped rather than
// double-persisted. db may be a transaction handle so the check and the inserts
// that follow share one transaction.
func (api *Api) EventExistsByAltId(ctx context.Context, db *gorm.DB, altId string, occurred time.Time) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).Model(&Event{}).
		Where("alt_id = ? AND occurred_time = ?", altId, occurred).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// upsertParentEvents inserts the parent `events` rows for a batch of child event
// requests (location/measurement/alert) before the children, so the child
// insert's (device_id, event_type, occurred_time) foreign key is satisfied. The
// rows are deduped on that natural key and inserted ON CONFLICT DO NOTHING:
// multiple measurements in one message share a single parent event, and a
// redelivered message re-presents the same key.
//
// This replaces GORM's implicit belongs-to upsert of the Event association. On a
// composite-primary-key hypertable that upsert emits an `ON CONFLICT DO UPDATE`
// with no inference target — invalid SQL (SQLSTATE 42601) — so it failed every
// insert. Creating the parent explicitly (and omitting the association on the
// child insert) keeps the parent/child write in one statement each without that
// path.
func upsertParentEvents(ctx context.Context, db *gorm.DB, events []*Event) error {
	if len(events) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(events))
	distinct := make([]*Event, 0, len(events))
	for _, e := range events {
		key := fmt.Sprintf("%d|%d|%d", e.DeviceId, int64(e.EventType), e.OccurredTime.UnixNano())
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		distinct = append(distinct, e)
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "device_id"}, {Name: "event_type"}, {Name: "occurred_time"}},
		DoNothing: true,
	}).Create(distinct).Error
}

// Create a new location event.
func (api *Api) CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error) {
	created, err := api.CreateLocationEvents(ctx, api.RDB.DB(ctx), []*LocationEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a new measurement event.
func (api *Api) CreateMeasurementEvent(ctx context.Context, request *MeasurementEventCreateRequest) (*MeasurementEvent, error) {
	created, err := api.CreateMeasurementEvents(ctx, api.RDB.DB(ctx), []*MeasurementEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a new alert event.
func (api *Api) CreateAlertEvent(ctx context.Context, request *AlertEventCreateRequest) (*AlertEvent, error) {
	created, err := api.CreateAlertEvents(ctx, api.RDB.DB(ctx), []*AlertEventCreateRequest{request})
	if err != nil {
		return nil, err
	}
	return created[0], nil
}

// Create a batch of location events in a single multi-row INSERT on the given
// db handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateLocationEvent; tenant scoping is applied by the global
// tenant-scope create callback, which stamps the tenant onto every slice entry.
func (api *Api) CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error) {
	if len(requests) == 0 {
		return []*LocationEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*LocationEvent, 0, len(requests))
	for _, request := range requests {
		parents = append(parents, &request.Event)
		created = append(created, &LocationEvent{
			DeviceId:     request.DeviceId,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			Latitude:     rdb.NullFloat64Of(request.Latitude),
			Longitude:    rdb.NullFloat64Of(request.Longitude),
			Elevation:    rdb.NullFloat64Of(request.Elevation),
		})
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; omit the association so GORM inserts
	// the child rows directly against the (device_id, event_type, occurred_time)
	// foreign key rather than re-upserting the parent.
	result := db.WithContext(ctx).Omit("Event").Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a batch of measurement events in a single multi-row INSERT on the given
// db handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateMeasurementEvent; tenant scoping is applied by the global
// tenant-scope create callback.
func (api *Api) CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*MeasurementEventCreateRequest) ([]*MeasurementEvent, error) {
	if len(requests) == 0 {
		return []*MeasurementEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*MeasurementEvent, 0, len(requests))
	for _, request := range requests {
		parents = append(parents, &request.Event)
		created = append(created, &MeasurementEvent{
			DeviceId:     request.DeviceId,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			Name:         request.Name,
			Value:        rdb.NullFloat64Of(request.Value),
			Classifier:   request.Classifier,
		})
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; omit the association so GORM inserts
	// the child rows directly against the (device_id, event_type, occurred_time)
	// foreign key rather than re-upserting the parent.
	result := db.WithContext(ctx).Omit("Event").Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a batch of alert events in a single multi-row INSERT on the given db
// handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateAlertEvent; tenant scoping is applied by the global
// tenant-scope create callback.
func (api *Api) CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*AlertEventCreateRequest) ([]*AlertEvent, error) {
	if len(requests) == 0 {
		return []*AlertEvent{}, nil
	}
	parents := make([]*Event, 0, len(requests))
	created := make([]*AlertEvent, 0, len(requests))
	for _, request := range requests {
		parents = append(parents, &request.Event)
		created = append(created, &AlertEvent{
			DeviceId:     request.DeviceId,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			Type:         request.Type,
			Level:        request.Level,
			Message:      request.Message,
			Source:       request.Source,
		})
	}
	if err := upsertParentEvents(ctx, db, parents); err != nil {
		return nil, err
	}
	// The parent events are upserted above; omit the association so GORM inserts
	// the child rows directly against the (device_id, event_type, occurred_time)
	// foreign key rather than re-upserting the parent.
	result := db.WithContext(ctx).Omit("Event").Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// CreateEventAnchors persists an event's anchor rows on the given db handle (a
// transaction). Anchors follow the same dedup policy as the events they index:
// an alternateId-bearing event is skipped before it reaches here on redelivery,
// and an event without one is re-persisted along with its anchors — so a plain
// insert keeps anchors in lockstep with the base event.
func (api *Api) CreateEventAnchors(ctx context.Context, db *gorm.DB, anchors []*EventAnchor) error {
	if len(anchors) == 0 {
		return nil
	}
	return db.WithContext(ctx).Create(anchors).Error
}
