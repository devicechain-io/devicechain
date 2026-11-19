// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"database/sql"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Credential type vocabulary (ADR-014). Pluggable: new types (LWM2M, DID) add
// no Device-schema churn.
type CredentialType string

const (
	CredentialAccessToken CredentialType = "ACCESS_TOKEN"
	CredentialMqttBasic   CredentialType = "MQTT_BASIC"

	// CredentialX509Certificate is a client certificate pinned by the SHA-256 of its leaf DER
	// and bound to the trust anchor its chain must reach. Unlike the other two types it is NOT
	// presented by id: the id is a public value (the pin), so a connect that offered it as a
	// bearer would authenticate on a readable string. The device proves possession of the
	// certificate's key in the TLS handshake instead, and the chain is verified at every
	// connect (core x509trust). It is therefore created only by CreateDeviceCredential, which
	// computes the pin from the certificate, and PresentedById is false for it.
	CredentialX509Certificate CredentialType = "X509_CERTIFICATE"
)

// Valid reports whether the credential type names one of the supported types.
func (t CredentialType) Valid() bool {
	switch t {
	case CredentialAccessToken, CredentialMqttBasic, CredentialX509Certificate:
		return true
	default:
		return false
	}
}

// PresentedById reports whether a device authenticates with this type by presenting its
// credential id (an access token, an MQTT username), as opposed to proving possession of a
// key. It is the gate every id-based authentication path applies BEFORE it looks anything up:
// a certificate's pin is public, so a path that treated it like an id would authenticate on
// a value anyone who has seen the certificate can type.
func (t CredentialType) PresentedById() bool {
	return t == CredentialAccessToken || t == CredentialMqttBasic
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
	// CertificatePem and TrustAnchorToken create an X509_CERTIFICATE credential: the device's
	// certificate (optionally followed by the intermediates that link it to the anchor) and
	// the token of the tenant trust anchor its chain must reach. The credentialId is computed
	// from the certificate and a caller-supplied one is refused; the expiry is the chain's.
	// Both are refused for any other type.
	CertificatePem   *string
	TrustAnchorToken *string
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
//
// 🔴 THIS IS THE ROW WITHOUT ITS DEVICE. The belongs-to association (the Device a credential
// resolves to) is the registry's, and this package imports nothing of the registry, so
// model.DeviceCredential embeds this struct and adds the Device field beside it. GORM flattens
// the embedding, so the table, the columns and their order are exactly those of the struct
// before the carve, and every field here reads as a field of model.DeviceCredential.
type DeviceCredential struct {
	gorm.Model
	rdb.TenantScoped
	rdb.TokenReference
	rdb.MetadataEntity

	DeviceId       uint
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
	// TrustAnchorId is the tenant trust anchor an X509_CERTIFICATE credential's chain must reach
	// at every connect, nil for any other type (and for a certificate credential stored before
	// anchors existed, which therefore never authenticates). ON DELETE RESTRICT.
	TrustAnchorId *uint
	// CertificatePem is the pinned leaf certificate of an X509_CERTIFICATE credential, for
	// display and audit. It is public; nothing authenticates from it (the pin in CredentialId
	// and the presented chain do).
	CertificatePem sql.NullString
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
