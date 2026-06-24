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

// Data required to create a device type.
type DeviceTypeCreateRequest struct {
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

// Represents a device type.
type DeviceType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Devices []Device
}

// Search criteria for locating device types.
type DeviceTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for device type search.
type DeviceTypeSearchResults struct {
	Results    []DeviceType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device.
type DeviceCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	DeviceTypeToken string
	Metadata        *string
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

// Search criteria for locating devices.
type DeviceSearchCriteria struct {
	rdb.Pagination
	DeviceType *string
}

// Results for device search.
type DeviceSearchResults struct {
	Results    []Device
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device relationship type.
type DeviceRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
	Tracked     bool
}

// Metadata indicating a relationship between devices.
type DeviceRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
	Tracked bool
}

// Search criteria for locating device relationship types.
type DeviceRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for device relationship type search.
type DeviceRelationshipTypeSearchResults struct {
	Results    []DeviceRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device relationship.
type DeviceRelationshipCreateRequest struct {
	Token            string
	SourceDevice     string
	RelationshipType string
	Targets          EntityRelationshipCreateRequest
	Metadata         *string
}

// Captures a relationship between devices.
type DeviceRelationship struct {
	EntityRelationship
	SourceDeviceId     uint
	SourceDevice       Device
	RelationshipTypeId uint
	RelationshipType   DeviceRelationshipType
}

// Search criteria for locating device relationships.
type DeviceRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceDevice     *string
	RelationshipType *string
	Tracked          *bool
}

// Results for device relationship search.
type DeviceRelationshipSearchResults struct {
	Results    []DeviceRelationship
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device group.
type DeviceGroupCreateRequest struct {
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

// Represents a group of devices.
type DeviceGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Search criteria for locating device groups.
type DeviceGroupSearchCriteria struct {
	rdb.Pagination
}

// Results for device group search.
type DeviceGroupSearchResults struct {
	Results    []DeviceGroup
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device group relationship type.
type DeviceGroupRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between device and group.
type DeviceGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Search criteria for locating device groups relationship types.
type DeviceGroupRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for device group search.
type DeviceGroupRelationshipTypeSearchResults struct {
	Results    []DeviceGroupRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device group relationship.
type DeviceGroupRelationshipCreateRequest struct {
	Token             string
	SourceDeviceGroup string
	RelationshipType  string
	Targets           EntityRelationshipCreateRequest
	Metadata          *string
}

// Represents a device-to-group relationship.
type DeviceGroupRelationship struct {
	EntityRelationship
	SourceDeviceGroupId uint
	SourceDeviceGroup   DeviceGroup
	RelationshipTypeId  uint
	RelationshipType    DeviceGroupRelationshipType
}

// Search criteria for locating device groups relationships.
type DeviceGroupRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceDeviceGroup *string
	RelationshipType  *string
}

// Results for device group relationship search.
type DeviceGroupRelationshipSearchResults struct {
	Results    []DeviceGroupRelationship
	Pagination rdb.SearchResultsPagination
}
