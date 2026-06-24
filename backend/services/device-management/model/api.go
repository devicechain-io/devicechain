/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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

	// Device relationships.
	DeviceRelationshipsById(ctx context.Context, ids []uint) ([]*DeviceRelationship, error)
	DeviceRelationshipsByToken(ctx context.Context, tokens []string) ([]*DeviceRelationship, error)
	DeviceRelationships(ctx context.Context, criteria DeviceRelationshipSearchCriteria) (*DeviceRelationshipSearchResults, error)
	CreateDeviceRelationship(ctx context.Context, request *DeviceRelationshipCreateRequest) (*DeviceRelationship, error)
}
