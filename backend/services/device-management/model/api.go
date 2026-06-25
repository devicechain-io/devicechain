// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
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

// Interface for device management API (used for mocking)
type DeviceManagementApi interface {
	// Device types.
	DeviceTypesById(ctx context.Context, ids []uint) ([]*DeviceType, error)
	DeviceTypesByToken(ctx context.Context, tokens []string) ([]*DeviceType, error)
	DeviceTypes(ctx context.Context, criteria DeviceTypeSearchCriteria) (*DeviceTypeSearchResults, error)

	// Devices.
	DevicesById(ctx context.Context, ids []uint) ([]*Device, error)
	DevicesByToken(ctx context.Context, tokens []string) ([]*Device, error)
	Devices(ctx context.Context, criteria DeviceSearchCriteria) (*DeviceSearchResults, error)

	// Device credentials (ADR-014).
	CreateDeviceCredential(ctx context.Context, request *DeviceCredentialCreateRequest) (*DeviceCredential, error)
	UpdateDeviceCredential(ctx context.Context, token string, request *DeviceCredentialCreateRequest) (*DeviceCredential, error)
	DeviceCredentialsById(ctx context.Context, ids []uint) ([]*DeviceCredential, error)
	DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*DeviceCredential, error)
	DeviceCredentials(ctx context.Context, criteria DeviceCredentialSearchCriteria) (*DeviceCredentialSearchResults, error)
	DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error)

	// Device authentication (transport security, ADR-014).
	AuthenticateDevice(ctx context.Context, presented *PresentedCredential, now time.Time) (*Device, error)

	// Entity relationships (uniform edge model, ADR-013).
	EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error)
	EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*EntityRelationship, error)
	EntityRelationships(ctx context.Context, criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error)
	CreateEntityRelationship(ctx context.Context, request *EntityRelationshipCreateRequest) (*EntityRelationship, error)
	EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*EntityRelationshipType, error)

	// Metric definitions (ADR-016).
	CreateMetricDefinition(ctx context.Context, request *MetricDefinitionCreateRequest) (*MetricDefinition, error)
	UpdateMetricDefinition(ctx context.Context, token string, request *MetricDefinitionCreateRequest) (*MetricDefinition, error)
	MetricDefinitionsById(ctx context.Context, ids []uint) ([]*MetricDefinition, error)
	MetricDefinitionsByToken(ctx context.Context, tokens []string) ([]*MetricDefinition, error)
	MetricDefinitions(ctx context.Context, criteria MetricDefinitionSearchCriteria) (*MetricDefinitionSearchResults, error)
	MetricDefinitionsByDeviceType(ctx context.Context, deviceTypeId uint) ([]*MetricDefinition, error)

	// Entity attributes (ADR-012).
	SetEntityAttribute(ctx context.Context, request *EntityAttributeSetRequest) (*EntityAttribute, error)
	EntityAttributes(ctx context.Context, criteria EntityAttributeSearchCriteria) (*EntityAttributeSearchResults, error)
	DeleteEntityAttribute(ctx context.Context, entityType string, entity string, scope string, attrKey string) (bool, error)
	EntityAttributesByEntity(ctx context.Context, entityType string, entityId uint, scope *string) ([]*EntityAttribute, error)
}
