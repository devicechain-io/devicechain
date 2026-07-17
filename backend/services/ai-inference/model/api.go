// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// maxParamsBytes caps a stored provider params document. Params is a small tuning
// document (max tokens, temperature, …), not a blob, so 16 KiB is generous.
const maxParamsBytes = 16 << 10

// ErrInvalidParams is returned when a create/update carries Params that is not a
// well-formed JSON object.
var ErrInvalidParams = errors.New("provider params must be a JSON object")

// ErrParamsTooLarge is returned when Params exceeds maxParamsBytes.
var ErrParamsTooLarge = errors.New("provider params exceeds the maximum size")

// ErrInvalidEndpoint is returned when a create/update carries an Endpoint that is
// not a well-formed absolute http(s) URL.
var ErrInvalidEndpoint = errors.New("provider endpoint must be an absolute http(s) URL")

// ErrConflict is returned by UpdateAIProvider when the caller passes the version it
// edited (expectedUpdatedAt) and the row has moved on since — a concurrent edit.
var ErrConflict = errors.New("provider was modified by another writer; reload and try again")

// Api is the ai-inference persistence surface: the instance-scoped AIProvider list
// plus each provider's write-only API key (sealed in the ADR-059 secret store).
// Secrets is required — a provider's key is never a column.
type Api struct {
	RDB     *rdb.RdbManager
	Secrets secrets.SecretStore
}

// NewApi creates a new API instance around the rdb manager and secret store.
func NewApi(rdb *rdb.RdbManager, store secrets.SecretStore) *Api {
	return &Api{RDB: rdb, Secrets: store}
}

// sys returns a gorm handle in the instance-global system context. AIProvider is
// not tenant-scoped, so the tenant-scope callback is a no-op for it; running in the
// system context is the same lane iam.Store and settings.Store use for their
// instance-global rows, kept for consistency.
func (api *Api) sys(ctx context.Context) *gorm.DB {
	return api.RDB.DB(core.WithSystemContext(ctx))
}

// paramsJSON validates that raw is a well-formed, size-bounded JSON object (or
// empty/whitespace → null) and returns it as a column value. Empty is allowed (a
// provider need not carry params); a non-object non-empty value is rejected.
func paramsJSON(raw *string) (datatypes.JSON, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := bytes.TrimSpace([]byte(*raw))
	if len(trimmed) == 0 {
		return nil, nil
	}
	if len(trimmed) > maxParamsBytes {
		return nil, ErrParamsTooLarge
	}
	if !json.Valid(trimmed) || trimmed[0] != '{' {
		return nil, ErrInvalidParams
	}
	return datatypes.JSON(trimmed), nil
}

// endpointValue validates an optional endpoint override and returns it as a column
// string ("" when absent). A present value must be an absolute http(s) URL — the
// operator sets it (ai:admin), and the actual outbound call (slice 0c) is
// SSRF-guarded there, but a typo is caught here at write.
func endpointValue(raw *string) (string, error) {
	if raw == nil {
		return "", nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrInvalidEndpoint
	}
	// Reject a query or fragment: the endpoint is a BASE URL onto which the provider
	// impl splices its API path (e.g. /v1/messages), so a query/fragment would produce a
	// silently broken URL. Credentials belong in the ADR-059 secret handle, never here.
	if u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidEndpoint
	}
	return v, nil
}

// validateRequest validates a create/update request's kind, model, endpoint, and
// params, returning the params as a column value and the endpoint string.
func (api *Api) validateRequest(request *AIProviderCreateRequest) (datatypes.JSON, string, error) {
	if err := validateProviderKind(request.Kind); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, "", errors.New("provider model is required")
	}
	endpoint, err := endpointValue(request.Endpoint)
	if err != nil {
		return nil, "", err
	}
	params, err := paramsJSON(request.Params)
	if err != nil {
		return nil, "", err
	}
	return params, endpoint, nil
}

// CreateAIProvider inserts a new provider. The kind must be registered and the model
// present; a non-empty request.Secret (the API key) is sealed into the secret store
// under the provider's handle (never a column). A new provider is offered to no one
// until it is granted to a tier (ADR-065) — registering a model and selling it are
// separate acts, so an operator can add and smoke-test a provider without it
// appearing on any tenant's menu.
func (api *Api) CreateAIProvider(ctx context.Context, request *AIProviderCreateRequest) (*AIProvider, error) {
	params, endpoint, err := api.validateRequest(request)
	if err != nil {
		return nil, err
	}

	created := &AIProvider{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		Kind:     request.Kind,
		Endpoint: endpoint,
		ModelID:  request.Model,
		Params:   params,
		Enabled:  request.Enabled,
	}
	if err := api.sys(ctx).Create(created).Error; err != nil {
		return nil, err
	}
	// Seal the key under the provider's handle. The row is written first so its
	// immutable id (the secret's stable key) exists.
	if err := api.applyProviderSecret(ctx, created.ID, request.Secret); err != nil {
		// Roll the row back (best effort) so the create is atomic from the caller's
		// view — otherwise a retry would collide on the now-existing token.
		if delErr := api.sys(ctx).Unscoped().Delete(created).Error; delErr != nil {
			log.Warn().Err(delErr).Str("token", request.Token).
				Msg("Failed to roll back provider row after secret write failure; provider may exist without a key")
		}
		return nil, err
	}
	return created, nil
}

// UpdateAIProvider replaces the provider with the given (current) token. The secret
// is write-only: a nil request.Secret preserves the stored key, a non-nil value
// replaces it, and an explicit empty string clears it. A provider's GRANTS are not
// touched here — editing a model and changing who is offered it are separate acts
// with separate audit trails. When expectedUpdatedAt is non-nil it is an
// optimistic-concurrency precondition (ErrConflict if the row moved on since).
func (api *Api) UpdateAIProvider(ctx context.Context, token string, request *AIProviderCreateRequest, expectedUpdatedAt *string) (*AIProvider, error) {
	params, endpoint, err := api.validateRequest(request)
	if err != nil {
		return nil, err
	}

	matches, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	fields := map[string]any{
		"token":       request.Token,
		"name":        rdb.NullStrOf(request.Name),
		"description": rdb.NullStrOf(request.Description),
		"kind":        request.Kind,
		"endpoint":    endpoint,
		"model":       request.Model,
		"params":      params,
		"enabled":     request.Enabled,
	}

	// No precondition → unconditional last-write-wins. Save the loaded row (its PK +
	// AuditLabel reach the audit journal, unlike a map Updates) with the new fields.
	if expectedUpdatedAt == nil {
		current.Token = request.Token
		current.Name = rdb.NullStrOf(request.Name)
		current.Description = rdb.NullStrOf(request.Description)
		current.Kind = request.Kind
		current.Endpoint = endpoint
		current.ModelID = request.Model
		current.Params = params
		current.Enabled = request.Enabled
		if err := api.sys(ctx).Save(current).Error; err != nil {
			return nil, err
		}
		return api.reloadWithSecret(ctx, request.Token, current.ID, request.Secret)
	}

	// Optimistic concurrency: a clean early-out, then an ATOMIC guarded write so a
	// concurrent save slipping in between the read and this write moves updated_at and
	// matches zero rows instead of being silently clobbered.
	if current.UpdatedAt.Format(time.RFC3339) != *expectedUpdatedAt {
		return nil, ErrConflict
	}
	res := api.sys(ctx).Model(&AIProvider{}).
		Where("id = ? AND updated_at = ?", current.ID, current.UpdatedAt).
		Updates(fields)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrConflict
	}
	return api.reloadWithSecret(ctx, request.Token, current.ID, request.Secret)
}

// reloadWithSecret applies the write-only secret (keyed by the provider's immutable
// id, so a token rename in the same update keeps the key bound) and returns the
// freshly-reloaded provider (for the bumped updated_at).
func (api *Api) reloadWithSecret(ctx context.Context, token string, id uint, secret *string) (*AIProvider, error) {
	if secret != nil {
		if err := api.applyProviderSecret(ctx, id, secret); err != nil {
			return nil, err
		}
	}
	reloaded, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(reloaded) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return reloaded[0], nil
}

// applyProviderSecret writes the provider's API key to the store to match the
// request: a non-empty value is sealed (Put), an explicit empty string clears it
// (Delete, idempotent). A nil secret is a caller decision made above (preserve) and
// never reaches here. Keyed by the provider's immutable id, instance-scoped. The
// request ctx is threaded through (not context.Background) so the operator's claims
// reach the audit journal for this — the most sensitive mutation in the service; the
// store overrides to the system lane for an instance-scoped ref, so no tenant leaks.
func (api *Api) applyProviderSecret(ctx context.Context, id uint, secret *string) error {
	if secret == nil {
		return nil
	}
	ref := AIProviderSecretRef(id)
	if *secret == "" {
		return api.Secrets.Delete(ctx, ref)
	}
	return api.Secrets.Put(ctx, ref, []byte(*secret))
}

// AIProvidersByToken looks up providers by their current tokens.
func (api *Api) AIProvidersByToken(ctx context.Context, tokens []string) ([]*AIProvider, error) {
	found := make([]*AIProvider, 0)
	result := api.sys(ctx).Find(&found, "token in ?", tokens)
	if result.Error != nil {
		return nil, result.Error
	}
	return found, nil
}

// AIProviders searches providers by criteria (kind filter + pagination).
func (api *Api) AIProviders(ctx context.Context, criteria AIProviderSearchCriteria) (*AIProviderSearchResults, error) {
	results := make([]AIProvider, 0)
	db, pag := api.RDB.ListOf(core.WithSystemContext(ctx), &AIProvider{}, func(result *gorm.DB) *gorm.DB {
		if criteria.Kind != nil {
			result = result.Where("kind = ?", *criteria.Kind)
		}
		return result
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AIProviderSearchResults{Results: results, Pagination: pag}, nil
}

// DeleteAIProvider hard-deletes the provider with the given token and its stored key,
// reporting whether a row was deleted. Hard delete (Unscoped): a provider has no
// trash/restore semantics.
//
// A provider that any tier or tenant is still granted is REFUSED (ErrProviderInUse,
// naming the grants). This is the ADR-044 ErrEntityInUse shape user-management already
// uses to protect a tier that tenants reference, and it is the refusal that makes
// provider deletion safe: cascading instead would let one delete silently empty a
// tier's menu and strip AI from every tenant at that tier within the governance cache
// TTL, with nothing in the operator's way. The database backs this up — provider_id
// carries ON DELETE RESTRICT and the delete below is a real DELETE, so the constraint
// genuinely fires — but the check runs first so the operator gets a legible message
// instead of a constraint violation.
func (api *Api) DeleteAIProvider(ctx context.Context, token string) (bool, error) {
	matches, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return false, err
	}
	if len(matches) == 0 {
		return false, nil
	}
	id := matches[0].ID

	if err := api.assertProviderNotGranted(ctx, id); err != nil {
		return false, err
	}

	// The `is_platform_baseline = false` predicate closes the gap between the check above
	// and this delete. Grants and assignments are backstopped by their FKs (ON DELETE
	// RESTRICT), so a concurrent grant cannot slip past assertProviderNotGranted — but
	// the baseline designation is a column on THIS row, so no constraint objects to
	// deleting the provider a SetPlatformBaseline just designated. Losing the baseline
	// silently drops every non-choosing tenant to NONE, so the refusal is worth
	// enforcing where the write actually lands rather than only where we looked.
	res := api.sys(ctx).Unscoped().
		Where("token = ? AND is_platform_baseline = ?", token, false).
		Delete(&AIProvider{})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// It existed a moment ago and is not gone by our hand: it was designated the
		// baseline in between. Report the same legible refusal the check would have.
		return false, fmt.Errorf("%w: it is the platform baseline model", ErrProviderInUse)
	}

	// Remove the provider's key so a deleted provider leaves no orphaned secret
	// (Delete is idempotent). The row is already hard-deleted, so a failure to remove
	// the (now unreachable) secret must not report the provider as undeleted: log and
	// continue. Orphaned ciphertext is benign — ids are never recycled.
	if err := api.Secrets.Delete(ctx, AIProviderSecretRef(id)); err != nil {
		log.Warn().Err(err).Str("token", token).
			Msg("Deleted provider but failed to remove its stored key (orphaned ciphertext)")
	}
	return true, nil
}
