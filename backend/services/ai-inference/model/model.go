// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AIProvider is an INSTANCE-scoped, operator-managed inference-provider config
// (ADR-056 §4). It is deliberately NOT tenant-scoped: at GA the provider list +
// its API keys are an operator concern (the hosted offering's shared key, or a
// self-hosted endpoint), and a tenant only CONSENTS to external routing (the
// per-tenant ai_external_enabled flag on user-management, ADR-056 §6). Tenant BYOK
// is a documented post-GA fast-follow — the secret store + the resolver already
// take a scope, so adding tenant rows later is additive.
//
// The row holds no cleartext credential: the API key is sealed in the ADR-059
// secret store under the provider's immutable id (AIProviderSecretRef) and never
// returned across the API (the read side exposes only hasSecret).
//
// A provider is addressed by Kind + Endpoint + Model, so the service is
// provider-agnostic from day one: `anthropic` is the only Kind with a shipped
// Provider impl at GA, but the entity already carries an Endpoint (base URL) so a
// self-hosted / openai-compatible model lands as a new Kind + impl with no schema
// change (ADR-056 — "still need to deploy our own models on the same interface").
type AIProvider struct {
	gorm.Model
	rdb.TokenReference
	rdb.NamedEntity

	// Kind selects the Provider implementation. One of the registered AIProviderKind
	// vocabulary (`anthropic` at GA); validated against it at write.
	Kind string `gorm:"not null;size:64"`
	// Endpoint is the provider API base URL. Empty means the Kind's built-in default
	// (for `anthropic`, the public Anthropic API). Set it to target a self-hosted or
	// proxied endpoint. An http(s) URL, validated at write.
	Endpoint string `gorm:"size:512"`
	// ModelID is the provider model id (e.g. "claude-opus-4-8"). Named ModelID (not
	// Model) so it does not collide with the embedded gorm.Model; the DB column stays
	// `model` via the column tag.
	ModelID string `gorm:"column:model;not null;size:128"`
	// Params is the opaque, per-Kind inference config (max tokens, temperature, …) as
	// a JSON object, or null. The Provider impl (slice 0c) interprets it.
	Params datatypes.JSON
	// Enabled gates whether this provider may be resolved and used. A disabled
	// provider stays in the list (and can be the active one) but never serves a call.
	Enabled bool `gorm:"not null;default:true"`
	// Active marks THE one provider used by default for inference. At most one live
	// row is active at a time (enforced by a partial unique index + a transactional
	// SetActiveProvider); zero active is the valid "default of none" — with no active
	// provider the feature is simply unavailable (fail-closed).
	Active bool `gorm:"not null;default:false"`
}

func (AIProvider) TableName() string { return "ai_providers" }

// AIProviderSecretName is the stable per-provider secret handle, keyed by the
// provider's IMMUTABLE numeric id (not its token) so a token rename keeps the same
// key bound — the `ai/provider/{id}` scheme ADR-059 reserved for this consumer.
func AIProviderSecretName(id uint) string {
	return fmt.Sprintf("ai/provider/%d", id)
}

// AIProviderSecretRef builds the INSTANCE-scoped SecretRef for a provider's API
// key. Unlike the tenant-scoped connector ref it carries no tenant (the provider
// list is instance-global), so it needs no context and never fails — the scope is
// fixed at instance by construction.
func AIProviderSecretRef(id uint) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeInstance, Name: AIProviderSecretName(id)}
}

// AIProviderCreateRequest is the data to create or update a provider. Params is the
// raw JSON params document (validated by the API layer). Secret (the API key) is
// write-only: a nil value preserves the stored key on update (the caller cannot read
// it back to resend it); a non-nil value replaces it, and an explicit empty string
// clears it. On create, nil/empty means no key yet.
type AIProviderCreateRequest struct {
	Token       string
	Name        *string
	Description *string
	Kind        string
	Endpoint    *string
	Model       string // the provider model id → AIProvider.ModelID
	Params      *string
	Enabled     bool
	Secret      *string
}

// AIProviderSearchCriteria is the filter/pagination for a provider search.
type AIProviderSearchCriteria struct {
	rdb.Pagination
	Kind *string
}

// AIProviderSearchResults is a page of providers plus its pagination info.
type AIProviderSearchResults struct {
	Results    []AIProvider
	Pagination rdb.SearchResultsPagination
}
