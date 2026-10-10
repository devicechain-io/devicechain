// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
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
	secretDigest, err := digestSecret(key, device.TenantId, request.CredentialValue)
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
		DeviceId:       device.ID,
		Device:         device,
		CredentialType: request.CredentialType,
		CredentialId:   request.CredentialId,
		SecretDigest:   secretDigest,
		Enabled:        request.Enabled,
		ExpiresAt:      expiresAt,
	}, nil
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
func digestSecret(key *credential.DeviceSecretKey, tenant string, value *string) (sql.NullString, error) {
	plain := sqlnull.Secret(value)
	if !plain.Valid {
		return sql.NullString{}, nil
	}
	if len(plain.String) > credential.MaxDeviceSecretBytes {
		return sql.NullString{}, limit.Exceeded("credentialValue bytes", len(plain.String), credential.MaxDeviceSecretBytes)
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
	secretDigest := updated.SecretDigest
	if request.CredentialValue.Set {
		if secretDigest, err = digestSecret(api.DeviceSecretKey, updated.TenantId, request.CredentialValue.Value); err != nil {
			return nil, err
		}
	} else if secretDigest, err = api.adoptLegacySecret(ctx, updated); err != nil {
		return nil, err
	}

	updated.Metadata = metadataJSON
	updated.CredentialType = credentialType
	updated.CredentialId = credentialId
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
	return digestSecret(api.DeviceSecretKey, cred.TenantId, &legacy[0].String)
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
			result = result.Where("credential_id = ?", criteria.CredentialId)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", criteria.Enabled)
		}
		return result.Preload("Device")
	}
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
	found := make([]*DeviceCredential, 0)
	result := api.presentedCredentialStatement(ctx, credentialType, credentialId).Find(&found)
	return oneLiveCredential(result, found, credentialType, credentialId)
}

// connectCredentialColumns and connectDeviceFields are everything an MQTT password
// connect reads from a resolved credential and its device: what checkExpiry,
// storedSecret and credentialDevice read on the credential, and the device's id, tenant
// and token.
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
	connectDeviceFields = []string{"ID", "TenantId", "Token"}
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
	return oneLiveCredential(result, found, credentialType, credentialId)
}

// authCredentialColumns and authDeviceFields are everything the per-event credential
// check and the access-token connect read from a resolved credential and its device.
// On the credential: evaluateCredential and credentialDevice (id, tenant, device id, type,
// stored secret, expiry), the enabled flag and type CredentialCache.fill requires before it
// keeps a row. On the device: what the event resolver reads of the authenticated device
// (id, tenant, token, device type, external id) and what the callout reads (token).
//
// The credential's columns are table-qualified for the reason connectCredentialColumns
// states. An allowlist on purpose: a column added to either model later is not read, and
// not copied into the credential cache, until it is added here; the golden test
// TestTheEventLookupResolvesWhatTheFullReadResolves fails if the resolver starts reading a
// device field this set leaves out.
var (
	authCredentialColumns = []string{
		"device_credentials.id",
		"device_credentials.tenant_id",
		"device_credentials.device_id",
		"device_credentials.credential_type",
		"device_credentials.credential_value",
		"device_credentials.enabled",
		"device_credentials.expires_at",
	}
	authDeviceFields = []string{"ID", "TenantId", "Token", "DeviceTypeId", "ExternalId"}
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
	return oneLiveCredential(result, found, credentialType, credentialId)
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
