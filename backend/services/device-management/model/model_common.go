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
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Base data required to create an entity relationship.
type EntityRelationshipCreateRequest struct {
	TargetDevice        *string
	TargetDeviceGroup   *string
	TargetAsset         *string
	TargetAssetGroup    *string
	TargetArea          *string
	TargetAreaGroup     *string
	TargetCustomer      *string
	TargetCustomerGroup *string
}

// Based data for capturing a relationship between entites.
type EntityRelationship struct {
	gorm.Model
	rdb.TokenReference
	rdb.MetadataEntity
	TargetDeviceId        *uint
	TargetDevice          *Device
	TargetDeviceGroupId   *uint
	TargetDeviceGroup     *DeviceGroup
	TargetAssetId         *uint
	TargetAsset           *Asset
	TargetAssetGroupId    *uint
	TargetAssetGroup      *AssetGroup
	TargetAreaId          *uint
	TargetArea            *Area
	TargetAreaGroupId     *uint
	TargetAreaGroup       *AreaGroup
	TargetCustomerId      *uint
	TargetCustomer        *Customer
	TargetCustomerGroupId *uint
	TargetCustomerGroup   *CustomerGroup
}
