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
