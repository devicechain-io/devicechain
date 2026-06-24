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

// Device group entity.
type IDeviceGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}
