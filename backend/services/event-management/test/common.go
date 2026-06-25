// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"time"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"
)

// Microservice information used for testing.
var EventManagementMicroservice = &core.Microservice{
	StartTime:                    time.Now(),
	InstanceId:                   "devicechain",
	TenantId:                     "tenant1",
	TenantName:                   "Tenant 1",
	MicroserviceId:               "event-management",
	MicroserviceName:             "Event Management",
	FunctionalArea:               "event-management",
	InstanceConfiguration:        config.InstanceConfiguration{},
	MicroserviceConfigurationRaw: make([]byte, 0),
}

/**
 * Mock for event management API.
 */

type MockApi struct {
	mock.Mock
}

func (api *MockApi) CreateLocationEvent(ctx context.Context, request *emmodel.LocationEventCreateRequest) (*emmodel.LocationEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.LocationEvent), args.Error(1)
}

func (api *MockApi) CreateMeasurementEvent(ctx context.Context, request *emmodel.MeasurementEventCreateRequest) (*emmodel.MeasurementEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.MeasurementEvent), args.Error(1)
}

func (api *MockApi) CreateAlertEvent(ctx context.Context, request *emmodel.AlertEventCreateRequest) (*emmodel.AlertEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.AlertEvent), args.Error(1)
}

func (api *MockApi) CreateLocationEvents(ctx context.Context, db *gorm.DB, requests []*emmodel.LocationEventCreateRequest) ([]*emmodel.LocationEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*emmodel.LocationEvent), args.Error(1)
}

func (api *MockApi) CreateMeasurementEvents(ctx context.Context, db *gorm.DB, requests []*emmodel.MeasurementEventCreateRequest) ([]*emmodel.MeasurementEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*emmodel.MeasurementEvent), args.Error(1)
}

func (api *MockApi) CreateAlertEvents(ctx context.Context, db *gorm.DB, requests []*emmodel.AlertEventCreateRequest) ([]*emmodel.AlertEvent, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*emmodel.AlertEvent), args.Error(1)
}

// PersistInTx runs fn directly (no real database in tests) passing a nil *gorm.DB
// handle. The batch create mocks above ignore the handle, so this preserves the
// all-or-nothing semantics that the production transaction provides while
// letting the .On(...) expectations record the persistence side effect.
func (api *MockApi) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	return fn(nil)
}

func (api *MockApi) Events(ctx context.Context, criteria emmodel.EventSearchCriteria) (*emmodel.EventSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.EventSearchResults), args.Error(1)
}

func (api *MockApi) LocationEvents(ctx context.Context, criteria emmodel.EventSearchCriteria) (*emmodel.LocationEventSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.LocationEventSearchResults), args.Error(1)
}

func (api *MockApi) MeasurementEvents(ctx context.Context, criteria emmodel.EventSearchCriteria) (*emmodel.MeasurementEventSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.MeasurementEventSearchResults), args.Error(1)
}

func (api *MockApi) AlertEvents(ctx context.Context, criteria emmodel.EventSearchCriteria) (*emmodel.AlertEventSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*emmodel.AlertEventSearchResults), args.Error(1)
}
