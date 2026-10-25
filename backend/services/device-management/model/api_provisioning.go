// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Errors returned by provisioning-profile writes. They are sentinels so
// a caller can map each outcome without string matching.
var (
	// ErrProvisioningKeyEmpty means an empty or blank provision key was supplied on
	// create. The key is how a device names the profile it is registering against.
	ErrProvisioningKeyEmpty = errors.New("provision key must not be empty")
	// errNoProvisioningSecretKey refuses a write carrying a secret when no EnrollmentKeys is
	// wired: nothing falls back to storing the secret.
	errNoProvisioningSecretKey = errors.New("provisioning secrets cannot be stored: no provisioning secret key is configured")
	// ErrProvisioningMaxDevices refuses a cap below one. INVALID_VALUE. A profile that may
	// register nobody is disabled (enabled: false), and a cap that says so by being zero would
	// look like a profile that works.
	ErrProvisioningMaxDevices = integrity.NewRefusal(integrity.ClassInvalid,
		"maxDevices must be at least 1; disable the profile to stop it registering devices")
)

// provisionSecretBytes is the entropy of a generated provision secret: 256 bits.
const provisionSecretBytes = 32

// generateProvisionSecret is a fresh provision secret: 32 bytes from the system CSPRNG,
// base64url without padding (43 characters). It is the ONLY source of a provision secret.
func generateProvisionSecret() (string, error) {
	raw := make([]byte, provisionSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate a provision secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// validMaxDevices checks a cap that is being set.
func validMaxDevices(max *int32) error {
	if max != nil && *max < 1 {
		return ErrProvisioningMaxDevices
	}
	return nil
}

// inUseOn turns a database foreign-key refusal (SQLSTATE 23503, 23001) of a delete into
// ErrEntityInUse with a sentence saying what still refers, so a delete that lost a race with a
// write that added a reference after the API's own count is answered the way the count would
// have answered it, and never as a server error. Any other error, including ErrEntityInUse
// itself, is returned unchanged.
func inUseOn(err error, what string) error {
	if err == nil || errors.Is(err, ErrEntityInUse) {
		return err
	}
	for _, v := range integrity.All(err) {
		if v.Class == integrity.ClassReference {
			return fmt.Errorf("%w: %s", ErrEntityInUse, what)
		}
	}
	return err
}

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
	return api.digestStoredProvisionSecret(tenant, secret)
}

// digestStoredProvisionSecret digests a secret already at rest, which the length limit on a
// SET does not apply to: the old column bounded it at 256 characters, up to four bytes each.
func (api *Api) digestStoredProvisionSecret(tenant, secret string) (sql.NullString, error) {
	if api.EnrollmentKeys == nil {
		return sql.NullString{}, errNoProvisioningSecretKey
	}
	digest, err := api.EnrollmentKeys.Provisioning.Digest(tenant, secret)
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
	if err := validMaxDevices(request.MaxDevices); err != nil {
		return nil, err
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
	// The secret is the server's: generated here, digested for storage, and shown ONCE, on the
	// row handed back. ProvisionSecretIssued is what tells the endpoint it may trust it.
	issued, err := generateProvisionSecret()
	if err != nil {
		return nil, err
	}
	secretDigest, err := api.digestProvisionSecret(deviceTypes[0].TenantId, issued)
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
		ProvisionSecretIssued: true,
		Strategy:              request.Strategy,
		DeviceType:            deviceTypes[0],
		CredentialType:        credentialType,
		Enabled:               request.Enabled,
		ExpiresAt:             expiresAt,
		MaxDevices:            intPtrOf(request.MaxDevices),
	}
	result := api.RDB.DB(ctx).Create(created)
	if result.Error != nil {
		return nil, result.Error
	}
	created.IssuedSecret = issued
	return created, nil
}

// intPtrOf widens a GraphQL Int onto the model's *int.
func intPtrOf(v *int32) *int {
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}

// lockProfileByToken reads the provisioning profile with this token on tx, locking its row
// FOR UPDATE. Every path that touches a profile AND a device takes the profile's lock first
// (registration, revoke, reset, delete), which is what keeps two of them from each holding
// the lock the other needs. A token that names nothing in the tenant is gorm.ErrRecordNotFound.
func lockProfileByToken(tx *gorm.DB, token string) (*ProvisioningProfile, error) {
	var locked ProvisioningProfile
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		return nil, err
	}
	return &locked, nil
}

// RotateProvisioningProfileSecret replaces the profile's provision secret with a new one,
// generated here and returned ONCE, on the profile's IssuedSecret. It is a hard cutover: the
// old secret stops working the moment this commits, there is no overlap (overlap is a second
// profile), and devices that are already registered are unaffected because they authenticate
// with their own credential.
//
// It is the only write besides create that sets ProvisionSecretIssued, so it is also what
// brings a profile created before the server generated secrets into service.
//
// It takes the profile's row lock, so it serializes with registrations against the profile: a
// registration either committed under the old secret before this, or checks the new digest
// after it.
func (api *Api) RotateProvisioningProfileSecret(ctx context.Context, token string) (*ProvisioningProfile, error) {
	var rotated *ProvisioningProfile
	issued, err := generateProvisionSecret()
	if err != nil {
		return nil, err
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockProfileByToken(tx, token)
		if err != nil {
			return err
		}
		digest, err := api.digestProvisionSecret(locked.TenantId, issued)
		if err != nil {
			return err
		}
		locked.ProvisionSecretDigest = digest
		locked.ProvisionSecretIssued = true
		// Save writes the retired plaintext column NULL (LegacyProvisionSecret): a rotation
		// must not leave an old-version pod's plaintext behind to be digested over it.
		if err := tx.Save(locked).Error; err != nil {
			return err
		}
		rotated = locked
		return nil
	})
	if err != nil {
		return nil, err
	}
	rotated.IssuedSecret = issued
	return rotated, nil
}

// ProvisionedDeviceCount is how many devices hold a credential issued by the profile:
// disabled and expired ones included, which is what the profile's cap counts.
func (api *Api) ProvisionedDeviceCount(ctx context.Context, profileId uint) (int64, error) {
	return provisionedDeviceCount(api.RDB.DB(ctx), profileId)
}

func provisionedDeviceCount(db *gorm.DB, profileId uint) (int64, error) {
	var n int64
	err := db.Model(&DeviceCredential{}).Where("provisioning_profile_id = ?", profileId).
		Distinct("device_id").Count(&n).Error
	return n, err
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
	if request.MaxDevices.Set && request.MaxDevices.Value != nil {
		if err := validMaxDevices(request.MaxDevices.Value); err != nil {
			return nil, err
		}
	}

	// apply folds the request's fields onto base. It runs twice: once on the copy read above, so
	// every refusal comes before a transaction opens, and once on the row read UNDER THE LOCK,
	// which is the one that is written.
	apply := func(base *ProvisioningProfile) error {
		strategy, err := request.Strategy.ApplyToRequired("strategy", base.Strategy)
		if err != nil {
			return err
		}
		if request.Strategy.Set && !ProvisioningStrategy(strategy).Valid() {
			return fmt.Errorf("invalid provisioning strategy: %s", strategy)
		}
		provisionKey, err := request.ProvisionKey.ApplyToRequired("provisionKey", base.ProvisionKey)
		if err != nil {
			return err
		}
		enabled, err := request.Enabled.ApplyToRequired("enabled", base.Enabled)
		if err != nil {
			return err
		}
		expiresAt, err := request.ExpiresAt.ApplyToNullTime("expiresAt", base.ExpiresAt)
		if err != nil {
			return err
		}
		metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(base.Metadata)))
		if err != nil {
			return err
		}
		base.Name = request.Name.ApplyToNullString(base.Name)
		base.Description = request.Description.ApplyToNullString(base.Description)
		base.Metadata = metadataJSON
		base.ProvisionKey = provisionKey
		base.MaxDevices = request.MaxDevices.ApplyToIntPtr(base.MaxDevices)
		base.Strategy = strategy
		// CredentialType is deliberately NOT assigned: it is not in the input (see
		// ProvisioningProfileUpdateRequest), so an update leaves what creation chose.
		base.Enabled = enabled
		base.ExpiresAt = expiresAt
		if deviceType != nil {
			base.DeviceType = deviceType
			base.DeviceTypeId = deviceType.ID
		}
		return nil
	}
	preview := *updated
	if err := apply(&preview); err != nil {
		return nil, err
	}

	// 🔴 EVERY FIELD IS FOLDED ONTO THE LOCKED ROW, not just the secret columns. A disable and a
	// rename that queue behind a registration run one after the other: if the rename wrote the
	// copy it read BEFORE the lock, it would write enabled=true back over the disable that
	// committed in between, and the fleet secret an operator had just shut off would work again.
	// Registrations hold the same lock, so an edit also never lands mid-registration.
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockProfileByToken(tx, updated.Token)
		if err != nil {
			return err
		}
		digest, adopted, err := api.adoptLegacyProvisionSecret(tx, locked)
		if err != nil {
			return err
		}
		if err := apply(locked); err != nil {
			return err
		}
		locked.ProvisionSecretDigest = digest
		if adopted {
			// An old-version pod set this secret, from an operator's own choice: it is no longer the
			// one the server generated, and the endpoint refuses it until it is rotated.
			locked.ProvisionSecretIssued = false
		}
		updated = locked
		return tx.Save(locked).Error
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// adoptLegacyProvisionSecret is the stored secret an update must keep. That is the row's
// digest, UNLESS an old-version pod rotated the secret during a rolling upgrade: it wrote the
// new secret into provision_secret and left the digest alone, so the plaintext column holds the
// later secret. The save that follows writes that column NULL (LegacyProvisionSecret), so the
// plaintext is digested here first rather than discarded, which would bring back the secret the
// old pod rotated away. Outside an upgrade's overlap the column is always NULL and this returns
// the digest unchanged. A digest converted this way is an operator-chosen secret, so the caller
// clears the profile's issued marker (adopted is true): only a rotation marks it again.
//
// The read runs on the update's own transaction, under the profile's row lock, so it cannot
// interleave with a rotation by a current pod. An old-version pod does not take that lock, so
// one that rotates in the gap between this read and the save is still lost; that needs an
// old-version pod to write the same profile within milliseconds of an edit, only during a
// rolling upgrade, and the secret then has to be rotated again.
func (api *Api) adoptLegacyProvisionSecret(db *gorm.DB, profile *ProvisioningProfile) (digest sql.NullString, adopted bool, err error) {
	var legacy []sql.NullString
	if err := db.Model(&ProvisioningProfile{}).Where("id = ?", profile.ID).
		Pluck("provision_secret", &legacy).Error; err != nil {
		return sql.NullString{}, false, err
	}
	if len(legacy) != 1 || !legacy[0].Valid || strings.TrimSpace(legacy[0].String) == "" {
		return profile.ProvisionSecretDigest, false, nil
	}
	digest, err = api.digestStoredProvisionSecret(profile.TenantId, legacy[0].String)
	return digest, err == nil, err
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
