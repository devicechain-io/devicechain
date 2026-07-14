// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

// Uniform entity group entity (ADR-061).
type IEntityGroup interface {
	IModel
	ITokenReference
	INamedEntity
	IBrandedEntity
	IMetadataEntity
	GetMemberType() string
	GetMembershipMode() string
}
