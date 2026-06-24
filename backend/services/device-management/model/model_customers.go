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

// Data required to create a customer type.
type CustomerTypeCreateRequest struct {
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

// Represents a customer type.
type CustomerType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	Customers []Customer
}

// Search criteria for locating customer types.
type CustomerTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for customer type search.
type CustomerTypeSearchResults struct {
	Results    []CustomerType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer.
type CustomerCreateRequest struct {
	Token             string
	Name              *string
	Description       *string
	CustomerTypeToken string
	Metadata          *string
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

// Search criteria for locating customers.
type CustomerSearchCriteria struct {
	rdb.Pagination
	CustomerTypeToken *string
}

// Results for customer search.
type CustomerSearchResults struct {
	Results    []Customer
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer relationship type.
type CustomerRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between customers.
type CustomerRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Search criteria for locating customer relationship types.
type CustomerRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for customer relationship type search.
type CustomerRelationshipTypeSearchResults struct {
	Results    []CustomerRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer relationship.
type CustomerRelationshipCreateRequest struct {
	Token            string
	SourceCustomer   string
	RelationshipType string
	Targets          EntityRelationshipCreateRequest
	Metadata         *string
}

// Captures a relationship between customers.
type CustomerRelationship struct {
	EntityRelationship
	SourceCustomerId   uint
	SourceCustomer     Customer
	RelationshipTypeId uint
	RelationshipType   CustomerRelationshipType
}

// Search criteria for locating customer relationships.
type CustomerRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceCustomer   *string
	RelationshipType *string
}

// Results for customer relationship search.
type CustomerRelationshipSearchResults struct {
	Results    []CustomerRelationship
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer group.
type CustomerGroupCreateRequest struct {
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

// Represents a group of customers.
type CustomerGroup struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Search criteria for locating customer groups.
type CustomerGroupSearchCriteria struct {
	rdb.Pagination
}

// Results for customer group search.
type CustomerGroupSearchResults struct {
	Results    []CustomerGroup
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer group relationship type.
type CustomerGroupRelationshipTypeCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Metadata    *string
}

// Metadata indicating a relationship between customer and group.
type CustomerGroupRelationshipType struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity
}

// Search criteria for locating customer groups relationship types.
type CustomerGroupRelationshipTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for customer group search.
type CustomerGroupRelationshipTypeSearchResults struct {
	Results    []CustomerGroupRelationshipType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a customer group relationship.
type CustomerGroupRelationshipCreateRequest struct {
	Token               string
	SourceCustomerGroup string
	RelationshipType    string
	Targets             EntityRelationshipCreateRequest
	Metadata            *string
}

// Represents a customer-to-group relationship.
type CustomerGroupRelationship struct {
	EntityRelationship
	SourceCustomerGroupId uint
	SourceCustomerGroup   CustomerGroup
	RelationshipTypeId    uint
	RelationshipType      CustomerGroupRelationshipType
}

// Search criteria for locating customer groups relationships.
type CustomerGroupRelationshipSearchCriteria struct {
	rdb.Pagination
	SourceCustomerGroup *string
	RelationshipType    *string
}

// Results for customer group relationship search.
type CustomerGroupRelationshipSearchResults struct {
	Results    []CustomerGroupRelationship
	Pagination rdb.SearchResultsPagination
}
