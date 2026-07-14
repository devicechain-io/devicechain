// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

// Asset type entity.
type IAssetType interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
}

// Asset entity.
type IAsset interface {
	IModel
	ITokenReference
	INamedEntity
	IMetadataEntity
	GetAssetType() DefaultAssetAssetType
}
