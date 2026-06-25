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

func (api *MockApi) CreateDeviceCredential(ctx context.Context, request *model.DeviceCredentialCreateRequest) (*model.DeviceCredential, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceCredential), args.Error(1)
}

func (api *MockApi) UpdateDeviceCredential(ctx context.Context, token string, request *model.DeviceCredentialCreateRequest) (*model.DeviceCredential, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceCredential), args.Error(1)
}

func (api *MockApi) DeviceCredentialsById(ctx context.Context, ids []uint) ([]*model.DeviceCredential, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.DeviceCredential), args.Error(1)
}

func (api *MockApi) DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*model.DeviceCredential, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.DeviceCredential), args.Error(1)
}

func (api *MockApi) DeviceCredentials(ctx context.Context, criteria model.DeviceCredentialSearchCriteria) (*model.DeviceCredentialSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceCredentialSearchResults), args.Error(1)
}

func (api *MockApi) DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*model.DeviceCredential, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.DeviceCredential), args.Error(1)
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

func (api *MockApi) SetEntityAttribute(ctx context.Context, request *model.EntityAttributeSetRequest) (*model.EntityAttribute, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.EntityAttribute), args.Error(1)
}

func (api *MockApi) EntityAttributes(ctx context.Context,
	criteria model.EntityAttributeSearchCriteria) (*model.EntityAttributeSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.EntityAttributeSearchResults), args.Error(1)
}

func (api *MockApi) DeleteEntityAttribute(ctx context.Context, entityType string, entity string, scope string, attrKey string) (bool, error) {
	args := api.Mock.Called()
	return args.Get(0).(bool), args.Error(1)
}

func (api *MockApi) EntityAttributesByEntity(ctx context.Context, entityType string, entityId uint, scope *string) ([]*model.EntityAttribute, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.EntityAttribute), args.Error(1)
}

func (api *MockApi) CreateMetricDefinition(ctx context.Context, request *model.MetricDefinitionCreateRequest) (*model.MetricDefinition, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.MetricDefinition), args.Error(1)
}

func (api *MockApi) UpdateMetricDefinition(ctx context.Context, token string, request *model.MetricDefinitionCreateRequest) (*model.MetricDefinition, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.MetricDefinition), args.Error(1)
}

func (api *MockApi) MetricDefinitionsById(ctx context.Context, ids []uint) ([]*model.MetricDefinition, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.MetricDefinition), args.Error(1)
}

func (api *MockApi) MetricDefinitionsByToken(ctx context.Context, tokens []string) ([]*model.MetricDefinition, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.MetricDefinition), args.Error(1)
}

func (api *MockApi) MetricDefinitions(ctx context.Context, criteria model.MetricDefinitionSearchCriteria) (*model.MetricDefinitionSearchResults, error) {
	args := api.Mock.Called()
	return args.Get(0).(*model.MetricDefinitionSearchResults), args.Error(1)
}

func (api *MockApi) MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*model.MetricDefinition, error) {
	args := api.Mock.Called()
	return args.Get(0).([]*model.MetricDefinition), args.Error(1)
}
