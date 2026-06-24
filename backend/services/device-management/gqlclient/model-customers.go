// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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
