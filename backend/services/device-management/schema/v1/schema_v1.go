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

package v1

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

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

// Represents a device type.
type DeviceType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Devices []Device
}

// Represents a device.
type Device struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeId uint
	DeviceType   *DeviceType
}

// Metadata indicating a relationship between devices.
type DeviceRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
	Tracked bool
}

// Captures a relationship between devices.
type DeviceRelationship struct {
	EntityRelationship
	SourceDeviceId     uint
	SourceDevice       Device
	RelationshipTypeId uint
	RelationshipType   DeviceRelationshipType
}

// Represents a group of devices.
type DeviceGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Metadata indicating a relationship between device and group.
type DeviceGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Represents a device-to-group relationship.
type DeviceGroupRelationship struct {
	EntityRelationship
	SourceDeviceGroupId uint
	SourceDeviceGroup   DeviceGroup
	RelationshipTypeId  uint
	RelationshipType    DeviceGroupRelationshipType
}

// Data required to create an asset type.
type AssetTypeCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	Metadata        *string
}

// Represents an asset type.
type AssetType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Assets []Asset
}

// Search criteria for locating asset types.
type AssetTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for asset type search.
type AssetTypeSearchResults struct {
	Results    []AssetType
	Pagination rdb.SearchResultsPagination
}

// Data required to create an asset.
type AssetCreateRequest struct {
	Token          string
	Name           *string
	Description    *string
	AssetTypeToken string
	Metadata       *string
}

// Represents an asset.
type Asset struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AssetTypeId uint
	AssetType   *AssetType
}

// Search criteria for locating assets.
type AssetSearchCriteria struct {
	rdb.Pagination
	AssetTypeToken *string
}

// Results for asset search.
type AssetSearchResults struct {
	Results    []Asset
	Pagination rdb.SearchResultsPagination
}

// Data required to create an asset relationship type.
type AssetRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between assets.
type AssetRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Captures a relationship between assets.
type AssetRelationship struct {
	EntityRelationship
	SourceAssetId      uint
	SourceAsset        Asset
	RelationshipTypeId uint
	RelationshipType   AssetRelationshipType
}

// Represents a group of assets.
type AssetGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Metadata indicating a relationship between asset and group.
type AssetGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Represents a asset-to-group relationship.
type AssetGroupRelationship struct {
	EntityRelationship
	SourceAssetGroupId uint
	SourceAssetGroup   AssetGroup
	AssetId            uint
	RelationshipTypeId uint
	RelationshipType   AssetGroupRelationshipType
}

// Represents an area type.
type AreaType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Areas []Area
}

// Represents an area.
type Area struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	AreaTypeId uint
	AreaType   *AreaType
}

// Metadata indicating a relationship between areas.
type AreaRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Captures a relationship between areas.
type AreaRelationship struct {
	EntityRelationship
	SourceAreaId       uint
	SourceArea         Area
	RelationshipTypeId uint
	RelationshipType   AreaRelationshipType
}

// Represents a group of areas.
type AreaGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Metadata indicating a relationship between area and group.
type AreaGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Represents a area-to-group relationship.
type AreaGroupRelationship struct {
	EntityRelationship
	SourceAreaGroupId  uint
	SourceAreaGroup    AreaGroup
	RelationshipTypeId uint
	RelationshipType   AreaGroupRelationshipType
}

// Represents a customer type.
type CustomerType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Customers []Customer
}

// Represents a customer.
type Customer struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	CustomerTypeId uint
	CustomerType   *CustomerType
}

// Metadata indicating a relationship between customers.
type CustomerRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Captures a relationship between customers.
type CustomerRelationship struct {
	EntityRelationship
	SourceCustomerId   uint
	SourceCustomer     Customer
	RelationshipTypeId uint
	RelationshipType   CustomerRelationshipType
}

// Represents a group of customers.
type CustomerGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Metadata indicating a relationship between customer and group.
type CustomerGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Represents a customer-to-group relationship.
type CustomerGroupRelationship struct {
	EntityRelationship
	SourceCustomerGroupId uint
	SourceCustomerGroup   CustomerGroup
	RelationshipTypeId    uint
	RelationshipType      CustomerGroupRelationshipType
}
