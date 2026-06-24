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

// Interface for event management API (used for mocking)
type EventManagementApi interface {
	CreateLocationEvent(ctx context.Context, request *LocationEventCreateRequest) (*LocationEvent, error)
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
	result := api.RDB.Database.Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}
