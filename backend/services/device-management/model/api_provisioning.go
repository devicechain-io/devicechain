// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Errors returned by ProvisionDevice. They are sentinels so a caller (e.g. the
// future provisioning transport) can map each outcome without string matching.
// A device-facing transport collapses all of them to one generic rejection so a
// caller cannot probe which provision keys exist; operators see the distinct
// reasons.
var (
	// ErrProvisioningKeyNotResolved means the presented provision key did not
	// resolve to a profile in the request's tenant.
	ErrProvisioningKeyNotResolved = errors.New("provision key did not resolve to a provisioning profile")
	// ErrProvisioningDisabled means the resolved profile is disabled.
	ErrProvisioningDisabled = errors.New("provisioning profile is disabled")
	// ErrProvisioningExpired means the resolved profile is past its ExpiresAt.
	ErrProvisioningExpired = errors.New("provisioning profile has expired")
	// ErrProvisioningSecretMismatch means the presented provision secret did not
	// match the profile's secret.
	ErrProvisioningSecretMismatch = errors.New("provision secret did not match")
	// ErrProvisioningStrategyInvalid means the profile's stored strategy is not in
	// the known vocabulary (a misconfigured profile).
	ErrProvisioningStrategyInvalid = errors.New("provisioning profile strategy is not recognized")
	// ErrProvisioningDeviceNotPreProvisioned means the device does not exist and the
	// profile's CHECK_PRE_PROVISIONED strategy forbids creating it.
	ErrProvisioningDeviceNotPreProvisioned = errors.New("device is not pre-provisioned and the profile does not allow new devices")
)

// provisionableCredentialType reports whether provisioning can mint a credential
// of the given type. Only ACCESS_TOKEN is mintable today: its id is a generated
// bearer token, needing no out-of-band material. Minting MQTT_BASIC (a generated
// password) or X509 (a CA-signed cert) is a later onboarding slice.
func provisionableCredentialType(ctype string) bool {
	return CredentialType(ctype) == CredentialAccessToken
}

// Create a new provisioning profile.
func (api *Api) CreateProvisioningProfile(ctx context.Context, request *ProvisioningProfileCreateRequest) (*ProvisioningProfile, error) {
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

	created := &ProvisioningProfile{
		TokenReference: rdb.TokenReference{
			Token: request.Token,
		},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{
			Metadata: rdb.MetadataStrOf(request.Metadata),
		},
		ProvisionKey:    request.ProvisionKey,
		ProvisionSecret: request.ProvisionSecret,
		Strategy:        request.Strategy,
		DeviceType:      deviceTypes[0],
		CredentialType:  credentialType,
		Enabled:         request.Enabled,
		ExpiresAt:       expiresAt,
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	return created, nil
}

// Update an existing provisioning profile.
func (api *Api) UpdateProvisioningProfile(ctx context.Context, token string,
	request *ProvisioningProfileCreateRequest) (*ProvisioningProfile, error) {
	matches, err := api.ProvisioningProfilesByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	if !ProvisioningStrategy(request.Strategy).Valid() {
		return nil, fmt.Errorf("invalid provisioning strategy: %s", request.Strategy)
	}

	credentialType := string(CredentialAccessToken)
	if request.CredentialType != nil {
		credentialType = *request.CredentialType
	}
	if !provisionableCredentialType(credentialType) {
		return nil, fmt.Errorf("provisioning cannot mint credential type %q (only %s)",
			credentialType, CredentialAccessToken)
	}

	expiresAt, err := parseOptionalTime(request.ExpiresAt)
	if err != nil {
		return nil, err
	}

	updated := matches[0]
	updated.Token = request.Token
	updated.Name = rdb.NullStrOf(request.Name)
	updated.Description = rdb.NullStrOf(request.Description)
	updated.Metadata = rdb.MetadataStrOf(request.Metadata)
	updated.ProvisionKey = request.ProvisionKey
	updated.ProvisionSecret = request.ProvisionSecret
	updated.Strategy = request.Strategy
	updated.CredentialType = credentialType
	updated.Enabled = request.Enabled
	updated.ExpiresAt = expiresAt

	// Re-resolve the target device type if it changed.
	if updated.DeviceType == nil || request.DeviceTypeToken != updated.DeviceType.Token {
		deviceTypes, err := api.DeviceTypesByToken(ctx, []string{request.DeviceTypeToken})
		if err != nil {
			return nil, err
		}
		if len(deviceTypes) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		updated.DeviceType = deviceTypes[0]
	}

	result := api.RDB.DB(ctx).Save(updated)
	if result.Error != nil {
		return nil, result.Error
	}
	return updated, nil
}

// Get provisioning profiles by id.
func (api *Api) ProvisioningProfilesById(ctx context.Context, ids []uint) ([]*ProvisioningProfile, error) {
	found := make([]*ProvisioningProfile, 0)
	result := api.RDB.DB(ctx).Preload("DeviceType").Find(&found, ids)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// Get provisioning profiles by token.
func (api *Api) ProvisioningProfilesByToken(ctx context.Context, tokens []string) ([]*ProvisioningProfile, error) {
	found := make([]*ProvisioningProfile, 0)
	result := api.RDB.DB(ctx).Preload("DeviceType").Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
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

// ProvisioningProfileByProvisionKey resolves the profile a connecting device's
// provision key names. The lookup is tenant scoped by the global callback, so a
// key belonging to another tenant never resolves. It is intentionally not
// enabled-only: the enabled/expiry gates live in evaluateProvisioningProfile so
// an operator can distinguish "no such key" from "disabled". Returns
// gorm.ErrRecordNotFound if no profile matches.
func (api *Api) ProvisioningProfileByProvisionKey(ctx context.Context, provisionKey string) (*ProvisioningProfile, error) {
	found := make([]*ProvisioningProfile, 0)
	result := api.RDB.DB(ctx).Preload("DeviceType").Where("provision_key = ?", provisionKey).Find(&found)
	if result.Error != nil {
		return nil, result.Error
	}
	if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return found[0], nil
}

// evaluateProvisioningProfile verifies a resolved profile is usable for a
// provisioning request: enabled, not expired, and the presented secret matches.
// It is pure (no I/O) so the policy is unit-testable in isolation; now is
// supplied by the caller for deterministic expiry.
func evaluateProvisioningProfile(profile *ProvisioningProfile, presentedSecret string, now time.Time) error {
	if !profile.Enabled {
		return ErrProvisioningDisabled
	}
	if profile.ExpiresAt.Valid && !now.Before(profile.ExpiresAt.Time) {
		return ErrProvisioningExpired
	}
	// Constant-time compare to avoid leaking the secret via timing.
	if subtle.ConstantTimeCompare([]byte(presentedSecret), []byte(profile.ProvisionSecret)) != 1 {
		return ErrProvisioningSecretMismatch
	}
	return nil
}

// provisioningRejectsUnknownDevice reports whether a profile's strategy forbids
// creating a device that does not already exist (CHECK_PRE_PROVISIONED). Pure so
// the strategy gate is unit-testable.
func provisioningRejectsUnknownDevice(strategy ProvisioningStrategy) bool {
	return strategy == ProvisionCheckPreProvisioned
}

// ProvisionDevice runs the per-profile self-registration flow (ADR-012): it
// resolves the provision key, verifies the profile (enabled/expiry/secret), then
// applies the profile's strategy — ALLOW_NEW creates the device on first contact;
// CHECK_PRE_PROVISIONED requires it to already exist — and returns the device
// together with a credential it can authenticate with. The credential is reused
// if the device already holds an enabled one of the profile's type, so repeated
// provisioning is idempotent rather than minting duplicate credentials. now is
// supplied by the caller so expiry is deterministic in tests.
func (api *Api) ProvisionDevice(ctx context.Context, request *ProvisionDeviceRequest, now time.Time) (*ProvisionDeviceResult, error) {
	profile, err := api.ProvisioningProfileByProvisionKey(ctx, request.ProvisionKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProvisioningKeyNotResolved
		}
		return nil, err
	}
	if err := evaluateProvisioningProfile(profile, request.ProvisionSecret, now); err != nil {
		return nil, err
	}
	strategy := ProvisioningStrategy(profile.Strategy)
	if !strategy.Valid() {
		return nil, ErrProvisioningStrategyInvalid
	}
	if profile.DeviceType == nil {
		return nil, ErrProvisioningStrategyInvalid
	}

	// Resolve the claimed identity; absence drives the strategy decision.
	devices, err := api.DevicesByToken(ctx, []string{request.DeviceToken})
	if err != nil {
		return nil, err
	}

	var device *Device
	created := false
	if len(devices) > 0 {
		device = devices[0]
	} else {
		if provisioningRejectsUnknownDevice(strategy) {
			return nil, ErrProvisioningDeviceNotPreProvisioned
		}
		device, err = api.CreateDevice(ctx, &DeviceCreateRequest{
			Token:           request.DeviceToken,
			Name:            request.Name,
			DeviceTypeToken: profile.DeviceType.Token,
			Metadata:        request.Metadata,
		})
		if err != nil {
			return nil, err
		}
		created = true
	}

	credentialId, err := api.mintOrReuseCredential(ctx, device.Token, profile.CredentialType, now)
	if err != nil {
		return nil, err
	}

	return &ProvisionDeviceResult{
		Device:         device,
		CredentialType: profile.CredentialType,
		CredentialId:   credentialId,
		// ACCESS_TOKEN carries no separate secret: the id is the bearer token.
		CredentialValue: nil,
		Created:         created,
	}, nil
}

// ProvisionDeviceBootstrap is the entry point an unauthenticated provisioning
// transport calls: a device presents only its provision key+secret and never
// names a tenant. It resolves the owning tenant from the globally-unique
// provision key before any tenant is known — the device-side analog of the
// login-by-username lookup and a sanctioned WithSystemContext bootstrap
// (ADR-015) — then re-enters scoped to that tenant and runs ProvisionDevice, so
// the secret/strategy gates and the device + credential writes are all tenant
// isolated exactly as an authenticated path would be. The secret is NOT trusted
// during tenant resolution; ProvisionDevice verifies it within the tenant scope.
func (api *Api) ProvisionDeviceBootstrap(ctx context.Context, request *ProvisionDeviceRequest, now time.Time) (*ProvisionDeviceResult, error) {
	sysctx := core.WithSystemContext(ctx)
	profile, err := api.ProvisioningProfileByProvisionKey(sysctx, request.ProvisionKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProvisioningKeyNotResolved
		}
		return nil, err
	}
	tctx := core.WithTenant(ctx, profile.TenantId)
	return api.ProvisionDevice(tctx, request, now)
}

// mintOrReuseCredential returns the credential id a provisioned device should
// authenticate with for the given type. It reuses the device's existing enabled,
// UNEXPIRED credential of that type when present (so re-provisioning is
// idempotent), and otherwise mints a fresh one. Only ACCESS_TOKEN is minted today
// (see provisionableCredentialType), so the generated id is the bearer token and
// no secret value is stored. now is supplied so an expired credential is never
// handed back (review #4): reusing one would return a dead token the device
// cannot authenticate with.
func (api *Api) mintOrReuseCredential(ctx context.Context, deviceToken string, credentialType string, now time.Time) (string, error) {
	enabled := true
	existing, err := api.DeviceCredentials(ctx, DeviceCredentialSearchCriteria{
		// Reuse must consider every live credential of this type for the device, not
		// a bounded page — the explicit internal unbounded path (ADR-029). A bounded
		// default could miss a reusable credential past the page and mint a duplicate.
		Pagination:     rdb.Pagination{Unbounded: true},
		Device:         &deviceToken,
		CredentialType: &credentialType,
		Enabled:        &enabled,
	})
	if err != nil {
		return "", err
	}
	for _, cred := range existing.Results {
		// Skip an enabled-but-expired credential: it would authenticate to nothing.
		if cred.ExpiresAt.Valid && !now.Before(cred.ExpiresAt.Time) {
			continue
		}
		return cred.CredentialId, nil
	}

	credentialId := uuid.New().String()
	_, err = api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token:          uuid.New().String(),
		DeviceToken:    deviceToken,
		CredentialType: credentialType,
		CredentialId:   credentialId,
		Enabled:        true,
	})
	if err != nil {
		return "", err
	}
	return credentialId, nil
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
