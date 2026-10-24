// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Errors returned by provisioning-profile writes. They are sentinels so
// a caller can map each outcome without string matching.
var (
	// ErrProvisioningSecretEmpty means an empty or blank provision secret was supplied
	// on create. An empty secret is no proof of anything, so it is rejected at write
	// time rather than persisted as a profile anyone holding the key can use.
	ErrProvisioningSecretEmpty = errors.New("provision secret must not be empty")
	// ErrProvisioningKeyEmpty means an empty or blank provision key was supplied on
	// create. The key is how a device names the profile it is registering against.
	ErrProvisioningKeyEmpty = errors.New("provision key must not be empty")
	// errNoProvisioningSecretKey refuses a write carrying a secret when no EnrollmentSecretKey is
	// wired: nothing falls back to storing the secret.
	errNoProvisioningSecretKey = errors.New("provisioning secrets cannot be stored: no provisioning secret key is configured")
)

// digestProvisionSecret is the stored form of a provisioning secret: its keyed digest, bound
// to the profile's tenant. The secret is digested EXACTLY AS SENT, surrounding whitespace
// included, because a device presents it byte for byte. A blank one is refused by the caller
// before it gets here; one over credential.MaxEnrollmentSecretBytes is refused with
// LIMIT_EXCEEDED, which the plaintext column did before it (a digest is fixed-width and would
// not).
func (api *Api) digestProvisionSecret(tenant, secret string) (sql.NullString, error) {
	if len(secret) > credential.MaxEnrollmentSecretBytes {
		return sql.NullString{}, limit.Exceeded("provisionSecret bytes", len(secret), credential.MaxEnrollmentSecretBytes)
	}
	if api.EnrollmentSecretKey == nil {
		return sql.NullString{}, errNoProvisioningSecretKey
	}
	digest, err := api.EnrollmentSecretKey.Digest(tenant, secret)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: digest, Valid: true}, nil
}

// provisionableCredentialType reports whether provisioning can mint a credential
// of the given type. Only ACCESS_TOKEN is mintable today: its id is a generated
// bearer token, needing no out-of-band material. Minting MQTT_BASIC (a generated
// password) is a later onboarding slice.
func provisionableCredentialType(ctype string) bool {
	return CredentialType(ctype) == CredentialAccessToken
}

// Create a new provisioning profile.
func (api *Api) CreateProvisioningProfile(ctx context.Context, request *ProvisioningProfileCreateRequest) (*ProvisioningProfile, error) {
	// A blank key or secret is refused, not stored. TrimSpace DECIDES here and does not
	// transform: a padded value is stored exactly as sent. The update path already
	// refuses a blank value (OptionalString.ApplyToRequired), by the same rule.
	if strings.TrimSpace(request.ProvisionKey) == "" {
		return nil, ErrProvisioningKeyEmpty
	}
	if strings.TrimSpace(request.ProvisionSecret) == "" {
		return nil, ErrProvisioningSecretEmpty
	}
	if !ProvisioningStrategy(request.Strategy).Valid() {
		return nil, fmt.Errorf("invalid provisioning strategy: %s", request.Strategy)
	}

	// Default and constrain the minted credential type.
	credentialType := string(CredentialAccessToken)
	if request.CredentialType != nil {
		credentialType = *request.CredentialType
	}
	if !provisionableCredentialType(credentialType) {
		return nil, fmt.Errorf("provisioning cannot mint credential type %q (only %s)",
			credentialType, CredentialAccessToken)
	}

	deviceTypes, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
	if err != nil {
		return nil, err
	}
	if len(deviceTypes) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	expiresAt, err := parseOptionalTime(request.ExpiresAt)
	if err != nil {
		return nil, err
	}

	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	secretDigest, err := api.digestProvisionSecret(deviceTypes[0].TenantId, request.ProvisionSecret)
	if err != nil {
		return nil, err
	}
	created := &ProvisioningProfile{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: metadataJSON,
		},
		ProvisionKey:          request.ProvisionKey,
		ProvisionSecretDigest: secretDigest,
		Strategy:              request.Strategy,
		DeviceType:            deviceTypes[0],
		CredentialType:        credentialType,
		Enabled:               request.Enabled,
		ExpiresAt:             expiresAt,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// UpdateProvisioningProfile applies a PARTIAL update: a field the caller did not name
// keeps its stored value — including the shared SECRET the fleet presents, which the
// full-replace shape blanked on every edit that failed to restate it.
func (api *Api) UpdateProvisioningProfile(ctx context.Context, token string,
	request *ProvisioningProfileUpdateRequest) (*ProvisioningProfile, error) {
	matches, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	updated := matches[0]

	// Everything that can refuse resolves before anything is written.
	currentTypeToken := ""
	if updated.DeviceType != nil {
		currentTypeToken = updated.DeviceType.Token
	}
	retypeTo, retype, err := resolveRequiredTypeRef(request.DeviceTypeToken, currentTypeToken, "deviceTypeToken")
	if err != nil {
		return nil, err
	}
	var deviceType *DeviceType
	if retype {
		deviceTypes, err := api.DeviceTypesByToken(ctx, []string{retypeTo})
		if err != nil {
			return nil, err
		}
		if len(deviceTypes) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		deviceType = deviceTypes[0]
	}
	strategy, err := request.Strategy.ApplyToRequired("strategy", updated.Strategy)
	if err != nil {
		return nil, err
	}
	if request.Strategy.Set && !ProvisioningStrategy(strategy).Valid() {
		return nil, fmt.Errorf("invalid provisioning strategy: %s", strategy)
	}
	provisionKey, err := request.ProvisionKey.ApplyToRequired("provisionKey", updated.ProvisionKey)
	if err != nil {
		return nil, err
	}
	// The stored secret is a digest and is never read back, so "keep it" is decided by whether
	// the request names one. An absent secret keeps the stored digest; a named one is checked
	// for blank, then digested as sent.
	newSecret, err := request.ProvisionSecret.ApplyToRequired("provisionSecret", "")
	if err != nil {
		return nil, err
	}
	secretDigest := updated.ProvisionSecretDigest
	if request.ProvisionSecret.Set {
		if secretDigest, err = api.digestProvisionSecret(updated.TenantId, newSecret); err != nil {
			return nil, err
		}
	} else if secretDigest, err = api.adoptLegacyProvisionSecret(ctx, updated); err != nil {
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

	updated.Name = request.Name.ApplyToNullString(updated.Name)
	updated.Description = request.Description.ApplyToNullString(updated.Description)
	updated.Metadata = metadataJSON
	updated.ProvisionKey = provisionKey
	updated.ProvisionSecretDigest = secretDigest
	updated.Strategy = strategy
	// CredentialType is deliberately NOT assigned: it is not in the input (see
	// ProvisioningProfileUpdateRequest), so an update leaves what creation chose.
	updated.Enabled = enabled
	updated.ExpiresAt = expiresAt
	if deviceType != nil {
		updated.DeviceType = deviceType
		updated.DeviceTypeId = deviceType.ID
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// adoptLegacyProvisionSecret is the stored secret an update that does not name one must keep.
// That is the row's digest, UNLESS an old-version pod rotated the secret during a rolling
// upgrade: it wrote the new secret into provision_secret and left the digest alone, so the
// plaintext column holds the later secret. The save that follows writes that column NULL
// (LegacyProvisionSecret), so the plaintext is digested here first rather than discarded,
// which would bring back the secret the old pod rotated away. Outside an upgrade's overlap
// the column is always NULL and this returns the digest unchanged.
func (api *Api) adoptLegacyProvisionSecret(ctx context.Context, profile *ProvisioningProfile) (sql.NullString, error) {
	var legacy []sql.NullString
	if err := api.RDB.DB(ctx).Model(&ProvisioningProfile{}).Where("id = ?", profile.ID).
		Pluck("provision_secret", &legacy).Error; err != nil {
		return sql.NullString{}, err
	}
	if len(legacy) != 1 || !legacy[0].Valid || strings.TrimSpace(legacy[0].String) == "" {
		return profile.ProvisionSecretDigest, nil
	}
	return api.digestProvisionSecret(profile.TenantId, legacy[0].String)
}

// Get provisioning profiles by id.
func (api *Api) ProvisioningProfilesById(ctx context.Context, ids []uint) ([]*ProvisioningProfile, error) {
	return rdb.FindByIds[ProvisioningProfile](api.RDB.DB(ctx).Preload("DeviceType"), ids)
}

// Get provisioning profiles by token.
func (api *Api) ProvisioningProfilesByToken(ctx context.Context, tokens []string) ([]*ProvisioningProfile, error) {
	found := make([]*ProvisioningProfile, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx).Preload("DeviceType"), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, nil
}

// Search for provisioning profiles that meet criteria.
func (api *Api) ProvisioningProfiles(ctx context.Context, criteria ProvisioningProfileSearchCriteria) (*ProvisioningProfileSearchResults, error) {
	results := make([]ProvisioningProfile, 0)
	db, pag := api.RDB.ListOf(ctx, &ProvisioningProfile{}, func(result *gorm.DB) *gorm.DB {
		if criteria.DeviceType != nil {
			result = result.Where("device_type_id = (?)",
				api.RDB.DB(ctx).Model(&DeviceType{}).Select("id").Where("token = ?", criteria.DeviceType))
		}
		if criteria.Strategy != nil {
			result = result.Where("strategy = ?", criteria.Strategy)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", criteria.Enabled)
		}
		return result.Preload("DeviceType")
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &ProvisioningProfileSearchResults{
		Results:    results,
		Pagination: pag,
	}, nil
}

// parseOptionalTime parses an optional RFC3339 timestamp into a sql.NullTime,
// returning the zero (invalid) value when the input is nil.
func parseOptionalTime(value *string) (sql.NullTime, error) {
	if value == nil {
		return sql.NullTime{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, *value)
	if err != nil {
		return sql.NullTime{}, err
	}
	return sql.NullTime{Time: parsed, Valid: true}, nil
}
