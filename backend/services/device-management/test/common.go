// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/mock"
)

// Microservice information used for testing.
var DeviceManagementMicroservice = &core.Microservice{
	StartTime:                    time.Now(),
	InstanceId:                   "devicechain",
	TenantId:                     "tenant1",
	TenantName:                   "Tenant 1",
	MicroserviceId:               "device-management",
	MicroserviceName:             "Device Management",
	FunctionalArea:               "device-management",
	InstanceConfiguration:        config.InstanceConfiguration{},
	MicroserviceConfigurationRaw: make([]byte, 0),
}

/**
 * Mock for device management API.
 */

type MockApi struct {
	mock.Mock
}

func (api *MockApi) DeviceTypesById(ctx context.Context, ids []uint) ([]*model.DeviceType, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.DeviceType), args.Error(1)
}

func (api *MockApi) DeviceTypesByToken(ctx context.Context, tokens []string) ([]*model.DeviceType, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.DeviceType), args.Error(1)
}

func (api *MockApi) DeviceTypes(ctx context.Context, criteria model.DeviceTypeSearchCriteria) (*model.DeviceTypeSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceTypeSearchResults), args.Error(1)
}

func (api *MockApi) DevicesById(ctx context.Context, ids []uint) ([]*model.Device, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.Device), args.Error(1)
}

func (api *MockApi) DevicesByToken(ctx context.Context, tokens []string) ([]*model.Device, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.Device), args.Error(1)
}

func (api *MockApi) Devices(ctx context.Context, criteria model.DeviceSearchCriteria) (*model.DeviceSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceSearchResults), args.Error(1)
}

func (api *MockApi) EntityRelationshipsById(ctx context.Context, ids []uint) ([]*model.EntityRelationship, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.EntityRelationship), args.Error(1)
}

func (api *MockApi) EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*model.EntityRelationship, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.EntityRelationship), args.Error(1)
}

func (api *MockApi) EntityRelationships(ctx context.Context,
	criteria model.EntityRelationshipSearchCriteria) (*model.EntityRelationshipSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.EntityRelationshipSearchResults), args.Error(1)
}

func (api *MockApi) CreateEntityRelationship(ctx context.Context, request *model.EntityRelationshipCreateRequest) (*model.EntityRelationship, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.EntityRelationship), args.Error(1)
}

func (api *MockApi) EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*model.EntityRelationshipType, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.EntityRelationshipType), args.Error(1)
}
