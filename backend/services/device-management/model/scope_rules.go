// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// The scope rules of this service's models (ADR-081): how a member whose access is limited
// to part of the tenant — a customer user — may read each of them. core/rdb's scope
// callback applies a model's rule beside the tenant predicate, and REFUSES a scoped read
// of every tenant-scoped model that declares none. So this file is the whole list of what
// a scoped caller can reach in device-management's store, and everything absent from it —
// relationships, group memberships and versions, rules, geofences, credentials,
// attributes, provisioning, the audit journal — is refused at the model, whatever a
// resolver does.
//
// Four kinds, by what decides visibility:
//
//   - the DEVICE row is visible while the caller's scope currently owns it: an edge of a
//     TRACKED relationship type from the device to a customer the scope names
//     (rdb.DeviceEdge). Keyed on the tracked flag, not on a type token: "assigned" is only
//     the default, and any tracked type to a customer is ownership. It is visible, too,
//     while it is a member of an access-scope group the scope names, at the group's
//     published version (the rule's group branch, api_group_access.go);
//   - an AREA or ASSET row is visible while a device the caller can currently see has an edge
//     of a TRACKED relationship type to it (rdb.ReachedFromDevice, ADR-081 decision 12). It
//     is derived from the device rule, never a rule of its own: a device that moves or is
//     sold takes the visibility with it on the next read. An untracked edge, or a second
//     hop (device -> asset -> customer), reaches nothing. The relationship graph itself
//     stays closed - it would name co-owners - and so do the rows' writes;
//   - a device-derived row (an alarm) carries the anchors its device had when it was
//     written (rdb.AnchorColumn), so it keeps the owner it had then;
//   - tenant configuration a customer's console needs to render what it may see — device
//     types, profiles, their published versions and vocabularies, customer, area and asset
//     types — is readable unfiltered (rdb.Catalogue), and never writable.

// deviceOwnership is the ownership graph the Device rule reads: this service's
// relationship edges and their types.
var deviceOwnership = rdb.OwnershipEdges{
	Edge: &EntityRelationship{}, Type: &EntityRelationshipType{},
	SourceType: "source_type", SourceID: "source_id", SourceKind: string(entity.TypeDevice), Row: "id",
	TargetType: "target_type", TargetToken: "target_token",
	EdgeType: "relationship_type_id", TypeID: "id", Tracked: "tracked", TypeToken: "token",
	Groups: &rdb.GroupMembers{
		Kind:   auth.AnchorKindGroup,
		Member: &EntityGroupMembership{}, Group: &EntityGroup{},
		MemberType: "entity_type", MemberID: "entity_id", MemberKind: string(entity.TypeDevice),
		MemberGroup: "group_id", MemberVersion: "selector_version",
		GroupID: "id", GroupToken: "token", GroupVersion: "active_version", GroupEnabled: "access_scope_at",
	},
}

// ScopedBy: a scoped caller sees the devices its scope currently owns.
func (Device) ScopedBy() rdb.ScopeRule { return rdb.DeviceEdge(deviceOwnership) }

// ScopedBy: an alarm carries its device's anchors from when it was raised; a scoped caller
// may acknowledge and clear the alarms it can see, and never rewrite the stamp.
func (Alarm) ScopedBy() rdb.ScopeRule {
	return rdb.AnchorColumn("scope_anchors", rdb.AllowScopedUpdate)
}

// ScopedBy: a customer member reads its own customer record, and no other.
func (Customer) ScopedBy() rdb.ScopeRule {
	return rdb.NamedAnchor(string(entity.TypeCustomer), "token")
}

// ScopedBy: a member limited to a group reads that group's own record — so its console can
// name what it is limited to — and no other group. Nothing the record leads to opens with
// it: its members and versions are gated on Authorize, not AuthorizeScoped, and the
// membership and version rows declare no rule, so the store refuses them too.
func (EntityGroup) ScopedBy() rdb.ScopeRule {
	return rdb.NamedAnchor(string(entity.TypeGroup), "token")
}

// ScopedBy: a scoped caller sees the areas its visible devices are in. The resolver nulls the
// row's metadata (operator free-form JSON) for a scoped caller; see AreaResolver.Metadata.
func (Area) ScopedBy() rdb.ScopeRule {
	return rdb.ReachedFromDevice(deviceOwnership, &Device{}, string(entity.TypeArea), "token")
}

// ScopedBy: a scoped caller sees the assets its visible devices are assigned to (one hop; an
// asset assigned to a customer does not make the devices of that asset visible, nor the
// reverse). The resolver nulls its metadata and property document for a scoped caller.
func (Asset) ScopedBy() rdb.ScopeRule {
	return rdb.ReachedFromDevice(deviceOwnership, &Device{}, string(entity.TypeAsset), "token")
}

// The catalogue: configuration, not device-derived.

func (DeviceType) ScopedBy() rdb.ScopeRule              { return rdb.Catalogue() }
func (DeviceProfile) ScopedBy() rdb.ScopeRule           { return rdb.Catalogue() }
func (DeviceProfileVersion) ScopedBy() rdb.ScopeRule    { return rdb.Catalogue() }
func (DeviceProfileActivation) ScopedBy() rdb.ScopeRule { return rdb.Catalogue() }
func (MetricDefinition) ScopedBy() rdb.ScopeRule        { return rdb.Catalogue() }
func (CommandDefinition) ScopedBy() rdb.ScopeRule       { return rdb.Catalogue() }
func (CustomerType) ScopedBy() rdb.ScopeRule            { return rdb.Catalogue() }
func (AreaType) ScopedBy() rdb.ScopeRule                { return rdb.Catalogue() }
func (AssetType) ScopedBy() rdb.ScopeRule               { return rdb.Catalogue() }
