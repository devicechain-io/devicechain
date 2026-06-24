// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

// Area type entity.
type IAreaType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Area entity.
type IArea interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetAreaType() DefaultAreaAreaType
}

// Area relationship type entity.
type IAreaRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Area relationship entity.
type IAreaRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceArea() DefaultAreaRelationshipSourceArea
	GetTargets() DefaultAreaRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAreaRelationshipRelationshipTypeAreaRelationshipType
}

// Area group entity.
type IAreaGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Area group relationship type entity.
type IAreaGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Area group relationship entity.
type IAreaGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceAreaGroup() DefaultAreaGroupRelationshipSourceAreaGroup
	GetTargets() DefaultAreaGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultAreaGroupRelationshipRelationshipTypeAreaGroupRelationshipType
}
