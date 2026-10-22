// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/sqlnull"
	"gorm.io/gorm"
)

// buildDeviceCredential validates a credential create request against an ALREADY
// RESOLVED owning device and renders the row to insert. It performs no I/O, so the
// whole of a credential's admission policy — the type vocabulary, the RFC3339
// expiry parse, the metadata JSON check — is decided before any transaction opens.
//
// It exists so ReplaceDevice (ADR-074) admits a credential by exactly the same
// rules as CreateDeviceCredential rather than by a second, quietly diverging copy.
// That mattered enough to refactor for: the replacement path has to insert its
// credential INSIDE the transaction that retires the outgoing ones, so it cannot
// simply call CreateDeviceCredential, and hand-inlining the four checks is how the
// two paths end up disagreeing about (say) whether "access_token" is a type.
//
// The row carries BOTH Device and DeviceId. The association is what
// CreateDeviceCredential has always written through; DeviceId is what a caller that
// omits the association (`Omit("Device")`, as the replacement transaction does)
// writes instead. Setting both means neither caller has to reach around this
// function to get a correct row.
func buildDeviceCredential(key *credential.DeviceSecretKey, device *Device, request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	// Validate credential type against the known vocabulary. A retired type is refused
	// with its own typed error, not as an unknown one.
	if CredentialType(request.CredentialType).Retired() {
		return nil, &UnsupportedCredentialTypeError{Type: CredentialType(request.CredentialType)}
	}
	if !CredentialType(request.CredentialType).Valid() {
		return nil, fmt.Errorf("invalid credential type: %s", request.CredentialType)
	}

	// Parse optional expiration timestamp (RFC3339).
	expiresAt := sql.NullTime{}
	if request.ExpiresAt != nil {
		parsed, err := time.Parse(time.RFC3339, *request.ExpiresAt)
		if err != nil {
			return nil, err
		}
		expiresAt = sql.NullTime{Time: parsed, Valid: true}
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	// A type that compares no secret stores none: a value sent with it is dropped, so it
	// cannot ride along into a later change of type.
	var secretDigest sql.NullString
	if credentialRequiresSecret(request.CredentialType) {
		if secretDigest, err = digestSecret(key, device.TenantId, request.CredentialValue, minSecretBytes(request.CredentialType)); err != nil {
			return nil, err
		}
	}
	// An ACCESS_TOKEN is stored as its keyed digest, never as the token. The row handed back
	// carries the token as sent in IssuedCredentialId, once, for the response to the write
	// that set it.
	storedId, issued, err := storedCredentialIdForWrite(key, device.TenantId, request.CredentialType, request.CredentialId)
	if err != nil {
		return nil, err
	}
	return &DeviceCredential{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: metadataJSON,
		},
		DeviceId:           device.ID,
		Device:             device,
		CredentialType:     request.CredentialType,
		CredentialId:       storedId,
		IssuedCredentialId: issued,
		SecretDigest:       secretDigest,
		Enabled:            request.Enabled,
		ExpiresAt:          expiresAt,
	}, nil
}

// ErrCredentialIdRequiredForType refuses a change of a credential's type to or from
// ACCESS_TOKEN that does not set its id in the same request. INVALID_VALUE.
//
// The stored id of an ACCESS_TOKEN is a digest of the token, and of any other type the id
// itself (an MQTT username). Carrying one over across the change would read a username as
// a digest, or a digest as a username, and the credential would authenticate nobody.
var ErrCredentialIdRequiredForType = integrity.NewRefusal(integrity.ClassInvalid,
	"changing a credential to or from ACCESS_TOKEN requires credentialId in the same request")

// accessTokenCredential reports whether ctype stores its id as a keyed digest of the token.
func accessTokenCredential(ctype string) bool {
	return CredentialType(ctype) == CredentialAccessToken
}

// storedCredentialId is the value a presented (type, id) is stored and looked up as in the
// credential_id column: the id itself, except for an ACCESS_TOKEN, whose id IS the bearer
// secret and is kept only as its keyed digest (credential.DeviceSecretKey.AccessTokenDigest).
// It is deterministic, which is what lets a lookup be an indexed equality on the stored
// value. A missing key refuses: there is nothing to digest or look up by.
func storedCredentialId(key *credential.DeviceSecretKey, tenant, ctype, id string) (string, error) {
	if !accessTokenCredential(ctype) {
		return id, nil
	}
	if key == nil {
		return "", errNoDeviceSecretKey
	}
	return key.AccessTokenDigest(tenant, id)
}

// storedCredentialIdForWrite is storedCredentialId for a create or update that sets the id,
// returning the token as sent as well when it is one, for the one response that shows it.
// An access token over credential.MaxAccessTokenBytes is refused with LIMIT_EXCEEDED, as
// the plaintext column refused it before.
func storedCredentialIdForWrite(key *credential.DeviceSecretKey, tenant, ctype, id string) (stored, issued string, err error) {
	if !accessTokenCredential(ctype) {
		return id, "", nil
	}
	if len(id) > credential.MaxAccessTokenBytes {
		return "", "", limit.Exceeded("credentialId bytes", len(id), credential.MaxAccessTokenBytes)
	}
	if id == "" {
		return "", "", errors.New("an ACCESS_TOKEN credential needs a credentialId")
	}
	stored, err = storedCredentialId(key, tenant, ctype, id)
	if err != nil {
		return "", "", err
	}
	return stored, id, nil
}

// tenantOf is the tenant of the request context, or an error: a credential lookup is
// tenant-scoped, and an access token digest is bound to the tenant.
func tenantOf(ctx context.Context) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return "", errors.New("a credential lookup needs a tenant in context")
	}
	return tenant, nil
}

// ErrCredentialSecretTooShort refuses an MQTT_BASIC secret shorter than
// credential.MinDeviceSecretBytes. It carries extensions.code INVALID_VALUE and names the
// floor, never the secret.
var ErrCredentialSecretTooShort = integrity.NewRefusal(integrity.ClassInvalid,
	fmt.Sprintf("credentialValue must be at least %d bytes for an MQTT_BASIC credential", credential.MinDeviceSecretBytes))

// ErrCredentialSecretRequiredForType refuses a change of a credential's type to MQTT_BASIC
// that does not set its password in the same request. INVALID_VALUE.
var ErrCredentialSecretRequiredForType = integrity.NewRefusal(integrity.ClassInvalid,
	"changing a credential to MQTT_BASIC requires credentialValue in the same request")

// minSecretBytes is the shortest secret a write may set on a credential of ctype: the
// floor for MQTT_BASIC, the only type that compares a stored secret, and none for the rest.
func minSecretBytes(ctype string) int {
	if credentialRequiresSecret(ctype) {
		return credential.MinDeviceSecretBytes
	}
	return 0
}

// errNoDeviceSecretKey refuses a write carrying a secret when no DeviceSecretKey is wired:
// there is nothing to digest it with, and storing it as sent is what this replaced.
var errNoDeviceSecretKey = errors.New("device credential secrets cannot be stored: no device secret key is configured")

// digestSecret is the stored form of a secret a create or update carried: NULL for none
// (nil or ""), and otherwise its keyed digest, bound to the credential's tenant. The secret is digested EXACTLY AS SENT: a
// device presents its password byte for byte, so trimming it here would store a digest
// of something no device sends.
//
// A secret longer than credential.MaxDeviceSecretBytes is refused with LIMIT_EXCEEDED. The
// plaintext column refused it before; the digest is fixed-width and would not.
//
// minBytes is the shortest secret this write may set: credential.MinDeviceSecretBytes for
// an MQTT_BASIC credential, refused with INVALID_VALUE (ErrCredentialSecretTooShort), and
// 0 where a stored secret is carried over rather than set (adoptLegacySecret), so a
// secret that predates the floor is never refused by a write that did not choose it.
func digestSecret(key *credential.DeviceSecretKey, tenant string, value *string, minBytes int) (sql.NullString, error) {
	plain := sqlnull.Secret(value)
	if !plain.Valid {
		return sql.NullString{}, nil
	}
	if len(plain.String) > credential.MaxDeviceSecretBytes {
		return sql.NullString{}, limit.Exceeded("credentialValue bytes", len(plain.String), credential.MaxDeviceSecretBytes)
	}
	if len(plain.String) < minBytes {
		return sql.NullString{}, ErrCredentialSecretTooShort
	}
	if key == nil {
		return sql.NullString{}, errNoDeviceSecretKey
	}
	digest, err := key.Digest(tenant, plain.String)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: digest, Valid: true}, nil
}

// Create a new device credential.
func (api *Api) CreateDeviceCredential(ctx context.Context, request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	matches, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	created, err := buildDeviceCredential(api.DeviceSecretKey, matches[0], request)
	if err != nil {
		return nil, err
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateDeviceCredential applies a PARTIAL update: a field the caller did not name keeps
// its stored value — including the SECRET, which the full-replace shape blanked on every
// edit that failed to restate it.
func (api *Api) UpdateDeviceCredential(ctx context.Context, token string,
	request *DeviceCredentialUpdateRequest) (*DeviceCredential, error) {
	matches, err := api.DeviceCredentialsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]
	// The device the credential belonged to before this update: a re-point must evict the
	// credential's cached copy under the device it is leaving as well as the one it joins.
	priorDeviceId := updated.DeviceId

	// Everything that can refuse resolves before anything is written, so a refused update
	// leaves the credential exactly as the device last authenticated with it.
	currentDeviceToken := ""
	if updated.Device != nil {
		currentDeviceToken = updated.Device.Token
	}
	repointTo, repoint, err := resolveRequiredTypeRef(request.DeviceToken, currentDeviceToken, "deviceToken")
	if err != nil {
		return nil, err
	}
	var device *Device
	if repoint {
		devices, err := api.DevicesByToken(ctx, []string{repointTo})
		if err != nil {
			return nil, err
		}
		if len(devices) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		device = devices[0]
	}
	credentialType, err := request.CredentialType.ApplyToRequired("credentialType", updated.CredentialType)
	if err != nil {
		return nil, err
	}
	// The vocabulary check runs only when the caller named the type: an absent field has
	// nothing to validate, and checking the stored value instead would refuse a metadata
	// edit over a type the caller never sent.
	if request.CredentialType.Set && CredentialType(credentialType).Retired() {
		return nil, &UnsupportedCredentialTypeError{Type: CredentialType(credentialType)}
	}
	if request.CredentialType.Set && !CredentialType(credentialType).Valid() {
		return nil, fmt.Errorf("invalid credential type: %s", credentialType)
	}
	credentialId, err := request.CredentialId.ApplyToRequired("credentialId", updated.CredentialId)
	if err != nil {
		return nil, err
	}
	// The id is stored as a digest for an ACCESS_TOKEN and as sent for any other type, so
	// it follows the type. An id that is sent is digested for its resulting type; one that is
	// not is kept, which a change to or from ACCESS_TOKEN cannot do.
	var issuedId string
	switch {
	case request.CredentialId.Set:
		if credentialId, issuedId, err = storedCredentialIdForWrite(api.DeviceSecretKey, updated.TenantId, credentialType, credentialId); err != nil {
			return nil, err
		}
	case accessTokenCredential(credentialType) != accessTokenCredential(updated.CredentialType):
		return nil, ErrCredentialIdRequiredForType
	}
	enabled, err := request.Enabled.ApplyToRequired("enabled", updated.Enabled)
	if err != nil {
		return nil, err
	}
	expiresAt, err := request.ExpiresAt.ApplyToNullTime("expiresAt", updated.ExpiresAt)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(updated.Metadata)))
	if err != nil {
		return nil, err
	}
	// Absent keeps the stored secret; null or "" clears it; a value replaces it with a
	// digest of the new secret.
	//
	// The secret follows the type. A type that compares none stores none. A change TO
	// MQTT_BASIC must carry a password in the same request, so no secret chosen under another
	// type (or none) becomes a password that skipped the minimum length.
	secretDigest := updated.SecretDigest
	switch {
	case !credentialRequiresSecret(credentialType):
		secretDigest = sql.NullString{}
	case !credentialRequiresSecret(updated.CredentialType) &&
		(!request.CredentialValue.Set || request.CredentialValue.Value == nil || *request.CredentialValue.Value == ""):
		return nil, ErrCredentialSecretRequiredForType
	case request.CredentialValue.Set:
		if secretDigest, err = digestSecret(api.DeviceSecretKey, updated.TenantId, request.CredentialValue.Value, minSecretBytes(credentialType)); err != nil {
			return nil, err
		}
	default:
		if secretDigest, err = api.adoptLegacySecret(ctx, updated); err != nil {
			return nil, err
		}
	}

	updated.Metadata = metadataJSON
	updated.CredentialType = credentialType
	updated.CredentialId = credentialId
	updated.IssuedCredentialId = issuedId
	updated.SecretDigest = secretDigest
	updated.Enabled = enabled
	updated.ExpiresAt = expiresAt
	if device != nil {
		updated.Device = device
		// Belt-and-braces, and said so rather than left to look load-bearing: gorm's Save
		// syncs a belongs-to FK from the association it is given, so a mutant deleting this
		// line is behaviour-equivalent and survives on purpose. It is set anyway so the
		// in-memory value handed back to the caller agrees with the row.
		updated.DeviceId = device.ID
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	// EVERY committed update evicts, a metadata-only edit included: one database read on
	// the device's next event, in place of a field-by-field judgement of which changes
	// matter that would have to be kept right forever.
	api.evictDeviceCredentials(ctx, updated.TenantId, priorDeviceId, updated.DeviceId)
	// A change that withdraws or alters what a session authenticated with ends the device's
	// open sessions; a rename or a metadata edit does not. A credential that is DISABLED
	// re-asks on every update, so repeating the disable after a failed kick is the retry.
	if authorityChanged := !updated.Enabled || request.CredentialType.Set || request.CredentialId.Set ||
		request.CredentialValue.Set || request.ExpiresAt.Set || repoint; authorityChanged {
		if kerr := api.endDeviceSessions(ctx, updated.TenantId, messaging.SessionKickCredentialChanged, currentDeviceToken, repointTo); kerr != nil {
			return updated, kerr
		}
	}
	return updated, nil
}

// adoptLegacySecret is the stored secret an update that does not name one must keep. That is
// the row's digest, UNLESS an old-version pod rotated the password during a rolling
// upgrade: it wrote the new password into credential_value and left secret_digest alone,
// so the plaintext column holds the later secret. The save that follows writes that column
// NULL (LegacyCredentialValue), so the plaintext is digested here first rather than
// discarded, which would bring back the password the old pod rotated away. Outside an
// upgrade's overlap the column is always NULL and this returns the digest unchanged.
func (api *Api) adoptLegacySecret(ctx context.Context, cred *DeviceCredential) (sql.NullString, error) {
	var legacy []sql.NullString
	if err := api.RDB.DB(ctx).Model(&DeviceCredential{}).Where("id = ?", cred.ID).
		Pluck("credential_value", &legacy).Error; err != nil {
		return sql.NullString{}, err
	}
	if len(legacy) != 1 || !legacy[0].Valid || legacy[0].String == "" {
		return cred.SecretDigest, nil
	}
	return digestSecret(api.DeviceSecretKey, cred.TenantId, &legacy[0].String, 0)
}

// Get device credentials by id.
func (api *Api) DeviceCredentialsById(ctx context.Context, ids []uint) ([]*DeviceCredential, error) {
	return rdb.FindByIds[DeviceCredential](api.RDB.DB(ctx).Preload("Device"), ids)
}

// Get device credentials by token.
func (api *Api) DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("Device"), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// deviceCredentialFilters builds the WHERE clauses for a credential search, shared by
// the paged DeviceCredentials read.
func (api *Api) deviceCredentialFilters(ctx context.Context,
	criteria DeviceCredentialSearchCriteria) func(result *gorm.DB) *gorm.DB {
	return func(result *gorm.DB) *gorm.DB {
		if criteria.Device != nil {
			result = result.Where("device_id = (?)",
				api.RDB.DB(ctx).Model(&Device{}).Select("id").Where("token = ?", criteria.Device))
		}
		if criteria.CredentialType != nil {
			result = result.Where("credential_type = ?", criteria.CredentialType)
		}
		if criteria.CredentialId != nil {
			result = result.Where("credential_id IN ?", api.searchCredentialIds(ctx, criteria.CredentialType, *criteria.CredentialId))
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", criteria.Enabled)
		}
		return result.Preload("Device")
	}
}

// searchCredentialIds are the stored values a credentialId filter matches: the id as sent
// (an MQTT username, or the fingerprint a read of an ACCESS_TOKEN returns) and, unless the
// search is narrowed to another type, the digest of the id as an access token, so an
// operator holding a token can still find its credential.
func (api *Api) searchCredentialIds(ctx context.Context, ctype *string, id string) []string {
	ids := []string{id}
	if ctype != nil && !accessTokenCredential(*ctype) {
		return ids
	}
	if tenant, err := tenantOf(ctx); err == nil {
		if digest, err := storedCredentialId(api.DeviceSecretKey, tenant, string(CredentialAccessToken), id); err == nil {
			ids = append(ids, digest)
		}
	}
	return ids
}

// Search for device credentials that meet criteria, ONE PAGE at a time.
//
// This is the GraphQL-facing read and it is always bounded.
func (api *Api) DeviceCredentials(ctx context.Context, criteria DeviceCredentialSearchCriteria) (*DeviceCredentialSearchResults, error) {
	results := make([]DeviceCredential, 0)
	db, pag := api.RDB.ListOf(ctx, &DeviceCredential{},
		api.deviceCredentialFilters(ctx, criteria), criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}

	// Wrap as search results.
	return &DeviceCredentialSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// Resolve a presented credential (type + id) to its owning device credential, in ONE
// statement: the owning device comes back on the same SELECT through a LEFT JOIN, which
// carries the device's soft-delete predicate in its ON clause, so a soft-deleted device
// leaves Device nil rather than dropping the credential row. Only enabled credentials
// match; returns gorm.ErrRecordNotFound if none. This is the lookup behind every
// credential-bearing event and every access-token MQTT connect (ADR-014), and it reads
// every column of both rows. An MQTT password connect runs the same statement over far
// fewer columns: deviceCredentialForConnect.
func (api *Api) DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error) {
	lookupId, err := api.presentedCredentialLookupId(ctx, credentialType, credentialId)
	if err != nil {
		return nil, err
	}
	found := make([]*DeviceCredential, 0)
	result := api.presentedCredentialStatement(ctx, credentialType, lookupId).Find(&found)
	return oneLiveCredential(result, found, credentialType, lookupId)
}

// presentedCredentialLookupId is the stored credential_id a presented (type, id) is matched
// against: storedCredentialId under the context's tenant. A missing tenant or key is an
// error, never a lookup that matches nothing quietly. An access token longer than any
// stored one matches nothing and is not digested.
func (api *Api) presentedCredentialLookupId(ctx context.Context, ctype, id string) (string, error) {
	if !accessTokenCredential(ctype) {
		return id, nil
	}
	tenant, err := tenantOf(ctx)
	if err != nil {
		return "", err
	}
	if len(id) > credential.MaxAccessTokenBytes {
		return "", gorm.ErrRecordNotFound
	}
	return storedCredentialId(api.DeviceSecretKey, tenant, ctype, id)
}

// connectCredentialColumns and connectDeviceFields are everything an MQTT password
// connect reads from a resolved credential and its device: what checkExpiry,
// storedSecret and credentialDevice read on the credential, and the device's id, tenant
// and token, and when it was disabled — the callout refuses a disabled device, and a NULL
// here reads as "enabled", so leaving the field out would admit one.
//
// The credential's columns are written table-qualified, as the WHERE clause is: a
// root-statement Select by FIELD name renders an unqualified column, and id, tenant_id,
// created_at and deleted_at exist in both joined tables, so the statement would be
// refused as ambiguous, and every password connect with it. It is an allowlist on
// purpose: a column added to either model later is not read here until it is added here.
//
// The device's fields go by field name, which gorm qualifies with the join's alias.
// "ID" must stay in the set: it is NOT NULL, and it is the column gorm reads to decide
// whether the joined device is there at all (a soft-deleted device leaves Device nil).
var (
	connectCredentialColumns = []string{
		"device_credentials.id",
		"device_credentials.tenant_id",
		"device_credentials.device_id",
		"device_credentials.secret_digest",
		"device_credentials.expires_at",
	}
	connectDeviceFields = []string{"ID", "TenantId", "Token", "DisabledAt"}
)

// deviceCredentialForConnect is DeviceCredentialByCredentialId reading only
// connectCredentialColumns and connectDeviceFields: the lookup of an MQTT password
// connect, through ResolveDeviceCredential.
//
// A row read here fills only the fields the password check reads. Every other field, the
// device's and the credential's metadata, name, description and external id included, is
// left at its zero value. That is the point: a username that exists costs one row of a
// fixed set of columns whose size does not depend on how much the device stores, so the
// time a refusal takes cannot be stretched by metadata (bounded only by the request body
// limit of the API that writes it) into a signal that the username exists. The empty
// result an unknown username gets is still cheaper than that one row; see checkPassword
// in processor/callout.go.
//
// 🔴 A ROW FROM HERE MUST NEVER REACH evaluateCredential. CredentialType is left empty
// and Enabled false, and credentialRequiresSecret decides whether to compare a secret
// from CredentialType: on a row read here it sees no secret-bearing type and SKIPS the
// compare, so any password would authenticate. Only the password connect, which
// compares the secret itself, may read through this finder.
func (api *Api) deviceCredentialForConnect(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	device := api.RDB.Database.Session(&gorm.Session{NewDB: true}).Select(connectDeviceFields)
	result := api.presentedCredentialStatement(ctx, credentialType, credentialId, device).
		Select(connectCredentialColumns).
		Find(&found)
	markMetadataUnread(found)
	return oneLiveCredential(result, found, credentialType, credentialId)
}

// authCredentialColumns and authDeviceFields are everything the per-event credential
// check and the access-token connect read from a resolved credential and its device.
// On the credential: evaluateCredential and credentialDevice (id, tenant, device id, type,
// stored secret, expiry), the enabled flag and type CredentialCache.fill requires before it
// keeps a row. On the device: what the event resolver reads of the authenticated device
// (id, tenant, token, device type, external id), what the callout reads (token), and when
// the device was disabled, which both refuse.
//
// The credential's columns are table-qualified for the reason connectCredentialColumns
// states. An allowlist on purpose: a column added to either model later is not read, and
// not copied into the credential cache, until it is added here; the golden test
// TestTheEventLookupResolvesWhatTheFullReadResolves fails if the resolver starts reading a
// device field this set leaves out.
var (
	authCredentialColumns = []string{
		"device_credentials.id",
		"device_credentials.credential_id",
		"device_credentials.tenant_id",
		"device_credentials.device_id",
		"device_credentials.credential_type",
		"device_credentials.secret_digest",
		"device_credentials.enabled",
		"device_credentials.expires_at",
	}
	authDeviceFields = []string{"ID", "TenantId", "Token", "DeviceTypeId", "ExternalId", "DisabledAt"}
)

// deviceCredentialForAuth is DeviceCredentialByCredentialId reading only
// authCredentialColumns and authDeviceFields: the lookup behind authenticateCredential,
// which is to say every cache miss of the per-event credential check and every access-token
// connect. Metadata, name and description of both rows are not read, so a miss costs the
// same however much the device stores, and the row the credential cache keeps is smaller.
//
// It is the same statement as the full finder, tenant-scoped by the same callback on the
// credential's own table and with the same soft-delete predicate on the device join.
func (api *Api) deviceCredentialForAuth(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	device := api.RDB.Database.Session(&gorm.Session{NewDB: true}).Select(authDeviceFields)
	result := api.presentedCredentialStatement(ctx, credentialType, credentialId, device).
		Select(authCredentialColumns).
		Find(&found)
	markMetadataUnread(found)
	return oneLiveCredential(result, found, credentialType, credentialId)
}

// markMetadataUnread marks the device of each row a narrow lookup found as read without
// its metadata (Device.MetadataRead), so a nil Metadata on it is not taken for none stored.
func markMetadataUnread(found []*DeviceCredential) {
	for _, c := range found {
		if c != nil && c.Device != nil {
			c.Device.metadataUnread = true
		}
	}
}

// presentedCredentialStatement is the one statement that resolves a presented
// credential: enabled-only, matched on type and id, tenant-scoped by the callback on the
// credential's own table, with the owning device on a LEFT JOIN carrying the device's
// soft-delete predicate. deviceJoin is passed to Joins("Device", ...) as is: nothing for
// every device column, or a *gorm.DB carrying a Select for fewer. That sub-DB only
// carries clauses and is never executed, so it owes no tenant scope of its own.
//
// 🔴 THE JOINED DEVICE IS NOT TENANT-SCOPED BY THE QUERY. The tenant-scope callback adds
// its predicate to the statement's own table only — the credential's — and nothing to a
// joined one. The credential row is this tenant's; the device it points at is only
// whatever its device_id names. A caller must therefore check the joined device against
// the credential before trusting it, which is what credentialDevice does. The predicates
// are table-qualified so a column the two tables come to share cannot make the statement
// ambiguous.
func (api *Api) presentedCredentialStatement(ctx context.Context, credentialType string, credentialId string,
	deviceJoin ...any) *gorm.DB {
	return api.RDB.DB(ctx).Joins("Device", deviceJoin...).
		Where("device_credentials.credential_type = ? AND device_credentials.credential_id = ? AND device_credentials.enabled = ?",
			credentialType, credentialId, true)
}

// oneLiveCredential is what a presentedCredentialStatement found, as one credential: the
// statement's error as is, gorm.ErrRecordNotFound for none, and an error for more than one.
func oneLiveCredential(result *gorm.DB, found []*DeviceCredential, credentialType string, credentialId string) (*DeviceCredential, error) {
	if result.Error != nil {
		return nil, result.Error
	}
	if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	// The partial unique index guarantees at most one live match. More than one
	// means that invariant was violated (index missing or corrupt) — fail closed
	// rather than silently authenticating against an ambiguous credential.
	if len(found) > 1 {
		return nil, fmt.Errorf("device credential lookup ambiguous: %d live rows for type %q id %q",
			len(found), credentialType, credentialId)
	}
	return found[0], nil
}
