// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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
	// profile. Discovery facets, free-text; the authoring UI suggests values
	// already in use (FacetValues), with explicit curation left for later.
	Manufacturer *string
	Model        *string
	Metadata     *string
}

// DeviceTypeUpdateRequest is the PARTIAL-update counterpart of
// DeviceTypeCreateRequest, and the first entity converted to the platform-wide
// partial-update semantic.
//
// Every field is optional in the three-state sense the Optional* types carry:
// omitted leaves the stored value alone, an explicit null clears it, a value sets
// it. Sending {name: "Excavator"} renames the type and touches nothing else.
//
// The old shape reused DeviceTypeCreateRequest, which made an update a FULL
// REPLACE: a caller renaming a type wiped its imageUrl, icon, three colours,
// manufacturer, model, metadata and its adopted profile reference — successfully,
// with a 200 and the emptied entity returned. The console worked around it by
// reading the whole record and writing it back, which bought a second problem
// (two operators editing one type now clobber each other across every field);
// every other caller — dcctl, the SDKs, MCP, any direct API user — kept the footgun.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT from this struct, and that is a capability
// removal, not an oversight. The create request carries a Token and the old update
// path assigned it (`found.Token = request.Token`), so an update could MOVE a
// type's token. The token is already the mutation's own argument, so carrying it
// again in the payload only created a second, disagreeing source for the same
// identity. Leaving it out makes a token move unrepresentable rather than merely
// refused, which is the same line user-management admin already drew.
type DeviceTypeUpdateRequest struct {
	Name            dcgraphql.OptionalString
	Description     dcgraphql.OptionalString
	ImageUrl        dcgraphql.OptionalString
	Icon            dcgraphql.OptionalString
	BackgroundColor dcgraphql.OptionalString
	ForegroundColor dcgraphql.OptionalString
	BorderColor     dcgraphql.OptionalString
	// ProfileToken re-points the DeviceProfile this type adopts. Omitted leaves the
	// current profile in place — which the full-replace shape could not express, and
	// is why a rename used to silently un-declare position for every device built on
	// the type. Null (or an empty/whitespace token, preserved from the old behaviour)
	// detaches the profile. An unknown token is rejected.
	ProfileToken dcgraphql.OptionalString
	Manufacturer dcgraphql.OptionalString
	Model        dcgraphql.OptionalString
	// Metadata is an opaque JSON string in the schema, not a map, so it replaces
	// wholesale when sent and clears on null. There is no per-key merge to choose
	// between: the API has never been able to address an individual key.
	Metadata dcgraphql.OptionalString
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

	// Manufacturer + Model are indexed identity facets (ADR-045 decision 8): they
	// back the distinct-value suggestion lists and are meant to be filtered/grouped
	// on, so the index matches DeviceProfile.Category's.
	Manufacturer sql.NullString `gorm:"size:128;index"` // identity facet (ADR-045 decision 8)
	// ModelName is the device model facet; the Go field avoids colliding with the
	// embedded gorm.Model, the DB column + GraphQL field stay "model".
	ModelName sql.NullString `gorm:"column:model;size:128;index"`

	Devices []Device
	// The metric/command/alarm definitions moved onto DeviceProfile in ADR-045
	// slice b; a type reaches them through its profile.
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. Not ordered by Manufacturer/ModelName despite both being
// indexed — those are nullable discovery facets a caller FILTERS on, and a nullable
// leading key would need explicit NULLS placement to buy an order nobody asked for.
func (DeviceType) DefaultOrder() string {
	return "device_types.created_at DESC, device_types.token ASC"
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
	ExternalId      *string
	Name            *string
	Description     *string
	DeviceTypeToken string
	Metadata        *string
}

// MaxBulkDeviceCount bounds how many devices one CreateDevices call renders and
// creates in a single transaction. A cap is not optional: the whole batch is one
// DB transaction held open for the duration, so an unbounded count would let a
// single request pin locks and memory across the fleet. Larger fleets are created
// with repeated calls (bumping StartIndex).
const MaxBulkDeviceCount = 1000

// DeviceBulkCreateRequest is a templated bulk-provisioning request: render Count
// devices from the templates (indices StartIndex .. StartIndex+Count-1) and create
// them in one transaction. Templates use the core placeholder grammar — "{n}" /
// "{n:0Wd}" for the index, plus "{random}" in ExternalIdTemplate for a fresh random
// business id per device. TokenTemplate must carry an index placeholder so every
// device gets a distinct token.
type DeviceBulkCreateRequest struct {
	DeviceTypeToken    string
	Count              int32
	StartIndex         *int32
	TokenTemplate      string
	NameTemplate       *string
	ExternalIdTemplate *string
	Metadata           *string
}

// DeviceUpdateRequest is the PARTIAL-update counterpart of DeviceCreateRequest.
// Omitted leaves the stored value alone, an explicit null clears it, a value sets it.
//
// 🔴 TOKEN IS DELIBERATELY ABSENT, and here that closes a defect with a second
// symptom. The old path located the row by `request.Token` and ignored the
// mutation's own `token` argument entirely, so a caller who sent a `token` argument
// naming one device and a `request.token` naming another silently updated the
// SECOND and got a 200 for it — the mandatory argument was dead. It also meant a
// device rename was unreachable through this method, which is why the roster
// fan-out below could assume a stable device-token key. Both remain true of the new
// shape, but now by construction: the argument is the only identity channel, and a
// token move is unrepresentable rather than accidentally unreachable.
type DeviceUpdateRequest struct {
	ExternalId  dcgraphql.OptionalString
	Name        dcgraphql.OptionalString
	Description dcgraphql.OptionalString
	// DeviceTypeToken re-types the device. Omitted keeps the current type. An
	// explicit NULL IS REFUSED — device_type_id is NOT NULL, and a device with no
	// type resolves no capability at all. An unknown token is refused, totally.
	//
	// A re-type may move the device onto a different DeviceProfile, so this is the
	// field the post-commit roster fan-out watches.
	DeviceTypeToken dcgraphql.OptionalString
	Metadata        dcgraphql.OptionalString
}

// Represents a device.
type Device struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.ExternalReference
	rdb.NamedEntity
	rdb.MetadataEntity

	DeviceTypeId uint
	DeviceType   *DeviceType
}

// DefaultOrder implements rdb.Sortable with the registry default: newest first, token
// as the unique tiebreak. created_at is emphatically not total on this table — a bulk
// create renders up to MaxBulkDeviceCount devices inside ONE transaction, so a
// thousand rows can share a timestamp to the microsecond, and the token is the only
// thing keeping those thousand from reshuffling across page boundaries. Devices are
// also paged as a group member family, where the closure's own order leads and this
// one only tiebreaks.
func (Device) DefaultOrder() string {
	return "devices.created_at DESC, devices.token ASC"
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

// A PARTIAL update to a device credential: omit a field to leave the stored value
// alone, send null to clear a nullable one, send a value to set it. There is no Token —
// the credential is identified by the mutation's `token` argument.
//
// 🔴 THE THREE STATES MATTER MORE HERE THAN ANYWHERE ELSE IN THIS AREA, because the
// full-replace shape made every edit a re-authentication event. Renaming nothing but the
// metadata sent a request with no credentialValue, which BLANKED the secret the device
// presents at connect time; a request with no expiresAt removed the expiry; a request
// that failed to restate `enabled: true` disabled the credential. All three returned 200
// and took the device offline at its next reconnect.
type DeviceCredentialUpdateRequest struct {
	// DeviceToken re-points the credential at another device. The FK is NOT NULL, so a
	// null is refused and an unknown token refuses the whole update.
	DeviceToken dcgraphql.OptionalString
	// CredentialType and CredentialId are NOT NULL: a credential with no type has no
	// vocabulary and one with no id resolves to no device, so both refuse a null.
	CredentialType dcgraphql.OptionalString
	CredentialId   dcgraphql.OptionalString
	// CredentialValue is the secret material and IS nullable — a credential type that
	// carries no secret is a real state — so an explicit null clears it. Omitting it
	// leaves the secret in place, which is what makes a metadata edit safe.
	CredentialValue dcgraphql.OptionalString
	// Enabled sits on a NOT NULL column; a null is refused rather than folded to false,
	// which would silently disable the credential.
	Enabled dcgraphql.OptionalBool
	// ExpiresAt is an RFC3339 timestamp on a nullable column: null (or an empty string,
	// which is what a cleared form field sends) means "never expires".
	ExpiresAt dcgraphql.OptionalString
	Metadata  dcgraphql.OptionalString
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

// DefaultOrder implements rdb.Sortable, and this is the one model here where the
// ordering is a BEHAVIOURAL choice rather than a stability one.
//
// mintOrReuseCredential reads a device's live credentials unbounded and reuses
// whichever comes off the FRONT of the set, so whatever leads this clause is the
// credential every re-provision hands back. expires_at DESC NULLS FIRST puts a
// never-expiring credential first, then the one with the most runway left. The
// obvious id ASC would have done the opposite — handed back the credential CLOSEST
// to expiry, forcing the earliest possible re-provision.
//
// NULLS FIRST is stated rather than implied. Postgres already defaults DESC to NULLS
// FIRST, but SQLite — what the model tests run on — puts NULLs first either way;
// saying it makes the harness and production agree instead of differing silently in
// the direction where the tests still pass. expires_at is far from unique (a fleet
// provisioned together shares one), so id DESC is the total tiebreak.
func (DeviceCredential) DefaultOrder() string {
	return "device_credentials.expires_at DESC NULLS FIRST, device_credentials.id DESC"
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
