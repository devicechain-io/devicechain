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
	TypeDevice   Type = "device"
	TypeAsset    Type = "asset"
	TypeArea     Type = "area"
	TypeCustomer Type = "customer"
	// TypeGroup is the single, member-family-agnostic group type (ADR-061). It
	// replaces the former per-family group tokens (devicegroup/assetgroup/
	// areagroup/customergroup): a group's member family is now a column on the
	// uniform EntityGroup, not four distinct entity identities.
	TypeGroup Type = "group"
)

// All is every recognized entity type, in a stable order.
var All = []Type{
	TypeDevice,
	TypeAsset,
	TypeArea,
	TypeCustomer,
	TypeGroup,
}

// Valid reports whether t is a recognized entity type.
func (t Type) Valid() bool {
	switch t {
	case TypeDevice, TypeAsset, TypeArea, TypeCustomer, TypeGroup:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t Type) String() string { return string(t) }
