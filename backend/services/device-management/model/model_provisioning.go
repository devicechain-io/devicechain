// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// ProvisioningStrategy decides whether a self-registration request may bring a
// brand-new device into existence or must match a device the operator
// pre-registered (ADR-012 provisioning policy). The two strategies are
// allow-new vs. check-pre-provisioned.
type ProvisioningStrategy string

const (
	// ProvisionAllowNew creates a device on first contact when none yet exists for
	// the presented token. Convenient for open fleets that self-register.
	ProvisionAllowNew ProvisioningStrategy = "ALLOW_NEW"
	// ProvisionCheckPreProvisioned rejects any token the operator has not already
	// registered. The locked-down posture: provisioning only mints credentials for
	// devices that were provisioned out of band.
	ProvisionCheckPreProvisioned ProvisioningStrategy = "CHECK_PRE_PROVISIONED"
)

// Valid reports whether the strategy names one of the known strategies.
func (s ProvisioningStrategy) Valid() bool {
	switch s {
	case ProvisionAllowNew, ProvisionCheckPreProvisioned:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (s ProvisioningStrategy) String() string {
	return string(s)
}

// Data required to create a provisioning profile.
type ProvisioningProfileCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ProvisionKey    string
	ProvisionSecret string
	Strategy        string
	DeviceTypeToken string
	// CredentialType is the credential type minted for a provisioned device.
	// Optional; defaults to ACCESS_TOKEN, the only type provisioning mints today
	// (minting MQTT_BASIC / X509 credentials is a later onboarding slice).
	CredentialType *string
	Enabled        bool
	ExpiresAt      *string
	Metadata       *string
}

// A PARTIAL update to a provisioning profile: omit a field to leave the stored value
// alone, send null to clear a nullable one, send a value to set it. There is no Token —
// the profile is identified by the mutation's `token` argument.
//
// 🔴 ProvisionSecret is the field this conversion protects. Under the full-replace shape
// an edit that changed nothing but the NAME had to restate the shared secret the whole
// fleet presents, and one that did not blanked it — after which every device in that
// fleet failed to self-register, with a 200 on the edit that broke them.
//
// # 🔴 credentialType is absent, and the reason is the same one that removes an immutable field
//
// A provisioning profile records which credential type it mints, and today
// provisionableCredentialType admits exactly one value: ACCESS_TOKEN. Create refuses
// anything else, so every stored value is ACCESS_TOKEN, so an update naming the field can
// only restate what is there (a no-op) or name something the check refuses (an error).
// That is the same shape as EntityGroup's memberType, and it gets the same answer: the
// field is not in the input.
//
// The difference from memberType is that this one is a CURRENT limit rather than an
// identity property — minting MQTT_BASIC or X509 is a later onboarding slice — and adding
// an optional field back to an input is additive. So this is a removal that reverses
// cleanly on the day a second type becomes mintable, not a decision about the entity.
//
// The full-replace shape it replaces did something worse than any of that: it read a nil
// CredentialType as "use the create-time default", so an edit that changed only the name
// RESET the credential type of every device the profile went on to provision.
type ProvisioningProfileUpdateRequest struct {
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// ProvisionKey and ProvisionSecret are the credentials a fleet presents, both on
	// NOT NULL columns: a blank either side authenticates nothing, so both refuse a null.
	ProvisionKey    dcgraphql.OptionalString
	ProvisionSecret dcgraphql.OptionalString
	// Strategy is a NOT NULL vocabulary column.
	Strategy dcgraphql.OptionalString
	// DeviceTypeToken is stamped onto auto-created devices; the FK is NOT NULL, so a
	// null is refused and an unknown token refuses the whole update.
	DeviceTypeToken dcgraphql.OptionalString
	Enabled         dcgraphql.OptionalBool
	// ExpiresAt is an RFC3339 timestamp on a nullable column: null (or an empty string)
	// means "never expires".
	ExpiresAt dcgraphql.OptionalString
	Metadata  dcgraphql.OptionalString
}

// ProvisioningProfile carries the shared key+secret a fleet presents to
// self-register, the strategy that governs whether unknown devices may be
// created, and the device type stamped onto auto-created devices (ADR-012). It
// is the provisioning-policy home that the ADR-012 Device Profile evolution
// ultimately folds in; until that lands it is a standalone entity resolved by
// its ProvisionKey.
//
// ProvisionSecret is stored as-is and verified with a constant-time compare,
// mirroring the DeviceCredential secret posture (ADR-014); hashing both secret
// stores is a future cross-cutting decision, not a provisioning-only one.
type ProvisioningProfile struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	ProvisionKey    string
	ProvisionSecret string
	Strategy        string
	DeviceTypeId    uint
	DeviceType      *DeviceType
	CredentialType  string
	Enabled         bool
	ExpiresAt       sql.NullTime
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Note this is NOT ordered by ExpiresAt the way DeviceCredential
// is — nothing reuses a provisioning profile off the front of a page; a profile is
// selected by its ProvisionKey, never by position in a list.
func (ProvisioningProfile) DefaultOrder() string {
	return "provisioning_profiles.created_at DESC, provisioning_profiles.token ASC"
}

// Search criteria for locating provisioning profiles.
type ProvisioningProfileSearchCriteria struct {
	rdb.Pagination
	DeviceType *string
	Strategy   *string
	Enabled    *bool
}

// Results for provisioning profile search.
type ProvisioningProfileSearchResults struct {
	Results    []ProvisioningProfile
	Pagination rdb.SearchResultsPagination
}

// ProvisionDeviceRequest is what a connecting device presents to self-register:
// the fleet's provision key+secret plus the identity it claims. The transport
// that carries this (a later onboarding slice) sets the tenant on the context;
// the request itself never names a tenant.
type ProvisionDeviceRequest struct {
	ProvisionKey    string
	ProvisionSecret string
	DeviceToken     string
	Name            *string
	Metadata        *string
}

// ProvisionDeviceResult is returned to a successfully provisioned device: its
// resolved identity plus the credential it must authenticate with going forward.
// CredentialValue is set only for credential types that carry a secret; for an
// ACCESS_TOKEN the bearer secret is the CredentialId itself.
type ProvisionDeviceResult struct {
	Device          *Device
	CredentialType  string
	CredentialId    string
	CredentialValue *string
	// Created reports whether this call brought the device into existence (true)
	// or resolved an already-registered one (false).
	Created bool
}
