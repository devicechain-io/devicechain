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

// The gqlclient package provides a GraphQL client for commonly used operations
// in the device management API.
package gqlclient

import "github.com/devicechain-io/dc-device-management/model"

//go:generate go run github.com/Khan/genqlient@v0.5.0

// Converts relationship targets into generated datatype.
func targets(req model.EntityRelationshipCreateRequest) EntityRelationshipTargetsCreateRequest {
	return EntityRelationshipTargetsCreateRequest{
		TargetDevice:        req.TargetDevice,
		TargetDeviceGroup:   req.TargetDeviceGroup,
		TargetAsset:         req.TargetAsset,
		TargetAssetGroup:    req.TargetAssetGroup,
		TargetArea:          req.TargetArea,
		TargetAreaGroup:     req.TargetAreaGroup,
		TargetCustomer:      req.TargetCustomer,
		TargetCustomerGroup: req.TargetCustomerGroup,
	}
}
