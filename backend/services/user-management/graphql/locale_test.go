// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
	"github.com/stretchr/testify/require"
)

// shippedLocaleDefault returns the code default for locale.default.
func shippedLocaleDefault(t *testing.T) []byte {
	t.Helper()
	d, ok := settingsdefs.Registry().Lookup(settingsdefs.KeyLocaleDefault)
	require.True(t, ok, "locale.default must be a registered setting")
	return d.Default
}

// ---- setSetting actually CALLS that validator -------------------------------
//
// 🔴 Same reasoning as the basemap pair above it, and the same gap it was written to
// close: TestValidateLocaleDefault proves the validator WORKS, and says nothing about
// whether anything invokes it. A validator nobody calls is a comment.

func TestSetSettingRefusesAnInvalidLocaleDefault(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	r := &SettingsResolver{}

	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settingsdefs.KeyLocaleDefault, Value: `"en_US"`})

	require.Error(t, err, "setSetting must run the locale rules, not just store opaque JSON")
	require.Contains(t, err.Error(), "language tag")

	eff, err := svc.Get(ctx, settingsdefs.KeyLocaleDefault)
	require.NoError(t, err)
	require.False(t, eff.Overridden, "a refused value must not be persisted")
}

// The positive control: a VALID locale default must still be storable. Without it, a
// validator that refused everything would pass the test above.
func TestSetSettingStoresAValidLocaleDefault(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	r := &SettingsResolver{}

	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settingsdefs.KeyLocaleDefault, Value: `"es"`})
	require.NoError(t, err)

	eff, err := svc.Get(ctx, settingsdefs.KeyLocaleDefault)
	require.NoError(t, err)
	require.True(t, eff.Overridden)
	require.JSONEq(t, `"es"`, string(eff.Value))
}

func TestSetSettingRequiresSettingsWriteForTheLocaleDefault(t *testing.T) {
	// A holder of locale:write may set its OWN tenant's locale; the INSTANCE default
	// is an operator surface and stays behind settings:write.
	ctx, _ := newSettingsCtx(t, string(auth.LocaleWrite))
	r := &SettingsResolver{}

	_, err := r.SetSetting(ctx, struct {
		Key   string
		Value string
	}{Key: settingsdefs.KeyLocaleDefault, Value: `"es"`})

	require.ErrorIs(t, err, auth.ErrForbidden)
}

// ---- resolveLocale: the cascade wiring ---------------------------------------
//
// The three rungs the SERVER owns. The console's own rungs (an explicit user choice
// above these, and the browser/English below them) live in the console's
// config.test.ts — the server has no view of either.

// 🔴 THE TENANT IS THE HIGH TIER. Swap the arguments in resolveLocale's Merge call and
// the operator's instance default silently wins over every tenant's own choice, while
// every other test in this file still passes.
func TestResolveLocalePrefersTheTenantOverTheOperatorDefault(t *testing.T) {
	got := resolveLocale(sp("es"), []byte(`"en"`))
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

// Rung 2b: a tenant that has set nothing inherits the operator's stored default.
func TestResolveLocaleFallsBackToTheOperatorDefault(t *testing.T) {
	got := resolveLocale(nil, []byte(`"es"`))
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

// 🔴 RUNG 2c, AND THE ONE THAT MATTERS MOST: with no stored override anywhere, the
// settings service hands back the CODE default, and that must resolve to NOTHING — so
// the console falls through to the browser.
//
// This test previously asserted "en", which pinned the defect rather than the contract.
// The chain was: code default "en" -> Get returns it -> resolveLocale yields "en" ->
// tenant.locale is "en" -> applyTenantDefaultLocale("en") -> changeLanguage("en"). A
// user whose browser asked for Spanish, on an instance nobody had configured, got
// English, and no rung-3 test could see it because every one of them fed a locale the
// server could not actually emit.
//
// Read from the registry rather than retyped, so this follows the shipped value rather
// than pinning a copy of it.
func TestResolveLocaleResolvesToNothingUnderTheShippedCodeDefault(t *testing.T) {
	require.Nil(t, resolveLocale(nil, shippedLocaleDefault(t)),
		"an unconfigured instance must decline to answer, leaving the browser rung in force")
}

// 🔴 A stored blank at the TENANT tier must not mask the operator's default. This is
// the trap locale.Normalize exists for, checked here at the seam a non-console client
// actually reaches: a blank written before the write-path gate existed is still on the
// row, and reading it as SET would hand the tenant nothing on an instance that has a
// default configured.
func TestResolveLocaleDoesNotLetAStoredBlankMaskTheDefault(t *testing.T) {
	got := resolveLocale(sp("   "), []byte(`"es"`))
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

// Degrade, never fail: this rides the console's boot query, so a stored default that
// is malformed or rule-invalid costs the operator default and nothing else — never the
// whole shell.
func TestResolveLocaleDegradesOnAMalformedStoredDefault(t *testing.T) {
	for name, stored := range map[string]string{
		"not json":                 `{`,
		"an object":                `{"locale":"es"}`,
		"a number":                 `7`,
		"json null":                `null`,
		"not a language tag":       `"English"`,
		"the POSIX spelling":       `"en_US"`,
		"a blank inside the value": `"   "`,
	} {
		require.Nilf(t, resolveLocale(nil, []byte(stored)), "%s must degrade to no operator default", name)
	}
}

// The counterweight to the degrade test: a tenant's OWN choice survives a malformed
// operator default. Degrading must cost the operator tier only.
func TestResolveLocaleKeepsTheTenantChoiceThroughAMalformedDefault(t *testing.T) {
	got := resolveLocale(sp("es"), []byte(`{`))
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

// 🔴 THE COMPOSITION TEST, run through the REAL settings service rather than a hand-
// written byte string. This is the one that was missing, and its absence is why the
// regression above survived a suite that looked thorough: every unit test fed
// resolveLocale a `storedDefault` chosen by the test, so nothing anywhere asserted what
// settings.Get ACTUALLY HANDS IT on an instance nobody has configured. A property
// proved only on inputs the system cannot produce is not proved.
//
// Fresh install, operator has never touched the setting, tenant has set no override:
// the resolved locale must be nil, so the console leaves the language to the browser.
func TestAFreshInstallResolvesNoTenantLocaleAtAll(t *testing.T) {
	ctx, _ := newSettingsCtx(t)

	got, err := (&TenantResolver{t: &iam.Tenant{}, svc: &SchemaResolver{}}).Locale(ctx)

	require.NoError(t, err)
	require.Nil(t, got,
		"with nothing stored at either tier the server must answer 'no default'; any tag here "+
			"is applied by the console and silently outranks the browser rung on every instance")
}

// The positive control on the composition above, through the same real service: once an
// operator DOES store a default, it reaches the tenant. Without this, a resolver that
// always returned nil would pass the test above.
func TestAStoredInstanceDefaultReachesATenantThatSetsNone(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	_, err := svc.Set(ctx, settingsdefs.KeyLocaleDefault, []byte(`"es"`), "operator@devicechain.local")
	require.NoError(t, err)

	got, err := (&TenantResolver{t: &iam.Tenant{}, svc: &SchemaResolver{}}).Locale(ctx)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "es", *got)
}

// And the tenant still outranks it, through the real service.
func TestATenantOverrideBeatsTheStoredInstanceDefault(t *testing.T) {
	ctx, svc := newSettingsCtx(t, string(auth.SettingsWrite))
	_, err := svc.Set(ctx, settingsdefs.KeyLocaleDefault, []byte(`"es"`), "operator@devicechain.local")
	require.NoError(t, err)

	got, err := (&TenantResolver{t: &iam.Tenant{Locale: sp("pt-BR")}, svc: &SchemaResolver{}}).Locale(ctx)

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "pt-BR", *got)
}

// ---- localeOverride: the RAW column, no cascade ------------------------------

func TestLocaleOverrideReturnsTheStoredValueWithNoCascade(t *testing.T) {
	r := &TenantResolver{t: &iam.Tenant{Locale: sp("es-mx")}}
	got := r.LocaleOverride()
	require.NotNil(t, got)
	// Normalized on the way out, so an editor seeded from it and a value written back
	// through the mutation compare equal instead of looking like a pending edit.
	require.Equal(t, "es-MX", *got)
}

func TestLocaleOverrideIsNilForATenantThatInherits(t *testing.T) {
	require.Nil(t, (&TenantResolver{t: &iam.Tenant{}}).LocaleOverride(),
		"an inheriting tenant must report no override, not the resolved value")
}

// ---- the authority gate ------------------------------------------------------

// 🔴 The whole reason locale:write exists as its own authority. A holder of
// branding:write can restyle the console; it must NOT be able to re-language it for
// every member of the tenant.
//
// Delete the auth.Authorize call in SetTenantLocale and this is the test that notices
// — nothing else in the package does.
func TestSetTenantLocaleRefusesAHolderOfBrandingWriteAlone(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), accessClaims(string(auth.BrandingWrite)))
	r := &SchemaResolver{}

	_, err := r.SetTenantLocale(ctx, struct{ Locale *string }{Locale: sp("es")})

	require.ErrorIs(t, err, auth.ErrForbidden,
		"branding:write must not carry the tenant's language with it")
}

// basemap:write is the nearest neighbour and the likeliest accidental fold, since both
// were split out of branding for adjacent reasons. It must not open this door either.
func TestSetTenantLocaleRefusesAHolderOfBasemapWriteAlone(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), accessClaims(string(auth.BasemapWrite)))
	r := &SchemaResolver{}

	_, err := r.SetTenantLocale(ctx, struct{ Locale *string }{Locale: sp("es")})

	require.ErrorIs(t, err, auth.ErrForbidden)
}

func TestSetTenantLocaleRefusesAnUnauthenticatedCaller(t *testing.T) {
	r := &SchemaResolver{}
	_, err := r.SetTenantLocale(context.Background(), struct{ Locale *string }{Locale: sp("es")})
	require.Error(t, err, "an unauthenticated caller must never reach the write")
}

// 🔴 The POSITIVE CONTROL, and without it the tests above prove only that the method
// can fail. A holder of locale:write must get PAST the authority gate — shown here by
// feeding it a locale that fails VALIDATION, so the error it returns is the validation
// error rather than the authorization one. A gate that refused everyone would fail
// this test.
func TestSetTenantLocaleAdmitsAHolderOfLocaleWriteAndThenValidates(t *testing.T) {
	ctx := auth.WithClaims(context.Background(), accessClaims(string(auth.LocaleWrite)))
	r := &SchemaResolver{}

	_, err := r.SetTenantLocale(ctx, struct{ Locale *string }{Locale: sp("English please")})

	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrForbidden, "locale:write must be admitted by the gate")
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.Contains(t, err.Error(), "language tag",
		"having passed the gate, the caller must be stopped by validation instead")
}

// ---- the write path's normalization ------------------------------------------

// prepareLocaleWrite is where "clear this" is made expressible. A client that sends ""
// (dcctl, the SDKs, a raw GraphQL caller — the console sends null) means "inherit",
// and a stored blank would instead WIN the cascade and mask the operator's default.
func TestPrepareLocaleWriteTurnsABlankIntoInherit(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		got, err := prepareLocaleWrite(sp(blank))
		require.NoError(t, err)
		require.Nilf(t, got, "%q must be stored as NULL, not as a blank that wins the cascade", blank)
	}
}

func TestPrepareLocaleWriteCanonicalizesBeforeStoring(t *testing.T) {
	got, err := prepareLocaleWrite(sp(" pt-br "))
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "pt-BR", *got, "the bytes stored must be the bytes validated")
}

func TestPrepareLocaleWriteAcceptsAnExplicitClear(t *testing.T) {
	got, err := prepareLocaleWrite(nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestPrepareLocaleWriteRefusesAValueThatIsNotALanguageTag(t *testing.T) {
	_, err := prepareLocaleWrite(sp("en_US"))
	require.Error(t, err)
}

// 🔴 A tag the console does not currently ship is STORED, not refused — the decision
// documented on the locale package. The shipped set lives with the catalogs, so
// mirroring it here would fail in the wrong direction: refusing a locale the console
// has just started shipping. Pinned as a behaviour so a later "tighten this up" is a
// deliberate reversal rather than a quiet one.
func TestPrepareLocaleWriteStoresAWellFormedTagTheConsoleDoesNotShip(t *testing.T) {
	got, err := prepareLocaleWrite(sp("fr"))
	require.NoError(t, err, "a well-formed tag is stored; it is inert until its catalog ships")
	require.NotNil(t, got)
	require.Equal(t, "fr", *got)
}
