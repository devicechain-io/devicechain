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

// Data required to create an area type.
type AreaTypeCreateRequest struct {
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

// Represents an area type.
type AreaType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Areas []Area
}

// Search criteria for locating area types.
type AreaTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for area type search.
type AreaTypeSearchResults struct {
	Results    []AreaType
	Pagination rdb.SearchResultsPagination
}

// Data required to create an area.
type AreaCreateRequest struct {
	Token         string
	Name          *string
	Description   *string
	AreaTypeToken string
	Metadata      *string
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

// Search criteria for locating areas.
type AreaSearchCriteria struct {
	rdb.Pagination
	AreaTypeToken *string
}

// Results for area search.
type AreaSearchResults struct {
	Results    []Area
	Pagination rdb.SearchResultsPagination
}

// Data required to create a area relationship type.
type AreaRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between areas.
type AreaRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Search criteria for locating area relationship types.
type AreaRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for area relationship type search.
type AreaRelationshipTypeSearchResults struct {
	Results    []AreaRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a area relationship.
type AreaRelationshipCreateRequest struct {
	Token            string
	SourceArea       string
	RelationshipType string
	Targets          EntityRelationshipCreateRequest
	Metadata         *string
}

// Captures a relationship between areas.
type AreaRelationship struct {
	EntityRelationship
	SourceAreaId       uint
	SourceArea         Area
	RelationshipTypeId uint
	RelationshipType   AreaRelationshipType
}

// Search criteria for locating area relationships.
type AreaRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceArea       *string
	RelationshipType *string
}

// Results for area relationship search.
type AreaRelationshipSearchResults struct {
	Results    []AreaRelationship
	Pagination rdb.SearchResultsPagination
}

// Data required to create an area group.
type AreaGroupCreateRequest struct {
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

// Represents a group of areas.
type AreaGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Search criteria for locating area groups.
type AreaGroupSearchCriteria struct {
	rdb.Pagination
}

// Results for area group search.
type AreaGroupSearchResults struct {
	Results    []AreaGroup
	Pagination rdb.SearchResultsPagination
}

// Data required to create a area group relationship type.
type AreaGroupRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between area and group.
type AreaGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Search criteria for locating area groups relationship types.
type AreaGroupRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for area group search.
type AreaGroupRelationshipTypeSearchResults struct {
	Results    []AreaGroupRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a area group relationship.
type AreaGroupRelationshipCreateRequest struct {
	Token            string
	SourceAreaGroup  string
	RelationshipType string
	Targets          EntityRelationshipCreateRequest
	Metadata         *string
}

// Represents a area-to-group relationship.
type AreaGroupRelationship struct {
	EntityRelationship
	SourceAreaGroupId  uint
	SourceAreaGroup    AreaGroup
	RelationshipTypeId uint
	RelationshipType   AreaGroupRelationshipType
}

// Search criteria for locating area groups relationships.
type AreaGroupRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceAreaGroup  *string
	RelationshipType *string
}

// Results for area group relationship search.
type AreaGroupRelationshipSearchResults struct {
	Results    []AreaGroupRelationship
	Pagination rdb.SearchResultsPagination
}
