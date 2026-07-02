// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
)

// TestSettingsSchemaParses validates that the settings schema parses against its
// resolver root — every settings field must have a matching resolver method
// (ADR-042 P2).
func TestSettingsSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("settings schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(SettingsSchemaContent, &SettingsResolver{})
}

// TestSettingsFailClosed confirms the settings resolvers reject an unauthenticated
// request (no claims on context) with ErrUnauthenticated, before ever touching the
// service — the endpoint's identity-token requirement is not the only gate.
func TestSettingsFailClosed(t *testing.T) {
	r := &SettingsResolver{}
	ctx := context.Background()

	_, err := r.Settings(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.Setting(ctx, struct{ Key string }{Key: "entity.token_masks"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: "entity.token_masks", Value: "{}"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.ClearSetting(ctx, struct{ Key string }{Key: "entity.token_masks"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// TestSettingsForbidWithoutAuthority confirms an authenticated identity lacking the
// settings authority is refused (ErrForbidden): a logged-in non-superuser can
// neither read nor write instance settings.
func TestSettingsForbidWithoutAuthority(t *testing.T) {
	r := &SettingsResolver{}
	ctx := auth.WithClaims(context.Background(), &auth.Claims{Authorities: []string{}})

	_, err := r.Settings(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: "entity.token_masks", Value: "{}"})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}
