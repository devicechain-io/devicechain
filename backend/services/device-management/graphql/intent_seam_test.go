// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/golang-jwt/jwt/v5"
)

func seamClaims(subject string, tokenType string, authorities ...auth.Authority) *auth.Claims {
	c := &auth.Claims{TokenType: tokenType, Tenant: "acme", Username: subject}
	c.Subject = subject
	for _, a := range authorities {
		c.Authorities = append(c.Authorities, string(a))
	}
	return c
}

// The observation seam answers the automation area's own service token and nobody else: not a
// person, whatever they hold; not a scoped member; not an agent; not any other service.
func TestTheSeamAnswersOnlyTheAutomationService(t *testing.T) {
	automation := seamClaims("automation", auth.TokenTypeService, auth.DeviceRead)
	scoped := seamClaims("automation", auth.TokenTypeService, auth.DeviceRead)
	scoped.ScopeAnchors = []string{"customer:acme-east"}
	agent := seamClaims("automation", auth.TokenTypeService, auth.DeviceRead)
	agent.Audience = jwt.ClaimStrings{"https://mcp.example.invalid"}
	cases := map[string]struct {
		claims *auth.Claims
		ok     bool
	}{
		"the automation service":                     {automation, true},
		"the automation service without device:read": {seamClaims("automation", auth.TokenTypeService, auth.TenantRead), false},
		"a person holding every authority":           {seamClaims("automation", auth.TokenTypeAccess, auth.DeviceRead, auth.DeviceWrite, auth.FleetOperate), false},
		"a scoped caller":                            {scoped, false},
		"an agent-origin token":                      {agent, false},
		"device-management's own service":            {seamClaims("device-management", auth.TokenTypeService, auth.DeviceRead), false},
		"event-processing":                           {seamClaims("event-processing", auth.TokenTypeService, auth.DeviceRead), false},
		"a service with no subject":                  {seamClaims("", auth.TokenTypeService, auth.DeviceRead), false},
		"a near-miss subject":                        {seamClaims("automation ", auth.TokenTypeService, auth.DeviceRead), false},
	}
	r := &SchemaResolver{}
	for name, tc := range cases {
		ctx := auth.WithClaims(core.WithTenant(context.Background(), "acme"), tc.claims)
		err := intentSeamCaller(ctx)
		if tc.ok != (err == nil) {
			t.Errorf("%s: guard = %v, want allowed %v", name, err, tc.ok)
		}
		if tc.ok {
			continue
		}
		// Every resolver refuses before it reaches the model (the resolver has none here, so
		// reaching it would panic rather than pass).
		calls := map[string]func() error{
			"configurationKeyStateDigest": func() error {
				_, err := r.ConfigurationKeyStateDigest(ctx, struct {
					Target IntentSeamTargetInput
					Key    string
				}{IntentSeamTargetInput{"g", 1}, "dim"})
				return err
			},
			"configurationKeyState": func() error {
				_, err := r.ConfigurationKeyState(ctx, struct {
					Target  IntentSeamTargetInput
					Key     string
					AfterId *string
					Limit   int32
				}{IntentSeamTargetInput{"g", 1}, "dim", nil, 10})
				return err
			},
			"groupTargetsIntersect": func() error {
				_, err := r.GroupTargetsIntersect(ctx, struct {
					A IntentSeamTargetInput
					B IntentSeamTargetInput
				}{IntentSeamTargetInput{"g", 1}, IntentSeamTargetInput{"h", 1}})
				return err
			},
			"previewIntentBulkChange": func() error {
				_, err := r.PreviewIntentBulkChange(ctx, struct {
					Kind         string
					ConfigSet    FleetConfigSetInput
					DeviceTokens []string
				}{"config.set", FleetConfigSetInput{Key: "dim"}, []string{"d1"}})
				return err
			},
			"bulkChangeGranteeStanding": func() error {
				_, err := r.BulkChangeGranteeStanding(ctx, struct{ Principal string }{"alice@example.com"})
				return err
			},
		}
		for op, call := range calls {
			if err := call(); err == nil {
				t.Errorf("%s: %s answered", name, op)
			}
		}
	}
	if err := intentSeamCaller(context.Background()); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("no claims = %v; want ErrUnauthenticated", err)
	}
}
