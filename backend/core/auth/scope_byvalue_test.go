// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// This file uses only API that existed before scoped memberships (ADR-081), so each
// test here compiles against the tree that preceded them and FAILS THERE BY VALUE. That
// is the evidence the slice closes something rather than adding a test that could only
// ever pass.

// signWithScope signs an access token whose payload carries "scp" with a raw claim map,
// the way any client able to obtain a signed token would see it.
func signWithScope(t *testing.T, scp any) (*Validator, string) {
	t.Helper()
	key, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{
		"tenant":      "acme",
		"username":    "c@acme-corp.example",
		"authorities": []string{"*"},
		"typ":         "access",
		"exp":         time.Now().Add(time.Minute).Unix(),
	}
	if scp != nil {
		claims["scp"] = scp
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = Thumbprint(&key.PublicKey)
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return NewValidator(&key.PublicKey), signed
}

// 🔴 THE LEAK THE SLICE CLOSES. A token whose payload names a scope must not pass a
// resolver that has not opted in to scoped callers, whatever authorities it holds — here
// "*". Before scoped memberships the claim was an unknown field, silently dropped by the
// decoder, and Authorize admitted the bearer to the whole tenant.
func TestScopedTokenIsRefusedByAuthorize(t *testing.T) {
	v, token := signWithScope(t, []string{"customer:acme-corp"})
	claims, err := v.Parse(token)
	if err != nil {
		t.Fatalf("a well-formed scoped access token must parse: %v", err)
	}
	ctx := WithClaims(context.Background(), claims)
	if err := Authorize(ctx, DeviceRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Authorize admitted a scoped caller to device:read: %v", err)
	}
	if err := AuthorizeAny(ctx, DeviceRead, EventRead); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AuthorizeAny admitted a scoped caller: %v", err)
	}

	// Negative control: the same token without "scp" is admitted, so the refusal is
	// the scope and not the fixture.
	v2, plain := signWithScope(t, nil)
	pc, err := v2.Parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := Authorize(WithClaims(context.Background(), pc), DeviceRead); err != nil {
		t.Fatalf("the unscoped control was refused: %v", err)
	}
}

// A token carrying an EMPTY scope is refused at the parse. The issuer never writes one
// (the claim is omitted when unscoped), so it was minted by something else; reading it
// as "unscoped" would make "scoped to nothing" mean "the whole tenant".
func TestAnEmptyScopeClaimIsRefusedAtParse(t *testing.T) {
	v, token := signWithScope(t, []string{})
	if _, err := v.Parse(token); err == nil {
		t.Fatal("an access token with an empty scp claim was admitted")
	}
}
