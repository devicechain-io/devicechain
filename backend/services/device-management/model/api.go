// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

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

	// Entity relationships (uniform edge model, ADR-013).
	EntityRelationshipsById(ctx context.Context, ids []uint) ([]*EntityRelationship, error)
	EntityRelationshipsByToken(ctx context.Context, tokens []string) ([]*EntityRelationship, error)
	EntityRelationships(ctx context.Context, criteria EntityRelationshipSearchCriteria) (*EntityRelationshipSearchResults, error)
	CreateEntityRelationship(ctx context.Context, request *EntityRelationshipCreateRequest) (*EntityRelationship, error)
	EntityRelationshipTypesByToken(ctx context.Context, tokens []string) ([]*EntityRelationshipType, error)
}
