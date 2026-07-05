// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Data required to create a device type.
type DeviceTypeCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	// ProfileToken references the DeviceProfile this type adopts (ADR-045).
	// Optional: a type with no profile is valid, it just grants its devices no
	// typed capability. Empty/omitted clears the reference.
	ProfileToken *string
	// Manufacturer + Model are identity facets (ADR-045 decision 8): they name
	// the device this type is, and stay correct even when many types share one
	// profile. Discovery facets, free-text (a curatable suggestion list backs the
	// authoring UI later).
	Manufacturer *string
	Model        *string
	Metadata     *string
}

// Represents a device type — the taxonomy/identity of a device (name, appearance,
// manufacturer/model), classifying its devices and referencing the DeviceProfile
// (ADR-045). The metric/command/alarm definitions still hang off the type in this
// slice; they relocate onto the referenced profile in a later slice, at which point
// the profile becomes the capability contract the type resolves through.
type DeviceType struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity

	// ProfileId is the nullable reference to the adopted DeviceProfile (ADR-045);
	// capability resolves device → type → profile. Nil means no profile adopted.
	ProfileId *uint `gorm:"index"`
	Profile   *DeviceProfile

	Manufacturer sql.NullString `gorm:"size:128"` // identity facet (ADR-045 decision 8)
	// ModelName is the device model facet; the Go field avoids colliding with the
	// embedded gorm.Model, the DB column + GraphQL field stay "model".
	ModelName sql.NullString `gorm:"column:model;size:128"`

	Devices            []Device
	MetricDefinitions  []MetricDefinition
	CommandDefinitions []CommandDefinition
	AlarmDefinitions   []AlarmDefinition
}

// Search criteria for locating device types.
type DeviceTypeSearchCriteria struct {
	rdb.Pagination
}

// Results for device type search.
type DeviceTypeSearchResults struct {
	Results    []DeviceType
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device.
type DeviceCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	DeviceTypeToken string
	Metadata        *string
}

// Represents a device.
type Device struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeId uint
	DeviceType   *DeviceType
}

// Search criteria for locating devices.
type DeviceSearchCriteria struct {
	rdb.Pagination
	DeviceType *string
}

// Results for device search.
type DeviceSearchResults struct {
	Results    []Device
	Pagination rdb.SearchResultsPagination
}

// Data required to create a device group.
type DeviceGroupCreateRequest struct {
	Token           string
	Name            *string
	Description     *string
	ImageUrl        *string
	Icon            *string
	BackgroundColor *string
	ForegroundColor *string
	BorderColor     *string
	Metadata        *string
}

// Represents a group of devices.
type DeviceGroup struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.NamedEntity
	rdb.BrandedEntity
	rdb.MetadataEntity
}

// Search criteria for locating device groups.
type DeviceGroupSearchCriteria struct {
	rdb.Pagination
}

// Results for device group search.
type DeviceGroupSearchResults struct {
	Results    []DeviceGroup
	Pagination rdb.SearchResultsPagination
}

// Credential type vocabulary (ADR-014). Pluggable: new types (LWM2M, DID) add
// no Device-schema churn.
type CredentialType string

const (
	CredentialAccessToken     CredentialType = "ACCESS_TOKEN"
	CredentialX509Certificate CredentialType = "X509_CERTIFICATE"
	CredentialMqttBasic       CredentialType = "MQTT_BASIC"
)

// Valid reports whether the credential type names one of the known types.
func (t CredentialType) Valid() bool {
	switch t {
	case CredentialAccessToken, CredentialX509Certificate, CredentialMqttBasic:
		return true
	default:
		return false
	}
}

// String returns the underlying string value.
func (t CredentialType) String() string {
	return string(t)
}

// Data required to create a device credential.
type DeviceCredentialCreateRequest struct {
	Token           string
	DeviceToken     string
	CredentialType  string
	CredentialId    string
	CredentialValue *string
	Enabled         bool
	ExpiresAt       *string
	Metadata        *string
}

// DeviceCredential holds authentication material for a device (ADR-014).
// Identity (Device) is stable and never rotates; credentials are rotatable and
// a device may hold several. CredentialId is the identifier a device presents
// at connect time (access token string, X.509 cert thumbprint/CN, or MQTT
// username); it resolves to the owning device. CredentialValue is the secret
// material (token secret, MQTT password, or certificate PEM).
type DeviceCredential struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	DeviceId        uint
	Device          *Device
	CredentialType  string
	CredentialId    string
	CredentialValue sql.NullString
	Enabled         bool
	ExpiresAt       sql.NullTime
}

// Search criteria for locating device credentials.
type DeviceCredentialSearchCriteria struct {
	rdb.Pagination
	Device         *string
	CredentialType *string
	CredentialId   *string
	Enabled        *bool
}

// Results for device credential search.
type DeviceCredentialSearchResults struct {
	Results    []DeviceCredential
	Pagination rdb.SearchResultsPagination
}
