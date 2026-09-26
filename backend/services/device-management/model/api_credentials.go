// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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
func buildDeviceCredential(device *Device, request *DeviceCredentialCreateRequest) (*DeviceCredential, error) {
	// Validate credential type against the known vocabulary.
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
		// Stored EXACTLY AS SENT: a device presents its password byte for byte, so trimming
		// it here would store something no device sends.
		CredentialValue: sqlnull.Secret(request.CredentialValue),
		Enabled:         request.Enabled,
		ExpiresAt:       expiresAt,
	}, nil
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

	created, err := buildDeviceCredential(matches[0], request)
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

	updated.Metadata = metadataJSON
	updated.CredentialType = credentialType
	updated.CredentialId = credentialId
	updated.CredentialValue = request.CredentialValue.ApplyToNullSecret(updated.CredentialValue)
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
	return updated, nil
}

// Get device credentials by id.
func (api *Api) DeviceCredentialsById(ctx context.Context, ids []uint) ([]*DeviceCredential, error) {
	return rdb.FindByIds[DeviceCredential](api.RDB.DB(ctx).Preload("Device"), ids)
}

// Get device credentials by token.
func (api *Api) DeviceCredentialsByToken(ctx context.Context, tokens []string) ([]*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	result := api.RDB.DB(ctx)
	result = result.Preload("Device")
	result = result.Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// deviceCredentialFilters builds the WHERE clauses for a credential search, shared by
// the paged DeviceCredentials and the full-set EnabledDeviceCredentialsOfType so the two
// cannot drift into filtering differently.
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
// This is the GraphQL-facing read and it is always bounded. Provisioning's reuse scan
// needs every live credential of a type and calls EnabledDeviceCredentialsOfType, which
// is a separate method so that this one cannot be asked to return the table.
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

// EnabledDeviceCredentialsOfType returns EVERY enabled credential of one type held by
// one device, with no LIMIT.
//
// 🔴 THE FULL SET IS THE CORRECTNESS REQUIREMENT, NOT A CONVENIENCE. Its caller reuses
// an existing unexpired credential so that re-provisioning is idempotent; a bounded page
// could miss a reusable credential sitting past the page boundary and mint a duplicate
// instead. That is why this is a named method rather than a page size the caller has to
// remember to make large enough.
//
// It is bounded by how many credentials of one type one device holds, which is the
// standard ListAllOf asks of its callers — not by a LIMIT.
func (api *Api) EnabledDeviceCredentialsOfType(ctx context.Context,
	deviceToken string, credentialType string) (*DeviceCredentialSearchResults, error) {
	enabled := true
	criteria := DeviceCredentialSearchCriteria{
		Device:         &deviceToken,
		CredentialType: &credentialType,
		Enabled:        &enabled,
	}
	results := make([]DeviceCredential, 0)
	db, pag := api.RDB.ListAllOf(ctx, &DeviceCredential{},
		api.deviceCredentialFilters(ctx, criteria))
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
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
// credential-bearing event and every MQTT connect (ADR-014).
//
// 🔴 THE JOINED DEVICE IS NOT TENANT-SCOPED BY THE QUERY. The tenant-scope callback adds
// its predicate to the statement's own table only — the credential's — and nothing to a
// joined one. The credential row is this tenant's; the device it points at is only
// whatever its device_id names. A caller must therefore check the joined device against
// the credential before trusting it, which is what credentialDevice does. The predicates
// are table-qualified so a column the two tables come to share cannot make the statement
// ambiguous.
func (api *Api) DeviceCredentialByCredentialId(ctx context.Context, credentialType string, credentialId string) (*DeviceCredential, error) {
	found := make([]*DeviceCredential, 0)
	result := api.RDB.DB(ctx).Joins("Device").
		Where("device_credentials.credential_type = ? AND device_credentials.credential_id = ? AND device_credentials.enabled = ?",
			credentialType, credentialId, true).
		Find(&found)
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
