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

package gqlclient

// Customer type entity.
type ICustomerType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Customer entity.
type ICustomer interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetCustomerType() DefaultCustomerCustomerType
}

// Customer relationship type entity.
type ICustomerRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Customer relationship entity.
type ICustomerRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceCustomer() DefaultCustomerRelationshipSourceCustomer
	GetTargets() DefaultCustomerRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultCustomerRelationshipRelationshipTypeCustomerRelationshipType
}

// Customer group entity.
type ICustomerGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Customer group relationship type entity.
type ICustomerGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Customer group relationship entity.
type ICustomerGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceCustomerGroup() DefaultCustomerGroupRelationshipSourceCustomerGroup
	GetTargets() DefaultCustomerGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultCustomerGroupRelationshipRelationshipTypeCustomerGroupRelationshipType
}
