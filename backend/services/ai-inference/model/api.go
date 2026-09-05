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
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
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

// validateRequest validates a create request's kind, model, endpoint, and params,
// returning the params as a column value and the endpoint string.
func (api *Api) validateRequest(request *AIProviderCreateRequest) (datatypes.JSON, string, error) {
	return api.validateProviderFields(request.Kind, request.Model, request.Endpoint, request.Params)
}

// validateProviderFields is validateRequest's body, taking the fields directly so the
// UPDATE path can validate what a partial request FOLDS ONTO rather than what it
// carries. kind and endpoint are validated as a PAIR — a kind defined by its address
// has no default to fall back to — so an update naming only one of them still has to be
// checked against the stored other, or a provider could be left addressable by nothing
// and refused only at the first call.
func (api *Api) validateProviderFields(kind, model string, endpoint, params *string) (datatypes.JSON, string, error) {
	if err := validateProviderKind(kind); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(model) == "" {
		return nil, "", errors.New("provider model is required")
	}
	resolvedEndpoint, err := endpointValue(endpoint)
	if err != nil {
		return nil, "", err
	}
	// A kind with no built-in base URL is unusable without one, so it is refused at the
	// write rather than at the first call — the same fail-closed reasoning that keeps an
	// unregistered kind out of the store.
	if err := validateEndpointForKind(AIProviderKind(kind), resolvedEndpoint); err != nil {
		return nil, "", err
	}
	paramsValue, err := paramsJSON(params)
	if err != nil {
		return nil, "", err
	}
	return paramsValue, resolvedEndpoint, nil
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

// UpdateAIProvider partially updates the provider with the given (current) token,
// which is the ONLY thing that names the row — the request carries no token, so
// retargeting a second provider is unrepresentable. Renaming is renameAiProvider's job.
//
// An omitted field leaves the stored value alone, an explicit null clears it, and a
// value sets it. `kind`, `model` and `enabled` sit on NOT NULL columns and refuse a
// null; `endpoint` and `params` clear on one. The secret is write-only, so omitting it
// preserves the stored key, a value rotates it, and a null — or an empty string —
// deletes it.
//
// A provider's GRANTS are not touched here — editing a model and changing who is
// offered it are separate acts with separate audit trails. When expectedUpdatedAt is
// non-nil it is an optimistic-concurrency precondition (ErrConflict if the row moved on
// since).
func (api *Api) UpdateAIProvider(ctx context.Context, token string, request *AIProviderUpdateRequest, expectedUpdatedAt *string) (*AIProvider, error) {
	matches, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	// 🔴 THE FOLD RUNS — AND VALIDATES — BEFORE ANYTHING IS WRITTEN, so a refused null
	// on a required column, or a kind left without the endpoint it needs, rejects the
	// WHOLE update. A caller who retries has not half-applied the first attempt.
	//
	// Every value below is what the row will HOLD, not what the request carried: an
	// update naming only `kind` is validated against the STORED endpoint, and one
	// clearing the endpoint against the STORED kind. Validating the request alone would
	// let `kind: "openai-compatible"` land on a provider with no address.
	kind, err := request.Kind.ApplyToRequired("kind", current.Kind)
	if err != nil {
		return nil, err
	}
	modelID, err := request.Model.ApplyToRequired("model", current.ModelID)
	if err != nil {
		return nil, err
	}
	enabled, err := request.Enabled.ApplyToRequired("enabled", current.Enabled)
	if err != nil {
		return nil, err
	}
	storedEndpoint := current.Endpoint
	storedParams := providerParamsStr(current.Params)
	params, endpoint, err := api.validateProviderFields(kind, modelID,
		request.Endpoint.ApplyTo(&storedEndpoint), request.Params.ApplyTo(storedParams))
	if err != nil {
		return nil, err
	}
	name := rdb.NullStrOf(request.Name.ApplyTo(dcgraphql.NullStr(current.Name)))
	description := rdb.NullStrOf(request.Description.ApplyTo(dcgraphql.NullStr(current.Description)))
	secret := updatedSecret(request.Secret)

	fields := map[string]any{
		"name":        name,
		"description": description,
		"kind":        kind,
		"endpoint":    endpoint,
		"model":       modelID,
		"params":      params,
		"enabled":     enabled,
	}

	// No precondition → unconditional last-write-wins. Save the loaded row (its PK +
	// AuditLabel reach the audit journal, unlike a map Updates) with the new fields.
	if expectedUpdatedAt == nil {
		current.Name = name
		current.Description = description
		current.Kind = kind
		current.Endpoint = endpoint
		current.ModelID = modelID
		current.Params = params
		current.Enabled = enabled
		if err := api.sys(ctx).Save(current).Error; err != nil {
			return nil, err
		}
		return api.reloadWithSecret(ctx, token, current.ID, secret)
	}

	// Optimistic concurrency: a clean early-out, then an ATOMIC guarded write so a
	// concurrent save slipping in between the read and this write moves updated_at and
	// matches zero rows instead of being silently clobbered.
	//
	// 🔴 The layout must match core/graphql.FormatTime, which produced the string the
	// caller echoes back; the guarded write re-reads updated_at, so this comparison is
	// the only enforcement of the CALLER's version. At RFC3339 it enforced it to the
	// whole second.
	if current.UpdatedAt.Format(time.RFC3339Nano) != *expectedUpdatedAt {
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
	// Reload by the token ARGUMENT, which is the only thing that ever named this row.
	// The payload used to carry one, and reloading by IT is how a blank payload token
	// made the caller's own success response read a row it could no longer name.
	return api.reloadWithSecret(ctx, token, current.ID, secret)
}

// RenameAIProvider changes a provider's token, and changes nothing else.
//
// 🔴 IT EXISTS BECAUSE THE RENAME WAS A REAL CAPABILITY THE UPDATE PAYLOAD CARRIED. The
// write-only key handle is keyed by the provider's IMMUTABLE ID (AIProviderSecretRef),
// so a rename keeps it bound — which reloadWithSecret's comment has said in as many
// words since the handle was designed. Grants and function assignments reference the
// provider by its numeric id too, so a rename cannot orphan a tier's menu or a tenant's
// choice either.
//
// The rules, in the order they are applied:
//
//  1. A BLANK new token — empty or WHITESPACE-ONLY — is refused. `token: String!`
//     admits "", and it used to be written straight onto the row, leaving a provider
//     tenants may still be assigned to addressable by nothing.
//  2. newToken == token is an idempotent NO-OP SUCCESS returning the provider, so the
//     retry of a rename that half-failed is safe.
//  3. A token another provider already holds is refused BY NAME, from inside the
//     transaction that does the write. The provider list is INSTANCE-global (no tenant
//     column), so the uniqueness this counts is instance-wide, matching the
//     uix_ai_providers_token index.
//
// The new token is stored VERBATIM, never trimmed: trimming would silently accept
// " primary " as naming "primary", while the token grammar refuses it plainly.
func (api *Api) RenameAIProvider(ctx context.Context, token string, newToken string) (*AIProvider, error) {
	if strings.TrimSpace(newToken) == "" {
		return nil, fmt.Errorf("cannot rename ai provider %q: the new token is blank, and a "+
			"provider named by nothing can never be granted or assigned again", token)
	}

	matches, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	current := matches[0]

	if newToken == current.Token {
		return current, nil
	}

	// 🔴 THE LOOKUP IS THE FAST PATH; THE UNIQUE INDEX IS THE AUTHORITY. Both are made to
	// say the same sentence, and the reason is that the transaction does NOT close the
	// race. At READ COMMITTED a Count that matches nothing takes no lock — there is no
	// row to lock — so two concurrent renames onto one free token both see it free and
	// the loser is stopped by the index instead. Without the translation below it is
	// handed `SQLSTATE 23505` and an index name, which is not what this API promises.
	//
	// The provider list is INSTANCE-global, so this counts across the whole instance,
	// matching uix_ai_providers_token's own scope.
	if err := api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var taken int64
		if err := tx.Model(&AIProvider{}).Where("token = ?", newToken).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return ErrAIProviderTokenTaken(token, newToken)
		}
		// 🔴 ONE COLUMN, NOT THE WHOLE ROW. Saving `current` would rewrite every field
		// from a copy loaded a moment ago, so a concurrent edit landing in that window
		// would be silently reverted by a mutation that changes only a name. This
		// mutation takes no expectedUpdatedAt precondition, so the narrow write is what
		// bounds it. It still passes through the token-grammar callback (a map
		// destination is checked the same way a struct is).
		//
		// 🔴 THE MODEL IS `current`, NOT `&AIProvider{}`, AND THAT IS THE AUDIT JOURNAL.
		// The journal reads its EntityPK off the statement's model, so a zero struct with
		// the id pushed into a Where clause journals an EMPTY primary key — which
		// core/rdb/audit.go defines as "a bulk/condition update". A rename would then be
		// recorded as a bulk operation, and since the label is the token, the entry
		// BEFORE it says `haiku` and this one says `haiku-eu` with nothing tying the two
		// together. On the one mutation whose whole content is that the identifier
		// changed, the PK is the only link between its two labels. Passing the loaded row
		// writes the same single column and keeps it.
		if err := tx.Model(current).Update("token", newToken).Error; err != nil {
			// THE LOSING RACER ARRIVES HERE rather than through the Count above, and it
			// must read exactly as the uncontended refusal does.
			if rdb.IsUniqueViolation(err, aiProviderTokenIndexName, "ai_providers.token") {
				return ErrAIProviderTokenTaken(token, newToken)
			}
			return err
		}
		current.Token = newToken
		return nil
	}); err != nil {
		return nil, err
	}
	return current, nil
}

// ErrAIProviderTokenTaken is the ONE sentence a caller gets when the token they asked for
// belongs to another provider — whether the pre-write lookup found it or the unique index
// did. Both paths are made to say this, because a client cannot be asked to write two
// handlers for one condition that differ only by timing.
func ErrAIProviderTokenTaken(token, newToken string) error {
	return fmt.Errorf("cannot rename ai provider %q to %q: that token is already in use "+
		"by another provider", token, newToken)
}

// aiProviderTokenIndexName is the INSTANCE-global partial unique index the baseline creates
// on ai_providers (token) among live rows. Postgres names it in the text of a unique
// violation, and that name is what distinguishes "this token is taken" from any other write
// failure.
//
// Unlike every tenant-scoped table's index this one is spelled as a literal in
// schema/baseline.go rather than derived from a naming rule, so
// TestAIProviderTokenIndexNameMatchesTheMigration compares the two literals directly.
const aiProviderTokenIndexName = "uix_ai_providers_token"

// providerParamsStr renders a stored params column as the *string the three-state fold
// takes: nil for a NULL column, so the ABSENT reading of a request that says nothing
// about params leaves the column NULL rather than writing an empty document.
func providerParamsStr(params datatypes.JSON) *string {
	if params == nil {
		return nil
	}
	raw := string(params)
	return &raw
}

// updatedSecret folds the three states of the write-only `secret` field onto the
// *string applyProviderSecret takes, which has only two: nil means PRESERVE, and a
// pointer is applied (empty clears, non-empty seals).
//
// 🔴 AN EXPLICIT NULL CLEARS, AND THAT IS A NEW OPERATION SPELLED THE PLATFORM'S WAY.
// Under the old pointer the clear was spelled as the empty STRING — a value standing in
// for an absence, because a pointer had no third state to give it. Now null means what
// it means on every other field: remove the stored value. The empty string keeps
// clearing too; a form's "" and a null are the same intent, and refusing one of them
// would only surprise a caller who had cleared a key the other way yesterday.
func updatedSecret(field dcgraphql.OptionalString) *string {
	if !field.Set {
		return nil
	}
	if field.Value == nil {
		cleared := ""
		return &cleared
	}
	return field.Value
}

// reloadWithSecret applies the write-only secret (keyed by the provider's immutable
// id, so a rename never orphans it) and returns the freshly-reloaded provider (for the
// bumped updated_at).
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

	// A plain predicate on the token, with no second guard on the DELETE itself. This
	// deliberately dropped a `is_platform_baseline = false` predicate (and its
	// ErrProviderInUse branch) that guarded the retired instance-wide baseline, and the
	// reason it is now UNNECESSARY rather than merely tidier is worth being precise
	// about, since removing a check reads like weakening one:
	//
	// The default now rides a GRANT ROW (AIProviderTierGrant.IsDefault), not a column on
	// the provider's own row. So the case the predicate closed — a designation landing
	// between assertProviderNotGranted and this DELETE, which nothing else would object to
	// because nothing else referenced it — cannot arise. Marking a provider as a tier's
	// default requires it to be granted to that tier first (SetTierDefault refuses
	// otherwise), a grant row carries provider_id with ON DELETE RESTRICT, and the grant
	// arm of assertProviderNotGranted already refuses on it. The database enforces the
	// race the hand-written predicate was covering: the protection that had to be
	// hand-written is now the protection that was already there.
	res := api.sys(ctx).Unscoped().Where("token = ?", token).Delete(&AIProvider{})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		// It existed a moment ago and is gone by another writer's hand, taking its key with
		// it. Report the delete we did not perform as false rather than claiming it.
		return false, nil
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
