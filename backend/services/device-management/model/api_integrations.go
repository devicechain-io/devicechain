// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Errors returned by integration operations. They are sentinels so a caller can map each
// outcome without string matching.
var (
	// ErrIntegrationAuthFailed is the ONE answer to a presented integration token that does
	// not authenticate: an integration that does not exist, a token that does not match, an
	// empty or oversized token. Nothing distinguishes them in the answer.
	ErrIntegrationAuthFailed = errors.New("integration authentication failed")
	// ErrIntegrationDisabled is the answer to a token that DOES match an integration an
	// operator has switched off. It is returned only after the token has been proven, so it
	// tells a caller nothing it did not already hold; it exists so a disabled integration is
	// reported as such instead of as a credential fault.
	ErrIntegrationDisabled = errors.New("integration is disabled")
	// ErrIntegrationsUnavailable refuses to create, rotate or authenticate when no integration
	// token key is wired: nothing falls back to storing or comparing a token itself.
	ErrIntegrationsUnavailable = errors.New("integrations are unavailable: no integration token key is configured")
)

// MaxIntegrationDeviceTypes bounds an integration's scope. It is far above any real fleet's
// number of device types behind one sender, and below the lookup bound the scope is resolved
// through.
const MaxIntegrationDeviceTypes = 50

// integrationTokenBytes is the entropy of a generated integration token: 256 bits.
const integrationTokenBytes = 32

// generateIntegrationToken is a fresh bearer token: 32 bytes from the system CSPRNG,
// base64url without padding (43 characters). It is the ONLY source of an integration token.
func generateIntegrationToken() (string, error) {
	raw := make([]byte, integrationTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate an integration token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// IntegrationsCeilingResolver reads how many integrations the tenant may hold: its tier's
// setting, else the platform default. A narrow READ seam, and like the other ceilings the
// error travels: it is read when an integration is created, a tier may sit at zero, and so
// there is no number to assume for a tenant whose ceiling cannot be read.
type IntegrationsCeilingResolver interface {
	Resolve(ctx context.Context, tenant string) (int, error)
}

// integrationsCeiling resolves the calling tenant's ceiling. With no resolver wired (a unit
// test, an instance with no user-management coordinate) it is the platform default.
func (api *Api) integrationsCeiling(ctx context.Context) (int, error) {
	if api.IntegrationsCeiling == nil {
		return governance.DefaultIntegrationsPerTenant, nil
	}
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return 0, errors.New("no tenant in context")
	}
	return api.IntegrationsCeiling.Resolve(ctx, tenant)
}

func invalidIntegration(format string, args ...any) error {
	return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
}

// digestIntegrationToken is the stored form of an integration token: its keyed digest, bound
// to the integration's tenant.
func (api *Api) digestIntegrationToken(tenant, token string) (string, error) {
	if api.EnrollmentKeys == nil || api.EnrollmentKeys.Integration == nil {
		return "", ErrIntegrationsUnavailable
	}
	return api.EnrollmentKeys.Integration.Digest(tenant, token)
}

// ------------------------------------------------------------------------------------------
// Validation of the type-dependent fields
// ------------------------------------------------------------------------------------------

// aliasSchemeCustom matches the alias scheme a generic sender names: custom.<name>.
var aliasSchemeCustom = regexp.MustCompile(`^custom\.[a-z0-9][a-z0-9_-]{0,62}$`)

// ValidateIntegrationIdentity checks the identity against the integration's type and returns
// it normalized. A network-server type defaults to DEV_EUI when none is named; a generic
// sender must name its alias scheme.
func ValidateIntegrationIdentity(t IntegrationType, identity *string) (string, error) {
	if identity == nil || strings.TrimSpace(*identity) == "" {
		if t.IsGeneric() {
			return "", invalidIntegration("identity is required for a %s integration: name the alias scheme as %scustom.<name>",
				t, IntegrationIdentityAliasPrefix)
		}
		return IntegrationIdentityDevEUI, nil
	}
	id := *identity
	switch t {
	case IntegrationChirpStackV4:
		if id == IntegrationIdentityDevEUI {
			return id, nil
		}
		return "", invalidIntegration("identity for a %s integration must be %s", t, IntegrationIdentityDevEUI)
	case IntegrationTTSV3:
		if id == IntegrationIdentityDevEUI || id == IntegrationIdentityDeviceID {
			return id, nil
		}
		return "", invalidIntegration("identity for a %s integration must be %s or %s",
			t, IntegrationIdentityDevEUI, IntegrationIdentityDeviceID)
	default:
		scheme, ok := strings.CutPrefix(id, IntegrationIdentityAliasPrefix)
		if !ok || !aliasSchemeCustom.MatchString(scheme) {
			return "", invalidIntegration("identity for a %s integration must be %scustom.<name>, where name is lower-case "+
				"letters, digits, '-' and '_' and at most 63 characters", t, IntegrationIdentityAliasPrefix)
		}
		return id, nil
	}
}

// GenericIntegrationSettings is the settings document of a generic sender. Every pointer is
// an RFC 6901 JSON pointer into the body.
type GenericIntegrationSettings struct {
	// IdFrom names where the device identifier is: "pointer:/a/b" (JSON only),
	// "header:Name" or "query:name". A binary body can only name a header or a query value.
	IdFrom string `json:"idFrom"`
	// ItemsPointer points at an array of items, each an uplink. JSON only.
	ItemsPointer string `json:"itemsPointer,omitempty"`
	// TimePointer points at the sender's own time. Without it the receipt time is used, and
	// a retried delivery is then stored twice. JSON only.
	TimePointer string `json:"timePointer,omitempty"`
	// FramePointer points at an encoded raw frame, stored for a decoder. JSON only.
	FramePointer string `json:"framePointer,omitempty"`
	// FrameEncoding is HEX or BASE64 and is set together with FramePointer.
	FrameEncoding string `json:"frameEncoding,omitempty"`
}

const maxGenericSettingText = 256

var (
	headerNameGrammar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)
	queryNameGrammar  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// pointerGrammar is an RFC 6901 pointer: "/" segments, "~" only as ~0 or ~1.
	pointerGrammar = regexp.MustCompile(`^(/([^/~]|~[01])*)+$`)
)

func validPointer(field, p string) error {
	if len(p) > maxGenericSettingText || !pointerGrammar.MatchString(p) {
		return invalidIntegration("generic.%s must be a JSON pointer such as /device/id (at most %d characters)",
			field, maxGenericSettingText)
	}
	return nil
}

// canonicalGenericSettings parses a generic sender's settings strictly (an unknown key is
// refused, never dropped), validates them against the integration's type, and returns the
// canonical JSON that is stored.
func canonicalGenericSettings(t IntegrationType, raw string) (datatypes.JSON, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var s GenericIntegrationSettings
	if err := dec.Decode(&s); err != nil {
		return nil, invalidIntegration("generic must be a JSON object with the keys idFrom, itemsPointer, timePointer, "+
			"framePointer and frameEncoding: %v", err)
	}
	if dec.More() {
		return nil, invalidIntegration("generic must be one JSON object")
	}
	if len(s.IdFrom) > maxGenericSettingText {
		return nil, invalidIntegration("generic.idFrom is too long (at most %d characters)", maxGenericSettingText)
	}
	kind, name, ok := strings.Cut(s.IdFrom, ":")
	if !ok {
		return nil, invalidIntegration("generic.idFrom must be pointer:/path, header:Name or query:name")
	}
	switch kind {
	case "pointer":
		if t == IntegrationGenericBinary {
			return nil, invalidIntegration("generic.idFrom for a %s integration must be header:Name or query:name: "+
				"a binary body has no fields to point at", t)
		}
		if err := validPointer("idFrom", name); err != nil {
			return nil, err
		}
	case "header":
		if !headerNameGrammar.MatchString(name) {
			return nil, invalidIntegration("generic.idFrom header name %q is not a valid header name", name)
		}
	case "query":
		if !queryNameGrammar.MatchString(name) {
			return nil, invalidIntegration("generic.idFrom query name %q is not a valid query parameter name", name)
		}
	default:
		return nil, invalidIntegration("generic.idFrom must be pointer:/path, header:Name or query:name")
	}
	for _, f := range []struct{ field, value string }{
		{"itemsPointer", s.ItemsPointer}, {"timePointer", s.TimePointer}, {"framePointer", s.FramePointer},
	} {
		if f.value == "" {
			continue
		}
		if t == IntegrationGenericBinary {
			return nil, invalidIntegration("generic.%s is not allowed for a %s integration: the body is not JSON", f.field, t)
		}
		if err := validPointer(f.field, f.value); err != nil {
			return nil, err
		}
	}
	switch {
	case s.FramePointer == "" && s.FrameEncoding != "":
		return nil, invalidIntegration("generic.frameEncoding needs generic.framePointer")
	case s.FramePointer != "" && s.FrameEncoding != "HEX" && s.FrameEncoding != "BASE64":
		return nil, invalidIntegration("generic.frameEncoding must be HEX or BASE64 when generic.framePointer is set")
	}
	out, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(out), nil
}

// genericSettingsFor folds a generic-settings value onto the integration's type: required
// for a generic type, refused for the others.
func genericSettingsFor(t IntegrationType, raw *string) (*datatypes.JSON, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		if t.IsGeneric() {
			return nil, invalidIntegration("generic is required for a %s integration: it names where the device identifier is", t)
		}
		return nil, nil
	}
	if !t.IsGeneric() {
		return nil, invalidIntegration("generic is not allowed for a %s integration", t)
	}
	canon, err := canonicalGenericSettings(t, *raw)
	if err != nil {
		return nil, err
	}
	return &canon, nil
}

// uniqueScopeTokens validates a scope list: at least one token, no more than
// MaxIntegrationDeviceTypes, no blank, no duplicate.
func uniqueScopeTokens(tokens []string) ([]string, error) {
	if len(tokens) == 0 {
		return nil, invalidIntegration("deviceTypeTokens must name at least one device type: an integration with no " +
			"scope resolves nothing (disable the integration to stop it)")
	}
	if len(tokens) > MaxIntegrationDeviceTypes {
		return nil, limit.Exceeded("integration device types", len(tokens), MaxIntegrationDeviceTypes)
	}
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			return nil, invalidIntegration("deviceTypeTokens cannot contain a blank token")
		}
		if _, dup := seen[t]; dup {
			return nil, invalidIntegration("deviceTypeTokens names %q more than once", t)
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// resolveScope finds the device types a scope list names, in the order named. An unknown
// token refuses the whole request.
func (api *Api) resolveScope(ctx context.Context, tokens []string) ([]*DeviceType, error) {
	found, err := api.DeviceTypesByToken(ctx, tokens)
	if err != nil {
		return nil, err
	}
	byToken := make(map[string]*DeviceType, len(found))
	for _, dt := range found {
		byToken[dt.Token] = dt
	}
	out := make([]*DeviceType, 0, len(tokens))
	for _, t := range tokens {
		dt, ok := byToken[t]
		if !ok {
			return nil, fmt.Errorf("%w: device type %q", gorm.ErrRecordNotFound, t)
		}
		out = append(out, dt)
	}
	return out, nil
}

// ------------------------------------------------------------------------------------------
// Writes
// ------------------------------------------------------------------------------------------

// CreateIntegration creates an integration, generating its bearer token. The token is
// returned ONCE, on the created row's IssuedToken; only its digest is stored.
//
// The tenant's integrations ceiling is read first, with no cache, and a tenant whose ceiling
// cannot be read is refused rather than assumed. The count is checked inside the transaction
// the row is created in, after taking a per-tenant advisory lock (lockIntegrationCreates), so
// two creates racing at the ceiling cannot both land.
func (api *Api) CreateIntegration(ctx context.Context, request *IntegrationCreateRequest) (*Integration, error) {
	itype := IntegrationType(request.Type)
	if !itype.Valid() {
		return nil, invalidIntegration("invalid integration type %q: one of %s, %s, %s, %s", request.Type,
			IntegrationChirpStackV4, IntegrationTTSV3, IntegrationGenericJSON, IntegrationGenericBinary)
	}
	identity, err := ValidateIntegrationIdentity(itype, request.Identity)
	if err != nil {
		return nil, err
	}
	generic, err := genericSettingsFor(itype, request.Generic)
	if err != nil {
		return nil, err
	}
	scopeTokens, err := uniqueScopeTokens(request.DeviceTypeTokens)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata)
	if err != nil {
		return nil, err
	}
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := api.resolveScope(ctx, scopeTokens)
	if err != nil {
		return nil, err
	}
	ceiling, err := api.integrationsCeiling(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot create the integration: the tenant's integrations ceiling could not be read: %w", err)
	}

	// The token is the server's: generated here, digested for storage, and shown ONCE, on the
	// row handed back.
	issued, err := generateIntegrationToken()
	if err != nil {
		return nil, err
	}
	digest, err := api.digestIntegrationToken(tenant, issued)
	if err != nil {
		return nil, err
	}
	linkMetrics := request.LinkMetrics != nil && *request.LinkMetrics
	created := &Integration{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		MetadataEntity: rdb.MetadataEntity{Metadata: metadataJSON},
		Type:           string(itype),
		Enabled:        request.Enabled,
		Identity:       identity,
		Generic:        generic,
		LinkMetrics:    linkMetrics,
		TokenDigest:    digest,
		TokenIssuedAt:  time.Now().UTC(),
	}
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockIntegrationCreates(tx, tenant); err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&Integration{}).Count(&n).Error; err != nil {
			return err
		}
		if int(n) >= ceiling {
			return limit.Exceeded("integrations (this tenant's "+governance.IntegrationsPerTenantField+" ceiling)",
				int(n)+1, ceiling)
		}
		if err := tx.Create(created).Error; err != nil {
			return err
		}
		return writeScope(tx, created.ID, scope)
	})
	if err != nil {
		return nil, err
	}
	created.DeviceTypes = scope
	created.IssuedToken = issued
	return created, nil
}

// lockIntegrationCreates serializes the creates of one tenant's integrations for the rest of
// the transaction, so the count the ceiling is checked against is the count the insert lands
// on. On PostgreSQL it is a transaction-scoped advisory lock keyed on the tenant and held for
// the few milliseconds of one create; anywhere else (the SQLite unit-test database, which has
// one writer at a time) there is nothing to take.
func lockIntegrationCreates(tx *gorm.DB, tenant string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tenant+":integrations").Error
}

// writeScope replaces an integration's scope rows with one row per device type.
func writeScope(tx *gorm.DB, integrationId uint, scope []*DeviceType) error {
	if err := tx.Where("integration_id = ?", integrationId).Delete(&IntegrationDeviceType{}).Error; err != nil {
		return err
	}
	rows := make([]IntegrationDeviceType, 0, len(scope))
	for _, dt := range scope {
		rows = append(rows, IntegrationDeviceType{IntegrationId: integrationId, DeviceTypeId: dt.ID})
	}
	return tx.Create(&rows).Error
}

// lockIntegrationByToken reads the integration with this token on tx, locking its row FOR
// UPDATE. A token that names nothing in the tenant is gorm.ErrRecordNotFound.
func lockIntegrationByToken(tx *gorm.DB, token string) (*Integration, error) {
	var locked Integration
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token = ?", token).First(&locked).Error; err != nil {
		return nil, err
	}
	return &locked, nil
}

// RotateIntegrationToken replaces the integration's bearer token with a new one, generated
// here and returned ONCE, on the integration's IssuedToken. It is a hard cutover: the old
// token stops working the moment this commits, with no overlap.
//
// It takes the integration's row lock, so it serializes with an edit: a disable that queues
// behind a rotation is applied to the row the rotation wrote, and the reverse.
func (api *Api) RotateIntegrationToken(ctx context.Context, token string) (*Integration, error) {
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	issued, err := generateIntegrationToken()
	if err != nil {
		return nil, err
	}
	digest, err := api.digestIntegrationToken(tenant, issued)
	if err != nil {
		return nil, err
	}
	var rotated *Integration
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockIntegrationByToken(tx, token)
		if err != nil {
			return err
		}
		locked.TokenDigest = digest
		locked.TokenIssuedAt = time.Now().UTC()
		if err := tx.Save(locked).Error; err != nil {
			return err
		}
		rotated = locked
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := api.loadIntegrationScopes(ctx, []*Integration{rotated}); err != nil {
		return nil, err
	}
	rotated.IssuedToken = issued
	return rotated, nil
}

// UpdateIntegration applies a PARTIAL update: a field the caller did not name keeps its
// stored value, including the token's digest. The type is not editable.
//
// Every field is folded onto the row read UNDER ITS LOCK, so an edit queued behind a disable
// or a rotation cannot write the older copy back over it.
func (api *Api) UpdateIntegration(ctx context.Context, token string, request *IntegrationUpdateRequest) (*Integration, error) {
	matches, err := api.IntegrationsByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	itype := IntegrationType(matches[0].Type)

	// Everything that can refuse resolves before anything is written.
	var newScope []*DeviceType
	if request.DeviceTypeTokens.Set {
		if request.DeviceTypeTokens.Value == nil {
			return nil, invalidIntegration("deviceTypeTokens cannot be cleared: an integration with no scope resolves " +
				"nothing (disable the integration to stop it)")
		}
		tokens, err := uniqueScopeTokens(request.DeviceTypeTokens.Value)
		if err != nil {
			return nil, err
		}
		if newScope, err = api.resolveScope(ctx, tokens); err != nil {
			return nil, err
		}
	}

	apply := func(base *Integration) error {
		enabled, err := request.Enabled.ApplyToRequired("enabled", base.Enabled)
		if err != nil {
			return err
		}
		linkMetrics, err := request.LinkMetrics.ApplyToRequired("linkMetrics", base.LinkMetrics)
		if err != nil {
			return err
		}
		identity := base.Identity
		if request.Identity.Set {
			if request.Identity.Value == nil {
				return invalidIntegration("identity cannot be cleared")
			}
			if identity, err = ValidateIntegrationIdentity(itype, request.Identity.Value); err != nil {
				return err
			}
		}
		generic := base.Generic
		if request.Generic.Set {
			if request.Generic.Value == nil || strings.TrimSpace(*request.Generic.Value) == "" {
				return invalidIntegration("generic cannot be cleared: a %s integration needs it, and the other types hold none", itype)
			}
			if generic, err = genericSettingsFor(itype, request.Generic.Value); err != nil {
				return err
			}
		}
		metadataJSON, err := rdb.JSONInputOf("metadata", request.Metadata.ApplyTo(dcgraphql.MetadataStr(base.Metadata)))
		if err != nil {
			return err
		}
		base.Name = request.Name.ApplyToNullString(base.Name)
		base.Description = request.Description.ApplyToNullString(base.Description)
		base.Metadata = metadataJSON
		base.Enabled = enabled
		base.LinkMetrics = linkMetrics
		base.Identity = identity
		base.Generic = generic
		return nil
	}
	preview := *matches[0]
	if err := apply(&preview); err != nil {
		return nil, err
	}

	var updated *Integration
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockIntegrationByToken(tx, token)
		if err != nil {
			return err
		}
		if err := apply(locked); err != nil {
			return err
		}
		if err := tx.Save(locked).Error; err != nil {
			return err
		}
		if newScope != nil {
			if err := writeScope(tx, locked.ID, newScope); err != nil {
				return err
			}
		}
		updated = locked
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := api.loadIntegrationScopes(ctx, []*Integration{updated}); err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteIntegration hard-deletes an integration by token, with its scope rows, and reports
// whether one was removed. Device aliases are not owned by an integration and are untouched.
func (api *Api) DeleteIntegration(ctx context.Context, token string) (bool, error) {
	deleted := false
	err := api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		locked, err := lockIntegrationByToken(tx, token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.Where("integration_id = ?", locked.ID).Delete(&IntegrationDeviceType{}).Error; err != nil {
			return err
		}
		result := tx.Unscoped().Delete(locked)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected > 0
		return nil
	})
	return deleted, err
}

// ------------------------------------------------------------------------------------------
// Reads
// ------------------------------------------------------------------------------------------

// loadIntegrationScopes fills each integration's DeviceTypes from its scope rows.
func (api *Api) loadIntegrationScopes(ctx context.Context, items []*Integration) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	var rows []IntegrationDeviceType
	if err := api.RDB.DB(ctx).Where("integration_id IN ?", ids).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	typeIds := make([]uint, 0, len(rows))
	for _, r := range rows {
		if !slices.Contains(typeIds, r.DeviceTypeId) {
			typeIds = append(typeIds, r.DeviceTypeId)
		}
	}
	types := map[uint]*DeviceType{}
	for chunk := range slices.Chunk(typeIds, rdb.MaxLookupKeys) {
		found, err := rdb.FindByIds[DeviceType](api.RDB.DB(ctx), chunk)
		if err != nil {
			return err
		}
		for _, dt := range found {
			types[dt.ID] = dt
		}
	}
	byIntegration := map[uint][]*DeviceType{}
	for _, r := range rows {
		if dt, ok := types[r.DeviceTypeId]; ok {
			byIntegration[r.IntegrationId] = append(byIntegration[r.IntegrationId], dt)
		}
	}
	for _, it := range items {
		it.DeviceTypes = byIntegration[it.ID]
		if it.DeviceTypes == nil {
			it.DeviceTypes = []*DeviceType{}
		}
	}
	return nil
}

// Get integrations by id.
func (api *Api) IntegrationsById(ctx context.Context, ids []uint) ([]*Integration, error) {
	found, err := rdb.FindByIds[Integration](api.RDB.DB(ctx), ids)
	if err != nil {
		return nil, err
	}
	return found, api.loadIntegrationScopes(ctx, found)
}

// Get integrations by token.
func (api *Api) IntegrationsByToken(ctx context.Context, tokens []string) ([]*Integration, error) {
	found := make([]*Integration, 0)
	if err := rdb.FindByKeys(api.RDB.DB(ctx), &found, "token", tokens); err != nil {
		return nil, err
	}
	return found, api.loadIntegrationScopes(ctx, found)
}

// Search for integrations that meet criteria.
func (api *Api) Integrations(ctx context.Context, criteria IntegrationSearchCriteria) (*IntegrationSearchResults, error) {
	results := make([]Integration, 0)
	db, pag := api.RDB.ListOf(ctx, &Integration{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Type != nil {
			result = result.Where("type = ?", *criteria.Type)
		}
		if criteria.Enabled != nil {
			result = result.Where("enabled = ?", *criteria.Enabled)
		}
		if criteria.DeviceType != nil {
			result = result.Where("id IN (?)", api.RDB.DB(ctx).Model(&IntegrationDeviceType{}).Select("integration_id").
				Where("device_type_id = (?)", api.RDB.DB(ctx).Model(&DeviceType{}).Select("id").
					Where("token = ?", *criteria.DeviceType)))
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	ptrs := make([]*Integration, len(results))
	for i := range results {
		ptrs[i] = &results[i]
	}
	if err := api.loadIntegrationScopes(ctx, ptrs); err != nil {
		return nil, err
	}
	return &IntegrationSearchResults{Results: results, Pagination: pag}, nil
}

// ------------------------------------------------------------------------------------------
// Authentication
// ------------------------------------------------------------------------------------------

// AuthenticateIntegration proves the presented bearer token belongs to the integration with
// the given token, in the tenant of ctx, and returns it with its scope loaded. It is the
// function the ingest path and the resolve path authenticate an integration through.
//
// Every failure is ErrIntegrationAuthFailed, and the credential compare costs the same: an
// integration that does not exist is compared against the Checker's dummy digest. The
// database work around it differs slightly between a known and an unknown slug (a known one
// also loads its scope). That is acceptable because the slug is not a secret; the token is,
// and nothing about it shows in the answer or in the compare. The compare is the credential Checker's, under KindIntegration,
// constant-time and bound to the tenant: a digest transplanted from another tenant's row
// verifies nothing. A token longer than any this server issues is compared as an empty one,
// so a caller cannot make the compare cost more.
//
// 🔴 A DISABLED INTEGRATION IS NOT AUTHENTICATED. When the token matches an integration that
// is switched off the answer is ErrIntegrationDisabled, returned only after the token has
// been proven, and the integration is returned with it so the caller can report why. Every
// caller that does not look at the error but at the value would be fooled by that, which is
// why the error is never nil for a disabled integration: check the error.
func (api *Api) AuthenticateIntegration(ctx context.Context, integrationToken, presented string) (*Integration, error) {
	if api.IntegrationChecker == nil || api.EnrollmentKeys == nil || api.EnrollmentKeys.Integration == nil {
		return nil, ErrIntegrationsUnavailable
	}
	tenant, err := ctxTenant(ctx)
	if err != nil {
		return nil, err
	}
	if len(presented) > credential.MaxEnrollmentSecretBytes {
		presented = ""
	}
	var matched *Integration
	lookup := func(ctx context.Context) (string, error) {
		var found Integration
		err := api.RDB.DB(ctx).Where("token = ?", integrationToken).First(&found).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return "", nil
		case err != nil:
			return "", err
		}
		matched = &found
		return found.TokenDigest, nil
	}
	err = api.IntegrationChecker.Check(ctx, credential.Principal{
		Kind: credential.KindIntegration, ID: tenant + ":" + integrationToken, Tenant: tenant,
	}, presented, lookup)
	switch {
	case err == nil && matched != nil:
	case err == nil, errors.Is(err, credential.ErrMismatch):
		return nil, ErrIntegrationAuthFailed
	default:
		return nil, err
	}
	if err := api.loadIntegrationScopes(ctx, []*Integration{matched}); err != nil {
		return nil, err
	}
	if !matched.Enabled {
		return matched, ErrIntegrationDisabled
	}
	return matched, nil
}
