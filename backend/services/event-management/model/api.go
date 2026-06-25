// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
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

	// PersistInTx runs fn inside a single database transaction whose handle
	// carries the supplied (tenant-scoped) context, so a message's events are
	// committed all-or-nothing (ADR-022 E5).
	PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error

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
// NOTE: this makes a message's writes all-or-nothing, but it does NOT make the
// at-least-once consume path idempotent — a redelivered message will re-insert
// its rows. True idempotency needs a per-event dedup key; that is a follow-up
// (tracked separately) and out of scope for E5.
func (api *Api) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}

// Create a new location event.
func (api *Api) CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error) {
	created := &LocationEvent{
		DeviceId:     request.DeviceId,
		OccurredTime: request.OccurredTime,
		Latitude:     rdb.NullFloat64Of(request.Latitude),
		Longitude:    rdb.NullFloat64Of(request.Longitude),
		Elevation:    rdb.NullFloat64Of(request.Elevation),
		Event:        request.Event,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a new measurement event.
func (api *Api) CreateMeasurementEvent(ctx context.Context, request *MeasurementEventCreateRequest) (*MeasurementEvent, error) {
	created := &MeasurementEvent{
		DeviceId:     request.DeviceId,
		EventType:    request.EventType,
		OccurredTime: request.OccurredTime,
		Name:         request.Name,
		Value:        rdb.NullFloat64Of(request.Value),
		Classifier:   request.Classifier,
		Event:        request.Event,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a new alert event.
func (api *Api) CreateAlertEvent(ctx context.Context, request *AlertEventCreateRequest) (*AlertEvent, error) {
	created := &AlertEvent{
		DeviceId:     request.DeviceId,
		EventType:    request.EventType,
		OccurredTime: request.OccurredTime,
		Type:         request.Type,
		Level:        request.Level,
		Message:      request.Message,
		Source:       request.Source,
		Event:        request.Event,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Create a batch of location events in a single multi-row INSERT on the given
// db handle (which may be a transaction). The per-row request->row mapping is
// identical to CreateLocationEvent; tenant scoping is applied by the global
// tenant-scope create callback, which stamps the tenant onto every slice entry.
func (api *Api) CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*LocationEventCreateRequest) ([]*LocationEvent, error) {
	if len(requests) == 0 {
		return []*LocationEvent{}, nil
	}
	created := make([]*LocationEvent, 0, len(requests))
	for _, request := range requests {
		created = append(created, &LocationEvent{
			DeviceId:     request.DeviceId,
			OccurredTime: request.OccurredTime,
			Latitude:     rdb.NullFloat64Of(request.Latitude),
			Longitude:    rdb.NullFloat64Of(request.Longitude),
			Elevation:    rdb.NullFloat64Of(request.Elevation),
			Event:        request.Event,
		})
	}
	result := db.WithContext(ctx).Create(&created)
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
	created := make([]*MeasurementEvent, 0, len(requests))
	for _, request := range requests {
		created = append(created, &MeasurementEvent{
			DeviceId:     request.DeviceId,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			Name:         request.Name,
			Value:        rdb.NullFloat64Of(request.Value),
			Classifier:   request.Classifier,
			Event:        request.Event,
		})
	}
	result := db.WithContext(ctx).Create(&created)
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
	created := make([]*AlertEvent, 0, len(requests))
	for _, request := range requests {
		created = append(created, &AlertEvent{
			DeviceId:     request.DeviceId,
			EventType:    request.EventType,
			OccurredTime: request.OccurredTime,
			Type:         request.Type,
			Level:        request.Level,
			Message:      request.Message,
			Source:       request.Source,
			Event:        request.Event,
		})
	}
	result := db.WithContext(ctx).Create(&created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}
