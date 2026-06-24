// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

// Device type entity.
type IDeviceType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Device entity.
type IDevice interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetDeviceType() DefaultDeviceDeviceType
}

// Device relationship type entity.
type IDeviceRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Device relationship entity.
type IDeviceRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceDevice() DefaultDeviceRelationshipSourceDevice
	GetTargets() DefaultDeviceRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultDeviceRelationshipRelationshipTypeDeviceRelationshipType
}

// Device group entity.
type IDeviceGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Device group relationship type entity.
type IDeviceGroupRelationshipType interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
}

// Device group relationship entity.
type IDeviceGroupRelationship interface {
	IModel
	ITokenReference
	IMetadataEntity
	GetSourceDeviceGroup() DefaultDeviceGroupRelationshipSourceDeviceGroup
	GetTargets() DefaultDeviceGroupRelationshipTargetsEntityRelationshipTargets
	GetRelationshipType() DefaultDeviceGroupRelationshipRelationshipTypeDeviceGroupRelationshipType
}
