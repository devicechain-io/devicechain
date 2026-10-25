// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"errors"
	"fmt"

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
	// MaintenanceWindowToken assigns the maintenance window that gates this type's
	// DISRUPTIVE and DESTRUCTIVE commands. Optional: a type with no window dispatches them
	// whenever the device is reachable. Empty/omitted leaves it unassigned.
	MaintenanceWindowToken *string
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
	// MaintenanceWindowToken re-points the maintenance window. Omitted keeps the current one,
	// null (or blank) unassigns it, an unknown token is refused.
	MaintenanceWindowToken dcgraphql.OptionalString
	Manufacturer           dcgraphql.OptionalString
	Model                  dcgraphql.OptionalString
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

	// MaintenanceWindowId is the nullable reference to the maintenance window that holds this
	// type's disruptive commands until it opens. Nil means the type has no window.
	MaintenanceWindowId *uint `gorm:"index"`
	MaintenanceWindow   *MaintenanceWindow

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
	// Enabled creates the device already disabled when false. Omitted or null is true:
	// a new device takes part in ingest and commanding unless the caller says otherwise.
	Enabled *bool
	// GatewayToken, when set, creates the device already assigned to that gateway device.
	// The same rules as AssignDeviceGateway apply, and a refusal fails the create.
	GatewayToken *string
	// ProvisioningProfileToken EARMARKS the device for a provisioning profile: the profile may
	// later adopt this device when it registers itself, and no other profile may. The profile
	// must exist in the tenant and be of the device's own type, otherwise the create is refused
	// with INVALID_VALUE. An earmark authorizes nothing by itself; the profile's secret does.
	ProvisioningProfileToken *string
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
	// Enabled disables (false) or re-enables (true) the device. Omitted leaves it as it
	// is. An explicit NULL IS REFUSED rather than read as false: a null that quietly
	// quarantined a device would cut it off from ingest and commanding with a 200.
	Enabled dcgraphql.OptionalBool
}

// ErrDeviceDisabled is the refusal for a device an operator has disabled: its events are
// refused at resolution (all but the platform's own presence transitions, which report the
// connection it already had), its broker connects are refused, and commands to it are refused
// at enqueue. It is permanent for as long as the device stays disabled, so nothing that
// receives it should retry.
var ErrDeviceDisabled = errors.New("the device is disabled")

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

	// ExpectedSince is when the device's membership of its current profile began — the base
	// of the detection engine's never-reported dead-man clock. A re-type of the device, or a
	// re-point of its type onto another profile, stamps it; NULL means "since the device was
	// created". It is never read directly: rosterEntries is the one reader, and it resolves
	// NULL to created_at, so the roster facts this service emits and the roster page the
	// engine reconciles from cannot disagree about it.
	ExpectedSince sql.NullTime

	// DisabledAt is when an operator disabled the device; NULL means it is enabled. A
	// disabled device's events are refused at resolution (all but its presence
	// transitions, which the platform's transports report and detection skips), its broker
	// connects are refused at the auth callout, and commands to it are refused at enqueue. It is
	// surfaced as `enabled` (DisabledAt NULL); the timestamp says since when.
	//
	// 🔴 THE STORED FORM IS "DISABLED SINCE", NOT AN `enabled` BOOLEAN, BECAUSE OF WHAT A
	// ZERO VALUE MEANS. Devices are read through narrow column lists (the credential
	// lookups' allowlists) and through the device-by-token key-value bucket, whose entries
	// are JSON written by whichever replica filled them — during a rolling upgrade, by a
	// replica that has never heard of this field. An `Enabled bool` would decode as false
	// from every such entry and refuse the whole fleet's events until each entry expired.
	// A NULL timestamp decodes as "enabled", which is what every device was before. The
	// price is the opposite direction: a narrow read that forgot this column would read a
	// disabled device as enabled, which is why the credential lookups' column lists carry
	// it and a test pins both (TestADisabledDeviceIsRefusedThroughEveryLookup).
	//
	// Nullable with no default, for the reason migration_device_disabled.go records.
	DisabledAt sql.NullTime

	// GatewayId is the id of the GATEWAY device that fronts this one: the only device whose
	// broker login may publish on this device's behalf (on its own gateway namespace), and
	// nil for a device that is not behind one. It is the single authority for that: the
	// resolver accepts a gateway's publish for this device only when this names the
	// publishing gateway (FailureReason NotGatewayChild otherwise), so it is written by
	// AssignDeviceGateway / ClearDeviceGateway and by nothing else.
	//
	// SINGLE-VALUED AND ONE LEVEL DEEP. A device has at most one gateway, so moving it is one
	// atomic write, and a gateway cannot itself have a gateway. It is a plain nullable
	// column with no foreign key, like the other same-service references here: DeleteDevice
	// refuses to delete a gateway that still has children (ErrGatewayHasChildren), which is
	// the rule a constraint would have enforced, with a sentence that names it.
	//
	// 🔴 NULL MUST MEAN "NO GATEWAY", AND IT DOES FOR THE DEVICE-BY-TOKEN CACHE: an entry
	// written by a replica that predates this field decodes with it nil, so a rolling upgrade
	// reads every cached device as having no gateway, and a gateway's publish for it is
	// REFUSED. That is the safe direction, and no assignment exists before the upgrade anyway.
	GatewayId *uint

	// ExpectedProvisioningProfileId is the EARMARK: the provisioning profile that may adopt
	// this device when it registers itself, and the only one. nil means no profile may. It is
	// a fact about the device and never authorizes anything by itself: the profile's secret
	// does, and a profile also refuses a device earmarked for another profile, one of a
	// different type, and one that already holds a credential.
	//
	// ON DELETE SET NULL (the migration's foreign key): deleting a profile returns its
	// earmarked devices to "no earmark" rather than blocking on them. Written by a create or
	// an import naming a provisioning profile, and by the profile itself when it creates a
	// device; nothing else sets it.
	ExpectedProvisioningProfileId *uint

	// metadataUnread marks a row read WITHOUT its metadata column: the narrow credential
	// lookups' (authDeviceFields, connectDeviceFields). On such a row a nil Metadata means
	// "not read", not "none stored". The event resolver once took a credential-authenticated
	// device's (always nil) Metadata for its stored constants, so every formula reading
	// `device.<key>` was missing_input on every credentialed event. Unexported, so neither
	// gorm nor JSON sees it; the zero value is "read", which every full read and every
	// decoded cache entry is. See MetadataRead.
	metadataUnread bool

	// ConnectCredentialExpiresAt is the expiry of the credential an MQTT CONNECT was just
	// authenticated with, carried to the auth callout so it can clamp the session's JWT to it.
	// It is TRANSIENT: set only on the copy AuthenticateDeviceConnect and ResolveDeviceCredential
	// return, never stored (gorm:"-"), never serialized (the device-by-token bucket holds this
	// struct as JSON), and zero on every other read. A session must not outlive the credential
	// that opened it, and the callout is the only place that mints the session's lifetime.
	ConnectCredentialExpiresAt sql.NullTime `gorm:"-" json:"-"`
}

// MetadataRead reports whether the row's Metadata is what the device stores. It is false
// for a row from a narrow credential lookup, whose metadata column was never read: a
// caller that needs the metadata reads the device in full (DevicesByToken) instead.
func (d *Device) MetadataRead() bool {
	return d != nil && !d.metadataUnread
}

// IsDisabled reports whether an operator has disabled the device.
func (d *Device) IsDisabled() bool {
	return d != nil && d.DisabledAt.Valid
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
	// ProvisioningProfile selects the devices that hold a credential issued by the provisioning
	// profile with this token (disabled and expired ones included). An unknown token matches
	// nothing.
	ProvisioningProfile *string
	// ExpectedProvisioningProfile selects the devices earmarked for the provisioning profile
	// with this token. An unknown token matches nothing.
	ExpectedProvisioningProfile *string
}

// Search criteria for the devices of one customer: those with a live edge of one
// relationship type from the device to the customer.
type CustomerDeviceSearchCriteria struct {
	rdb.Pagination
	// Customer is the customer's token.
	Customer string
	// RelationshipType is the token of the edge's relationship type.
	RelationshipType string
	// OwnershipOnly additionally requires that type to be TRACKED, so the edge is one of
	// ownership. The resolver sets it for a caller limited to part of the tenant; it is not
	// part of the GraphQL input.
	OwnershipOnly bool
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
	CredentialAccessToken CredentialType = "ACCESS_TOKEN"
	CredentialMqttBasic   CredentialType = "MQTT_BASIC"

	// CredentialX509Certificate is RETIRED, not part of the vocabulary: it is not accepted
	// until certificate verification ships, so the type is refused at create and update
	// (typed UNSUPPORTED) and at authentication (a stored row can never authenticate). The
	// constant survives only so that refusal can name it, and so a test can seed a stored
	// row. It returns to Valid() when certificate verification ships.
	CredentialX509Certificate CredentialType = "X509_CERTIFICATE"
)

// Valid reports whether the credential type names one of the supported types.
func (t CredentialType) Valid() bool {
	switch t {
	case CredentialAccessToken, CredentialMqttBasic:
		return true
	default:
		return false
	}
}

// Retired reports whether the type is a recognized but unsupported member of the
// vocabulary: refused with a typed code rather than as an unknown type.
func (t CredentialType) Retired() bool {
	return t == CredentialX509Certificate
}

// UnsupportedCredentialTypeError is the typed refusal of a retired credential type. It
// carries extensions.code UNSUPPORTED, and must reach graphql-go UNWRAPPED for the code to
// be served (see UnsupportedCredentialTypeOf).
type UnsupportedCredentialTypeError struct {
	Type CredentialType
}

func (e *UnsupportedCredentialTypeError) Error() string {
	return fmt.Sprintf("credential type %s is not supported until certificate verification ships; use ACCESS_TOKEN or MQTT_BASIC", e.Type)
}

// Extensions gives the refusal its wire code.
func (e *UnsupportedCredentialTypeError) Extensions() map[string]any {
	return map[string]any{"code": "UNSUPPORTED"}
}

// UnsupportedCredentialTypeOf returns the typed refusal found anywhere in err's chain, or
// err unchanged. A resolver returns its result so the code reaches the wire.
func UnsupportedCredentialTypeOf(err error) error {
	var unsupported *UnsupportedCredentialTypeError
	if errors.As(err, &unsupported) {
		return unsupported
	}
	return err
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
	// carries no secret is a real state — so an explicit null (or "") clears it. Omitting
	// it leaves the secret in place, which is what makes a metadata edit safe. A value is
	// digested EXACTLY AS SENT, surrounding whitespace included, because a device presents
	// it byte for byte.
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
// at connect time (access token string or MQTT
// username); it resolves to the owning device.
//
// 🔴 THE SECRET IS NEVER STORED, ONLY ITS KEYED DIGEST. SecretDigest is what
// credential.DeviceSecretKey made of the secret a create or update carried (an MQTT
// password), and NULL for a credential with none. Nothing that reads this row can
// recover the secret, and the cache that copies the row copies the digest. The
// credential_value column that held the plaintext is still in the table, NULLed by
// DigestPlaintextCredentialSecrets at startup and by every write (LegacyCredentialValue).
type DeviceCredential struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	DeviceId       uint
	Device         *Device
	CredentialType string
	// ProvisioningProfileId is the provisioning profile that ISSUED this credential, or nil for
	// one an operator created. It is the provenance the profile's cap counts, its delete is
	// refused over, and a profile's revoke acts on. Only the self-registration operation sets
	// it, and a credential update never changes it.
	//
	// ON DELETE RESTRICT (the migration's foreign key): the API refuses the profile's delete with
	// a readable sentence, and the constraint is the backstop that turns a missed check into a
	// refused delete instead of a dangling id.
	ProvisioningProfileId *uint
	// CredentialId is what the credential is looked up by. For MQTT_BASIC it is the
	// username as sent. For ACCESS_TOKEN it is the keyed digest of the token, never the
	// token: the token is a bearer secret, and a copy of the database must not hold one.
	CredentialId string
	// IssuedCredentialId is the access token as sent, on the row a create or update just
	// wrote, and "" on every row read from the database. It is not a column. The response
	// to the write that set a token shows it, once.
	IssuedCredentialId string `gorm:"-"`
	SecretDigest       sql.NullString
	Enabled            bool
	ExpiresAt          sql.NullTime

	// LegacyCredentialValue is the retired plaintext column, mapped WRITE-ONLY so that
	// every create and save of a credential writes it NULL. Nothing reads it, and nothing
	// may set it.
	//
	// 🔴 IT IS LOAD-BEARING DURING A ROLLING UPGRADE. An old-version pod still writes a
	// rotated password into credential_value and leaves secret_digest alone. If a new pod
	// then rotated again without clearing that column, the next start's digest step would
	// digest the OLDER password over the newer one, and the password that was rotated away
	// would authenticate again. Clearing it on every write means the column only ever holds
	// a value an old pod wrote LAST, which is the one the step should keep.
	LegacyCredentialValue sql.NullString `gorm:"column:credential_value;->:false;<-"`
}

// DefaultOrder implements rdb.Sortable, and this is the one model here where the
// ordering is a BEHAVIOURAL choice rather than a stability one.
//
// A credential listing leads with the credential that has the MOST runway left:
// expires_at DESC NULLS FIRST puts a
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
