// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 The token masks are served on BOTH planes, and these tests exist because
// nothing else in the suite can tell that apart from a duplicate.
//
// The masks are an instance-global setting, so which plane answers changes nothing
// about the VALUE. What it changes is whether the asking screen can ask at all. A
// tenant-scoped create form holds a tenant session that lasts days and an identity
// token that expires in fifteen minutes and cannot refresh; an operator creating the
// instance's first tenant holds an identity session and no tenant session at all.
// Serve on one plane only and one of those two silently falls back to the built-in
// pattern, minting tokens that ignore the operator's configuration.
//
// So the contract under test is three-part: both planes ANSWER, both answer the SAME
// thing, and both answer for a caller holding NO settings authority.

// TestTokenMasksAreReadableWithoutASettingsAuthority pins the deliberate widening.
//
// 🔴 This is the gap that mattered most. Every other settings operation is gated on
// settings:read, and nothing asserted that this one is not — so adding
// auth.Authorize(ctx, auth.SettingsRead) here would break token generation in every
// non-admin create form in the console and leave the entire Go suite green. The
// permissive path had a test for the case it REFUSES and none for the case it must
// ALLOW, which is the half that carries the intent.
func TestTokenMasksAreReadableWithoutASettingsAuthority(t *testing.T) {
	ctx, _ := newSettingsCtx(t) // authenticated, and holding no authorities at all

	masks, err := (&SettingsResolver{}).TokenMasks(ctx)
	require.NoError(t, err, "the identity lane must serve masks to any signed-in caller")
	assert.NotEmpty(t, masks)

	masks, err = (&SchemaResolver{}).TokenMasks(ctx)
	require.NoError(t, err, "the tenant data plane must serve masks to any signed-in caller")
	assert.NotEmpty(t, masks)
}

// TestBothPlanesServeTheSameMasks is the anti-drift check. Two resolvers answering
// the same question is a duplicate unless something holds them equal; if they ever
// diverge, a token's shape would depend on which screen minted it.
//
// It writes a non-default value first, so agreement cannot be satisfied by both
// planes independently returning the same built-in default — which is what they
// return when neither is reading the store at all.
func TestBothPlanesServeTheSameMasks(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	configured := `{"default":"{slug}","device":"dev-{alphanumeric-6}"}`
	_, err := svc.Set(ctx, settingsdefs.KeyTokenMasks, []byte(configured), "operator@devicechain.local")
	require.NoError(t, err)

	identityLane, err := (&SettingsResolver{}).TokenMasks(ctx)
	require.NoError(t, err)
	dataPlane, err := (&SchemaResolver{}).TokenMasks(ctx)
	require.NoError(t, err)

	assert.JSONEq(t, configured, identityLane, "the identity lane must serve what the operator configured")
	assert.JSONEq(t, configured, dataPlane, "the tenant data plane must serve what the operator configured")
	assert.Equal(t, identityLane, dataPlane, "the two planes must not drift")
}

// TestTokenMasksFailClosedOnBothPlanes covers the data plane's half of
// TestSettingsFailClosed.
//
// 🔑 It also pins an ordering that is easy to lose: the refusal must happen BEFORE
// the resolver reaches for the settings service. Both planes read that service out
// of the request context with a type assertion that panics when it is absent — which
// is right for a served request and wrong for an unauthenticated one, where there is
// nothing to serve. An earlier draft of the shared helper took the service as a
// parameter, so the caller evaluated it eagerly and this path panicked instead of
// returning an error. A bare context is exactly what catches that.
func TestTokenMasksFailClosedOnBothPlanes(t *testing.T) {
	_, err := (&SettingsResolver{}).TokenMasks(context.Background())
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = (&SchemaResolver{}).TokenMasks(context.Background())
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}
