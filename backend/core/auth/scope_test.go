// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func scopedAccessClaims(anchors []string, authorities ...string) *Claims {
	c := tenantAccessClaims(authorities...)
	c.ScopeAnchors = anchors
	return c
}

func mustScope(t *testing.T, anchors ...string) Scope {
	t.Helper()
	s, err := NewScope(anchors)
	if err != nil {
		t.Fatalf("NewScope(%v): %v", anchors, err)
	}
	return s
}

// ---- the resolver gate --------------------------------------------------------------

// 🔴 PRIVILEGE ESCALATION: a scoped member holding "*" reaches NOTHING through Authorize
// or AuthorizeAny — not one authority in the vocabulary. Deny-by-default is what makes
// every resolver written before scopes existed safe with no edit.
func TestAuthorizeRefusesAScopedCallerForEveryAuthority(t *testing.T) {
	ctx := ctxWith(scopedAccessClaims([]string{"customer:acme-corp"}, string(AuthorityAll)))
	for a := range vocabulary {
		if err := Authorize(ctx, a); !errors.Is(err, ErrScopeUnsupported) {
			t.Errorf("Authorize(%q) for a scoped holder of \"*\": %v", a, err)
		}
		if err := AuthorizeAny(ctx, a); !errors.Is(err, ErrScopeUnsupported) {
			t.Errorf("AuthorizeAny(%q) for a scoped holder of \"*\": %v", a, err)
		}
	}
	// Negative control: the same claims unscoped pass every tenant-tier authority.
	unscoped := ctxWith(tenantAccessClaims(string(AuthorityAll)))
	if err := Authorize(unscoped, DeviceWrite); err != nil {
		t.Fatalf("the unscoped control was refused: %v", err)
	}
}

// 🔴 FAIL CLOSED: claims whose scope is present but EMPTY — built in-process, never
// through the validator — are scoped to nothing. No scope must never read as "all".
func TestAnEmptyScopeInContextIsScopedToNothing(t *testing.T) {
	ctx := ctxWith(scopedAccessClaims([]string{}, string(DeviceRead)))
	if err := Authorize(ctx, DeviceRead); !errors.Is(err, ErrScopeUnsupported) {
		t.Fatalf("an empty in-context scope was admitted by Authorize: %v", err)
	}
	s, scoped := ScopeFromContext(ctx)
	if !scoped || !s.IsScoped() || len(s.Anchors()) != 0 {
		t.Fatalf("an empty in-context scope must be scoped with no anchors; got scoped=%v %+v", scoped, s)
	}
	got, err := AuthorizeScoped(ctx, DeviceRead)
	if err != nil || !got.IsScoped() || len(got.Anchors()) != 0 {
		t.Fatalf("AuthorizeScoped must hand back a scoped, anchorless scope: %+v, %v", got, err)
	}
}

// AuthorizeScoped is the opt-in: it admits a scoped caller and hands back the scope.
func TestAuthorizeScopedReturnsTheCallersScope(t *testing.T) {
	ctx := ctxWith(scopedAccessClaims([]string{"customer:acme-corp", "group:north"}, string(DeviceRead)))
	s, err := AuthorizeScoped(ctx, DeviceRead)
	if err != nil {
		t.Fatalf("AuthorizeScoped refused a scoped caller holding device:read: %v", err)
	}
	if !s.IsScoped() || !reflect.DeepEqual(s.Anchors(), []string{"customer:acme-corp", "group:north"}) {
		t.Fatalf("AuthorizeScoped returned %+v", s)
	}
	// An unscoped caller gets the zero Scope: unscoped, not "scoped to nothing".
	u, err := AuthorizeScoped(ctxWith(tenantAccessClaims(string(DeviceRead))), DeviceRead)
	if err != nil || u.IsScoped() {
		t.Fatalf("an unscoped caller must get an unscoped Scope: %+v, %v", u, err)
	}
}

// 🔴 PRIVILEGE ESCALATION through the opt-in doors: opting in admits the SCOPE, never
// an authority. A scoped caller without the authority, or holding a system-tier one on
// an access token, is refused exactly as Authorize would refuse an unscoped one.
func TestTheOptInDoorsStillCheckTheAuthorityAndTier(t *testing.T) {
	scoped := ctxWith(scopedAccessClaims([]string{"customer:acme-corp"}, string(DeviceRead), string(AIAdmin)))
	if _, err := AuthorizeScoped(scoped, DeviceWrite); !errors.Is(err, ErrForbidden) {
		t.Errorf("AuthorizeScoped admitted an authority the caller lacks: %v", err)
	}
	if _, err := AuthorizeScoped(scoped, AIAdmin); !errors.Is(err, ErrForbidden) {
		t.Errorf("AuthorizeScoped admitted a system-tier authority on an access token: %v", err)
	}
	if err := AuthorizeCatalogue(scoped, DeviceWrite); !errors.Is(err, ErrForbidden) {
		t.Errorf("AuthorizeCatalogue admitted an authority the caller lacks: %v", err)
	}
	if err := AuthorizeCatalogue(scoped, AIAdmin); !errors.Is(err, ErrForbidden) {
		t.Errorf("AuthorizeCatalogue admitted a system-tier authority on an access token: %v", err)
	}
	// Negative controls: each door admits the authority the caller does hold.
	if _, err := AuthorizeScoped(scoped, DeviceRead); err != nil {
		t.Errorf("AuthorizeScoped refused a held authority: %v", err)
	}
	if err := AuthorizeCatalogue(scoped, DeviceRead); err != nil {
		t.Errorf("AuthorizeCatalogue refused a held authority: %v", err)
	}
	// And all three refuse an unauthenticated context.
	if _, err := AuthorizeScoped(context.Background(), DeviceRead); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("AuthorizeScoped on no claims: %v", err)
	}
	if err := AuthorizeCatalogue(context.Background(), DeviceRead); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("AuthorizeCatalogue on no claims: %v", err)
	}
}

// The scope refusal is a FORBIDDEN, so every caller handling a refusal handles it, and
// it carries the reason the wire publishes. A plain ErrForbidden carries none.
func TestScopeUnsupportedIsAForbiddenWithAReason(t *testing.T) {
	wrapped := fmt.Errorf("list devices: %w", ErrScopeUnsupported)
	if !errors.Is(wrapped, ErrForbidden) || !errors.Is(wrapped, ErrScopeUnsupported) {
		t.Fatal("ErrScopeUnsupported must be both itself and an ErrForbidden, wrapped")
	}
	if errors.Is(ErrForbidden, ErrScopeUnsupported) {
		t.Fatal("a plain ErrForbidden must not read as a scope refusal")
	}
	if got := ForbiddenReason(wrapped); got != "SCOPE_UNSUPPORTED" {
		t.Fatalf("reason of a scope refusal = %q", got)
	}
	if got := ForbiddenReason(ErrForbidden); got != "" {
		t.Fatalf("a plain refusal carries reason %q", got)
	}
	// The clients recognise a refusal by the message prefix.
	if !strings.HasPrefix(ErrScopeUnsupported.Error(), "forbidden") {
		t.Fatalf("the scope refusal's message does not start with \"forbidden\": %q", ErrScopeUnsupported.Error())
	}
}

// ---- the claim at the parse -----------------------------------------------------------

// signClaims signs claims with a fresh key and returns the matching validator.
func signClaims(t *testing.T, c *Claims) (*Validator, string) {
	t.Helper()
	key := mustKey(t)
	if c.ExpiresAt == nil {
		c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	}
	return NewValidator(&key.PublicKey), signRaw(t, jwt.SigningMethodRS256, key, c)
}

// 🔴 CONFUSED DEPUTY: the tiers that act ABOVE a tenant never carry a scope. A scope on
// one is refused at the parse — the token is not admitted at all — so a service acting
// for the platform can never be handed a member's scope, nor an identity token reach the
// admin plane looking narrower than it is. A token type this build does not recognise
// is refused too (an allow-list), and so is a superuser marker beside a scope.
func TestTheScopeClaimIsRefusedWhereItMayNotRide(t *testing.T) {
	scp := []string{"customer:acme-corp"}
	cases := map[string]*Claims{
		"identity token":      {TokenType: TokenTypeIdentity, Username: "u", ScopeAnchors: scp},
		"service token":       {TokenType: TokenTypeService, Username: "svc", ScopeAnchors: scp},
		"unknown token type":  {TokenType: "session", Tenant: "acme", ScopeAnchors: scp},
		"untyped token":       {Tenant: "acme", ScopeAnchors: scp},
		"superuser marker":    {TokenType: TokenTypeAccess, Tenant: "acme", ActingAsSuperuser: true, ScopeAnchors: scp},
		"too many anchors":    {TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: manyAnchors(MaxScopeAnchors + 1)},
		"unknown anchor kind": {TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: []string{"tenant:globex"}},
		"bare anchor":         {TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: []string{"acme-corp"}},
		"empty anchor token":  {TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: []string{"customer:"}},
		"nested anchor":       {TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: []string{"customer:a:b"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			v, token := signClaims(t, c)
			if _, err := v.Parse(token); !errors.Is(err, ErrMalformedScope) {
				t.Fatalf("Parse admitted a token with a scope it may not carry: %v", err)
			}
		})
	}
	// Negative controls: the two tiers a scope may ride, at the cap, are admitted.
	for _, typ := range []string{TokenTypeAccess, TokenTypeRefresh} {
		v, token := signClaims(t, &Claims{TokenType: typ, Tenant: "acme", ScopeAnchors: manyAnchors(MaxScopeAnchors)})
		if _, err := v.Parse(token); err != nil {
			t.Fatalf("a %s token with %d anchors was refused: %v", typ, MaxScopeAnchors, err)
		}
	}
}

func manyAnchors(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("customer:c%d", i)
	}
	return out
}

// Every Validate* helper goes through Parse, so none of them skips the scope rule.
func TestEveryValidateHelperAppliesTheScopeRule(t *testing.T) {
	scp := []string{"customer:acme-corp"}
	v, idTok := signClaims(t, &Claims{TokenType: TokenTypeIdentity, Username: "u", ScopeAnchors: scp})
	if _, err := v.ValidateIdentity(idTok); err == nil {
		t.Error("ValidateIdentity admitted an identity token carrying a scope")
	}
	v, svcTok := signClaims(t, &Claims{TokenType: TokenTypeService, Username: "svc", ScopeAnchors: scp})
	if _, err := v.ValidateService(svcTok); err == nil {
		t.Error("ValidateService admitted a service token carrying a scope")
	}
	v, acc := signClaims(t, &Claims{TokenType: TokenTypeAccess, Tenant: "acme", ScopeAnchors: []string{"tenant:globex"}})
	if _, err := v.Validate(acc); err == nil {
		t.Error("Validate admitted an access token carrying a malformed scope")
	}
	// (An EMPTY "scp" cannot be produced from Claims — the field is omitempty — so it is
	// signed from a raw claim map in TestAnEmptyScopeClaimIsRefusedAtParse.)
}

// ---- the mint ---------------------------------------------------------------------------

// The scoped mints write the scope as "scp", keep it apart from the OAuth scope string,
// and give the token the shorter scoped lifetime (ADR-081 decision 8: the access
// lifetime IS the revocation window).
func TestTheScopedMintsCarryTheScopeAndTheShorterLifetime(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", 15*time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)
	scope := mustScope(t, "customer:acme-corp")

	before := time.Now()
	tok, err := iss.IssueScopedTenantAccess("acme", "c@acme-corp.example", nil, []string{string(DeviceRead)}, scope, "j1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.ScopeAnchors, []string{"customer:acme-corp"}) || c.ActingAsSuperuser {
		t.Fatalf("scoped tenant access token carries scp=%v sudo=%v", c.ScopeAnchors, c.ActingAsSuperuser)
	}
	if life := tok.ExpiresAt.Sub(before); life > DefaultScopedAccessTTL+time.Second || life < DefaultScopedAccessTTL-time.Second {
		t.Fatalf("a scoped access token lives %v; want the scoped TTL %v, not the access TTL", life, DefaultScopedAccessTTL)
	}

	o, err := iss.IssueScopedOAuthAccess("acme", "c@acme-corp.example", nil, []string{string(DeviceRead)},
		"mcp:read", []string{"https://mcp.example"}, scope, "client-1", "j2")
	if err != nil {
		t.Fatal(err)
	}
	oc, err := v.Validate(o.Token)
	if err != nil {
		t.Fatal(err)
	}
	if oc.Scope != "mcp:read" || !reflect.DeepEqual(oc.ScopeAnchors, []string{"customer:acme-corp"}) {
		t.Fatalf("the OAuth scope and the membership scope were conflated: scope=%q scp=%v", oc.Scope, oc.ScopeAnchors)
	}

	// Negative control: the unscoped mints carry no scp, and their caller is unscoped.
	u, err := iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(DeviceRead)}, false, "j3")
	if err != nil {
		t.Fatal(err)
	}
	uc, err := v.Validate(u.Token)
	if err != nil {
		t.Fatal(err)
	}
	if uc.ScopeAnchors != nil {
		t.Fatalf("an unscoped mint carries scp=%v", uc.ScopeAnchors)
	}
	if _, scoped := ScopeFromContext(ctxWith(uc)); scoped {
		t.Fatal("an unscoped token's caller reads as scoped")
	}
}

// The scoped lifetime never exceeds the issuer's access lifetime: an operator who
// shortened access tokens below the scoped default gets that for scoped ones too.
func TestTheScopedLifetimeNeverExceedsTheAccessLifetime(t *testing.T) {
	iss := NewIssuer(mustKey(t), "test", time.Minute, time.Hour)
	if got := iss.ScopedAccessTTL(); got != time.Minute {
		t.Fatalf("scoped TTL %v exceeds the issuer's 1m access TTL", got)
	}
	if got := NewIssuer(mustKey(t), "test", 0, 0).ScopedAccessTTL(); got != DefaultScopedAccessTTL || got >= DefaultAccessTTL {
		t.Fatalf("default scoped TTL = %v; want %v, shorter than %v", got, DefaultScopedAccessTTL, DefaultAccessTTL)
	}
}

// 🔴 FAIL CLOSED AT THE MINT: a scoped mint never writes an unscoped token. The claim
// is omitted when empty, so a scoped-but-anchorless Scope minted as-is would carry NO
// "scp" — the whole tenant. The zero Scope is refused for the same reason.
func TestAScopedMintRefusesAScopeItCannotWrite(t *testing.T) {
	iss := NewIssuer(mustKey(t), "test", 0, 0)
	anchorless, _ := ScopeFromContext(ctxWith(scopedAccessClaims([]string{})))
	for name, s := range map[string]Scope{"zero scope": {}, "anchorless scope": anchorless} {
		if _, err := iss.IssueScopedTenantAccess("acme", "c@x", nil, nil, s, "j"); err == nil {
			t.Errorf("IssueScopedTenantAccess minted from a %s", name)
		}
		if _, err := iss.IssueScopedOAuthAccess("acme", "c@x", nil, nil, "", nil, s, "", "j"); err == nil {
			t.Errorf("IssueScopedOAuthAccess minted from a %s", name)
		}
	}
	if _, err := NewScope(nil); !errors.Is(err, ErrMalformedScope) {
		t.Errorf("NewScope(nil) = %v", err)
	}
	if _, err := NewScope(manyAnchors(MaxScopeAnchors + 1)); !errors.Is(err, ErrMalformedScope) {
		t.Errorf("NewScope over the cap = %v", err)
	}
}

// A Scope's anchors cannot be changed through what it hands out.
func TestAScopeIsImmutable(t *testing.T) {
	in := []string{"customer:acme-corp"}
	s := mustScope(t, in...)
	in[0] = "customer:globex"
	out := s.Anchors()
	out[0] = "customer:globex"
	if s.Anchors()[0] != "customer:acme-corp" {
		t.Fatalf("a Scope's anchors changed through a slice it shared: %v", s.Anchors())
	}
}

// 🔴 CONFUSED DEPUTY, the functional half: a service token — which the enqueue
// validator's device lookup runs under — is never scoped, so the storage gate leaves it
// alone. It carries no scope because the parse refuses one on it (above).
func TestAServiceTokenIsNeverScoped(t *testing.T) {
	key := mustKey(t)
	st, err := NewIssuer(key, "test", 0, 0).IssueService("command-delivery", []string{string(DeviceRead)}, "j")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewValidator(&key.PublicKey).ValidateService(st.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, scoped := ScopeFromContext(ctxWith(c)); scoped {
		t.Fatal("a service token's caller reads as scoped")
	}
	if _, scoped := ScopeFromContext(context.Background()); scoped {
		t.Fatal("an unauthenticated context reads as scoped")
	}
}
