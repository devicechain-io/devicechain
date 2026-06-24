// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package entity defines DeviceChain's uniform entity-addressing vocabulary
// (ADR-013). Every addressable thing — a device, a customer, an area, a group,
// and so on — is named by a (Type, id) pair rather than by a dedicated typed
// foreign-key column. This package is the single source of truth for the set of
// entity Type values; relationship edges (device-management) and event anchors
// (event-management) both reference it so the two sides share one vocabulary.
//
// Adding a new entity type is a one-line constant here plus a registry entry in
// the owning service — no schema migration of relationship/event tables.
package entity

// Type is the class of an addressable entity. Its string value is stable and
// stored verbatim (e.g. as an event's anchor_type), so values must not change.
type Type string

const (
	TypeDevice        Type = "device"
	TypeDeviceGroup   Type = "devicegroup"
	TypeAsset         Type = "asset"
	TypeAssetGroup    Type = "assetgroup"
	TypeArea          Type = "area"
	TypeAreaGroup     Type = "areagroup"
	TypeCustomer      Type = "customer"
	TypeCustomerGroup Type = "customergroup"
)

// All is every recognized entity type, in a stable order.
var All = []Type{
	TypeDevice, TypeDeviceGroup,
	TypeAsset, TypeAssetGroup,
	TypeArea, TypeAreaGroup,
	TypeCustomer, TypeCustomerGroup,
}

// Valid reports whether t is a recognized entity type.
func (t Type) Valid() bool {
	switch t {
	case TypeDevice, TypeDeviceGroup, TypeAsset, TypeAssetGroup,
		TypeArea, TypeAreaGroup, TypeCustomer, TypeCustomerGroup:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t Type) String() string { return string(t) }
