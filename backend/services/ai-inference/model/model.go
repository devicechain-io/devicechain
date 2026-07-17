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
// (ADR-056 §4). It is deliberately NOT tenant-scoped: the provider list + its API
// keys are an operator concern (the hosted offering's shared key, or a self-hosted
// endpoint). A tenant does not own providers; it is OFFERED a menu of them by its
// tier (ADR-065 — see AIProviderTierGrant) and separately CONSENTS to external
// routing (the per-tenant ai_external_enabled flag on user-management, ADR-056 §6).
// Those are two different questions — "which models may I choose from" and "may my
// data leave the boundary" — and they compose: a model is usable iff granted AND
// (in-boundary OR consented).
//
// There is no tenant BYOK: a customer wanting its own keys buys a dedicated instance
// (ADR-065 decision 12, superseding ADR-059's "instance default + tenant BYOK"
// phrasing), which is where every other per-tenant-infrastructure request already
// lands.
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
	// provider stays in the list (and may still be granted to tiers) but never serves
	// a call, so an operator can take a model out of service without unpicking its
	// packaging. No gorm `default`: a `default:true` would make gorm write the DB
	// default for the zero value (false) on Create, so a provider could never be
	// persisted DISABLED — the `enabled: Boolean!` GraphQL contract always sends an
	// explicit value anyway.
	Enabled bool `gorm:"not null"`
	// IsPlatformBaseline designates this provider as the instance's baseline model: the
	// one a tenant gets for a function it never assigned, PROVIDED the tenant is
	// entitled to it (see Api.ResolveModelForFunction). At most one instance-wide
	// (uix_ai_providers_baseline backs it).
	//
	// It is a lowest-common-denominator, NOT a free tier. It is checked against the
	// tenant's menu like any other candidate, so a tenant whose tier grants nothing gets
	// nothing — AI is a tiered entitlement, and a baseline that bypassed the menu would
	// invert that for every unpackaged tenant on the instance.
	//
	// A STORED designation, never an inference. "The sole enabled provider" or "the
	// first one registered" would re-answer as the provider list changes, which is the
	// non-monotonic shape that cost this service five bugs (see function.go).
	//
	// No gorm `default` tag: a `default:false` would make gorm substitute the DB default
	// for the Go zero value, which is the shape that made Enabled unpersistable-as-false.
	IsPlatformBaseline bool `gorm:"not null"`
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
