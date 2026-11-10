// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

// ResourceClass is what a requested RFC 8707 `resource` is, relative to the instance's
// allowlist. The set is closed (it is a metric label), so the raw resource string is never a
// label value.
type ResourceClass string

const (
	// ResNone is no resource at all. An empty value is the same as an absent one.
	ResNone ResourceClass = "none"
	// ResArea is a platform area's resource identifier (base + "/" + area).
	ResArea ResourceClass = "area"
	// ResMCP is a registered MCP resource.
	ResMCP ResourceClass = "mcp"
	// ResExternal is an operator-declared resource server outside the platform.
	ResExternal ResourceClass = "external"
	// ResUnlisted is a well-formed resource that is on no list.
	ResUnlisted ResourceClass = "unlisted"
	// ResMalformed is a resource that is not an acceptable identifier at all.
	ResMalformed ResourceClass = "malformed"
	// ResMultiple is more than one resource in a request that may carry one.
	ResMultiple ResourceClass = "multiple"
)

// Issuable reports whether the authorization server may bind a token to a resource of this
// class: no resource, or one on the allowlist.
func (c ResourceClass) Issuable() bool {
	switch c {
	case ResNone, ResArea, ResMCP, ResExternal:
		return true
	}
	return false
}

// ResourceAllowlist is rule 1: the resources the authorization server may issue a token for.
// It is built from the same instance document every plane reads, so what the AS issues for
// and what a plane accepts cannot drift apart. It is immutable and safe for concurrent use.
//
// Matching is byte-exact, with no normalisation: validation forces one spelling into the
// configuration, and any other spelling a client sends is unlisted.
type ResourceAllowlist struct {
	mode config.TokenAudienceMode
	area map[string]struct{}
	mcp  map[string]struct{}
	ext  map[string]struct{}
}

// NewResourceAllowlist builds the allowlist from the instance's token-audience block. It
// validates resource identifiers and modes again, retaining programmatic observe
// compatibility. Service startup validates the document before calling this constructor.
//
// allowed = {AreaResource(base, a) : a in AreaAudiences} + mcpResources + externalResources.
// With no base there are no area entries.
func NewResourceAllowlist(cfg config.TokenAudienceConfiguration) (*ResourceAllowlist, error) {
	if err := validateAudiencePolicy(cfg); err != nil {
		return nil, fmt.Errorf("infrastructure.tokenAudience: %w", err)
	}
	a := &ResourceAllowlist{
		mode: cfg.EffectiveEnforcement(),
		area: map[string]struct{}{},
		mcp:  map[string]struct{}{},
		ext:  map[string]struct{}{},
	}
	for _, area := range config.AreaAudiences() {
		if r := config.AreaResource(cfg.AreaBaseUrl, area); r != "" {
			a.area[r] = struct{}{}
		}
	}
	for _, r := range cfg.McpResources {
		if r != "" {
			a.mcp[r] = struct{}{}
		}
	}
	for _, r := range cfg.ExternalResources {
		if r != "" {
			a.ext[r] = struct{}{}
		}
	}
	return a, nil
}

// Mode is the effective enforcement mode.
func (a *ResourceAllowlist) Mode() config.TokenAudienceMode { return a.mode }

// Classify returns the class of one resource by membership. It does not judge syntax or
// multiplicity: the caller owns those, because a request type decides them (the
// authorization endpoint counts them, client_credentials already refuses them). Empty is
// none.
func (a *ResourceAllowlist) Classify(resource string) ResourceClass {
	if resource == "" {
		return ResNone
	}
	if _, ok := a.area[resource]; ok {
		return ResArea
	}
	if _, ok := a.mcp[resource]; ok {
		return ResMCP
	}
	if _, ok := a.ext[resource]; ok {
		return ResExternal
	}
	return ResUnlisted
}
