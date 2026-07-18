// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

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
