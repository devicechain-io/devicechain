// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-user-management/iam"
)

// THE REGISTRY OF CONVERTED ADMIN-PLANE FAMILIES.
//
// Every entity whose update mutation carries the platform-wide partial-update semantic
// is declared here, once, and core's harness drives all of its properties against every
// one. Converting the next family is adding a row here.
//
// 🔴 The `seeded` values in each field list are what the family's seed must actually
// write. They are not documentation: SeedPopulatesEveryFieldDistinctly reads the row
// back and fails if the two disagree, because a fixture that does not hold what the
// table claims makes "the update preserved it" unobservable.
//
// This service's fifth converted family, PROFILE, is deliberately absent: it belongs to
// *identity.Manager, not to *admin.Service, and lives in that package's suite. That
// absence is not left to this comment to enforce — TestEveryUpdateTakesADedicatedRequest
// enumerates each service's own Update* methods separately, so a family missing from
// EITHER registry fails there.
func partialUpdateFamilies() []putest.Family[*Service] {
	return []putest.Family[*Service]{
		roleFamily(),
		tenantFamily(),
		tenantTierFamily(),
		oauthClientFamily(),
	}
}

// ─── shared fixture values ─────────────────────────────────────────────────

const (
	// The two tier tokens the tenant family needs: the one it is seeded at, and the one
	// the re-tier property moves it to. tierToken is a RequiredRef, so the harness also
	// drives an unknown token through it and requires the whole update to be refused.
	seededTierToken = "gold-fixture"
	otherTierToken  = "silver-fixture"
)

func roleFamily() putest.Family[*Service] {
	const token = "ops-role"
	scope := string(iam.ScopeSystem)

	// Both sets are SYSTEM-tier authorities, because the role is system-scoped and
	// validateAuthorities refuses a tenant-tier authority on a system role. Getting this
	// wrong would make every property fail on an authority error rather than on anything
	// to do with partial updates.
	seededAuthorities := []string{"role:read", "tenant:read"}
	replaceAuthorities := []string{"client:write"}

	return putest.Family[*Service]{
		Name:    "role",
		Token:   token,
		Migrate: []any{&iam.Role{}},
		Seed: func(t *testing.T, s *Service, ctx context.Context) {
			if _, err := s.CreateRole(ctx, RoleInput{
				Scope: scope, Token: token,
				Name: "Ops", Description: "The operations role", Authorities: seededAuthorities,
			}); err != nil {
				t.Fatalf("seed role: %v", err)
			}
		},
		Read: func(t *testing.T, s *Service, ctx context.Context) map[string]string {
			r, err := s.iam.RoleByScopeToken(ctx, iam.ScopeSystem, token)
			if err != nil {
				t.Fatalf("reload role: %v", err)
			}
			return map[string]string{
				"name":        nullStr(r.Name),
				"description": nullStr(r.Description),
				"authorities": renderList(r.Authorities),
			}
		},
		NewRequest: func() any { return new(RoleUpdateRequest) },
		Update: func(s *Service, ctx context.Context, token string, req any) error {
			_, err := s.UpdateRole(ctx, scope, token, req.(*RoleUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "Ops", "Operations",
				func(r *RoleUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", "The operations role", "Rewritten",
				func(r *RoleUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			// Clearable, which for a list means "emptied" — see RoleUpdateRequest for why
			// an empty authority set is a legal role here and not for an OAuth client.
			putest.OptionalStringListField("authorities", seededAuthorities, replaceAuthorities,
				func(r *RoleUpdateRequest) *dcgraphql.OptionalStringList { return &r.Authorities }),
		},
	}
}

// tenantFamily is the widest of the four and the one the governance decision lives in:
// eleven of its fifteen fields are ADR-023 overrides whose nil ALREADY meant "inherit
// the platform default, never unlimited", so all three states have to reach the column
// rather than two of them collapsing.
func tenantFamily() putest.Family[*Service] {
	const token = "acme-co"

	seededConfig := `{"fleet":"north"}`
	replaceConfig := `{"fleet":"south"}`

	return putest.Family[*Service]{
		Name:    "tenant",
		Token:   token,
		Migrate: []any{&iam.TenantTier{}, &iam.Tenant{}},
		Seed: func(t *testing.T, s *Service, ctx context.Context) {
			for _, tier := range []string{seededTierToken, otherTierToken} {
				if _, err := s.CreateTenantTier(ctx, TierInput{Token: tier}); err != nil {
					t.Fatalf("seed tier %s: %v", tier, err)
				}
			}
			cfg, err := ParseConfigJSON(&seededConfig)
			if err != nil {
				t.Fatalf("seed config: %v", err)
			}
			enabled := true
			if _, err := s.CreateTenant(ctx, TenantInput{
				Token: token, Name: "Acme", TierToken: seededTierToken, Config: cfg,
				AiExternalEnabled: &enabled,
				// Every value sits inside 1–100, which is the intersection of every
				// validator these eleven fields carry: rates and bursts must be positive,
				// shedPriority must be a point on the 1–100 band, and the three geofence
				// caps must not exceed their platform maxima. Distinct matters more than
				// large — a repeated value would let "this field was written" be satisfied
				// by a neighbour's.
				GovernanceOverrides: GovernanceOverrides{
					IngestMessagesPerSecond:      fptr(11),
					IngestBurst:                  iptr(12),
					OutboundMessagesPerSecond:    fptr(13),
					OutboundBurst:                iptr(14),
					AiInferenceRequestsPerMinute: fptr(15),
					AiInferenceBurst:             iptr(16),
					ShedPriority:                 iptr(17),
					HeldCommandCeiling:           iptr(18),
					GeoFencePositionCeiling:      iptr(19),
					GeoFenceCeiling:              iptr(20),
					GeoFencePositionBudget:       iptr(21),
				},
			}); err != nil {
				t.Fatalf("seed tenant: %v", err)
			}
		},
		Read: func(t *testing.T, s *Service, ctx context.Context) map[string]string {
			tn, err := s.iam.TenantByToken(ctx, token)
			if err != nil {
				t.Fatalf("reload tenant: %v", err)
			}
			if tn.Tier == nil {
				t.Fatal("the tenant reloaded without its tier preloaded, so the tierToken " +
					"reading below would report the same thing for every tier")
			}
			return map[string]string{
				"name":                         nullStr(tn.Name),
				"tierToken":                    tn.Tier.Token,
				"config":                       configJSON(tn.Config),
				"aiExternalEnabled":            nullBoolStr(tn.AiExternalEnabled),
				"ingestMessagesPerSecond":      nullFloatStr(tn.IngestMessagesPerSecond),
				"ingestBurst":                  nullIntStr(tn.IngestBurst),
				"outboundMessagesPerSecond":    nullFloatStr(tn.OutboundMessagesPerSecond),
				"outboundBurst":                nullIntStr(tn.OutboundBurst),
				"aiInferenceRequestsPerMinute": nullFloatStr(tn.AiInferenceRequestsPerMinute),
				"aiInferenceBurst":             nullIntStr(tn.AiInferenceBurst),
				"shedPriority":                 nullIntStr(tn.ShedPriority),
				"heldCommandCeiling":           nullIntStr(tn.HeldCommandCeiling),
				"geoFencePositionCeiling":      nullIntStr(tn.GeoFencePositionCeiling),
				"geoFenceCeiling":              nullIntStr(tn.GeoFenceCeiling),
				"geoFencePositionBudget":       nullIntStr(tn.GeoFencePositionBudget),
			}
		},
		NewRequest: func() any { return new(TenantUpdateRequest) },
		Update: func(s *Service, ctx context.Context, token string, req any) error {
			_, err := s.UpdateTenant(ctx, token, req.(*TenantUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "Acme", "Acme Industrial",
				func(r *TenantUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			// A RequiredRef: settable, an explicit null REFUSED (the FK is NOT NULL), and
			// an unknown token refuses the whole update rather than leaving the tenant
			// half-edited at a tier that does not exist.
			putest.RequiredRefField("tierToken", seededTierToken, otherTierToken,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalString { return &r.TierToken }),
			putest.OptionalStringField("config", seededConfig, replaceConfig,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalString { return &r.Config }),
			optionalBoolField("aiExternalEnabled", true,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalBool { return &r.AiExternalEnabled }),
			putest.OptionalFloat64Field("ingestMessagesPerSecond", 11, 31,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalFloat64 { return &r.IngestMessagesPerSecond }),
			putest.OptionalInt32Field("ingestBurst", 12, 32,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.IngestBurst }),
			putest.OptionalFloat64Field("outboundMessagesPerSecond", 13, 33,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalFloat64 { return &r.OutboundMessagesPerSecond }),
			putest.OptionalInt32Field("outboundBurst", 14, 34,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.OutboundBurst }),
			putest.OptionalFloat64Field("aiInferenceRequestsPerMinute", 15, 35,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalFloat64 { return &r.AiInferenceRequestsPerMinute }),
			putest.OptionalInt32Field("aiInferenceBurst", 16, 36,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.AiInferenceBurst }),
			putest.OptionalInt32Field("shedPriority", 17, 37,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.ShedPriority }),
			putest.OptionalInt32Field("heldCommandCeiling", 18, 38,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.HeldCommandCeiling }),
			putest.OptionalInt32Field("geoFencePositionCeiling", 19, 39,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.GeoFencePositionCeiling }),
			putest.OptionalInt32Field("geoFenceCeiling", 20, 40,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.GeoFenceCeiling }),
			putest.OptionalInt32Field("geoFencePositionBudget", 21, 41,
				func(r *TenantUpdateRequest) *dcgraphql.OptionalInt32 { return &r.GeoFencePositionBudget }),
		},
	}
}

func tenantTierFamily() putest.Family[*Service] {
	const token = "gold-tier"

	seededConfig := `{"ingestMessagesPerSecond":2000}`
	replaceConfig := `{"ingestMessagesPerSecond":3000}`

	return putest.Family[*Service]{
		Name:    "tenantTier",
		Token:   token,
		Migrate: []any{&iam.TenantTier{}, &iam.Tenant{}},
		Seed: func(t *testing.T, s *Service, ctx context.Context) {
			cfg, err := ParseConfigJSON(&seededConfig)
			if err != nil {
				t.Fatalf("seed config: %v", err)
			}
			if _, err := s.CreateTenantTier(ctx, TierInput{
				Token: token, Name: "Gold", Description: "The gold packaging",
				Config: cfg, Color: string(iam.TierColorAmber),
			}); err != nil {
				t.Fatalf("seed tier: %v", err)
			}
		},
		Read: func(t *testing.T, s *Service, ctx context.Context) map[string]string {
			tier, err := s.iam.TenantTierByToken(ctx, token)
			if err != nil {
				t.Fatalf("reload tier: %v", err)
			}
			return map[string]string{
				"name":        nullStr(tier.Name),
				"description": nullStr(tier.Description),
				"config":      configJSON(tier.Config),
				"color":       tier.Color,
			}
		},
		NewRequest: func() any { return new(TierUpdateRequest) },
		Update: func(s *Service, ctx context.Context, token string, req any) error {
			_, err := s.UpdateTenantTier(ctx, token, req.(*TierUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "Gold", "Gold Plus",
				func(r *TierUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", "The gold packaging", "Rewritten",
				func(r *TierUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			putest.OptionalStringField("config", seededConfig, replaceConfig,
				func(r *TierUpdateRequest) *dcgraphql.OptionalString { return &r.Config }),
			// NOT NULL with an empty default, so its cleared reading is "" — see
			// emptiableStringField.
			emptiableStringField("color", string(iam.TierColorAmber), string(iam.TierColorViolet),
				func(r *TierUpdateRequest) *dcgraphql.OptionalString { return &r.Color }),
		},
	}
}

func oauthClientFamily() putest.Family[*Service] {
	const clientId = "mcp-client"

	seededURIs := []string{"https://example.invalid/callback"}
	replaceURIs := []string{"https://example.invalid/other", "http://127.0.0.1:8080/cb"}
	seededScopes := []string{"read-only"}
	replaceScopes := []string{"location"}

	return putest.Family[*Service]{
		Name:    "oauthClient",
		Token:   clientId,
		Migrate: []any{&iam.OAuthClient{}},
		Seed: func(t *testing.T, s *Service, ctx context.Context) {
			if _, _, err := s.CreateOAuthClient(ctx, OAuthClientInput{
				ClientId: clientId, Name: "MCP", Description: "The agent client",
				RedirectURIs: seededURIs, Scopes: seededScopes,
			}); err != nil {
				t.Fatalf("seed oauth client: %v", err)
			}
		},
		Read: func(t *testing.T, s *Service, ctx context.Context) map[string]string {
			c, err := s.iam.OAuthClientByClientId(ctx, clientId)
			if err != nil {
				t.Fatalf("reload oauth client: %v", err)
			}
			return map[string]string{
				"name":         nullStr(c.Name),
				"description":  nullStr(c.Description),
				"redirectUris": renderList(c.RedirectURIs),
				"scopes":       renderList(c.Scopes),
			}
		},
		NewRequest: func() any { return new(OAuthClientUpdateRequest) },
		Update: func(s *Service, ctx context.Context, clientId string, req any) error {
			_, err := s.UpdateOAuthClient(ctx, clientId, req.(*OAuthClientUpdateRequest))
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "MCP", "MCP agents",
				func(r *OAuthClientUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", "The agent client", "Rewritten",
				func(r *OAuthClientUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			// Replaceable, never emptiable: the harness's ARequiredFieldRefusesAnExplicitNull
			// drives the null and this package's own test drives the [] that means the same
			// thing.
			requiredStringListField("redirectUris", seededURIs, replaceURIs,
				func(r *OAuthClientUpdateRequest) *dcgraphql.OptionalStringList { return &r.RedirectUris }),
			requiredStringListField("scopes", seededScopes, replaceScopes,
				func(r *OAuthClientUpdateRequest) *dcgraphql.OptionalStringList { return &r.Scopes }),
		},
	}
}
