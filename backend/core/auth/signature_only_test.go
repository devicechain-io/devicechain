// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ParseSignatureOnly returns an EXPIRED token we signed, and refuses an expired token
// someone else signed (naming our kid): skipping the claims must not skip the signature.
func TestParseSignatureOnlyAcceptsOurExpiredTokenAndRefusesAForgedOne(t *testing.T) {
	key, other := mustKey(t), mustKey(t)
	v := NewValidator(&key.PublicKey)

	sign := func(k interface{}, kid string) string {
		now := time.Now()
		c := Claims{SessionID: "sid-1", TokenType: TokenTypeIdentity, RegisteredClaims: jwt.RegisteredClaims{
			ID: "j", IssuedAt: jwt.NewNumericDate(now.Add(-2 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-time.Hour)),
		}}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &c)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	ours := sign(key, Thumbprint(&key.PublicKey))
	// A forgery naming OUR kid, signed with another key.
	forged := sign(other, Thumbprint(&key.PublicKey))

	if _, err := v.Parse(ours); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("control: Parse of our expired token = %v, want ErrTokenExpired", err)
	}

	claims, err := v.ParseSignatureOnly(ours)
	if err != nil {
		t.Fatalf("ParseSignatureOnly refused our expired token: %v", err)
	}
	if claims.SessionID != "sid-1" {
		t.Errorf("sid = %q, want sid-1", claims.SessionID)
	}
	if _, err := v.ParseSignatureOnly(forged); err == nil {
		t.Fatal("ParseSignatureOnly accepted a token signed with another key")
	}
	if _, err := v.ParseSignatureOnly("not-a-token"); err == nil {
		t.Fatal("ParseSignatureOnly accepted garbage")
	}
}
