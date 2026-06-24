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

package test

import (
	"context"
	"time"

	emmodel "github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/mock"
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
