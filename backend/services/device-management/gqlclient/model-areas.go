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
