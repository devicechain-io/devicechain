// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package gqlclient

// Base model fields.
type IModel interface {
	GetId() string
	GetCreatedAt() *string
	GetUpdatedAt() *string
	GetDeletedAt() *string
}

// Entity that may be referenced by token.
type ITokenReference interface {
	GetToken() string
}

// Entity with name and description.
type INamedEntity interface {
	GetName() *string
	GetDescription() *string
}

// Information for branded entities.
type IBrandedEntity interface {
	GetImageUrl() *string
	GetIcon() *string
	GetBackgroundColor() *string
	GetForegroundColor() *string
	GetBorderColor() *string
}

// Entity with attached metadata.
type IMetadataEntity interface {
	GetMetadata() *string
}
