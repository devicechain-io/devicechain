// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mustKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return key
}

// signRaw signs arbitrary claims with the given method/key so tests can forge
// tokens the well-behaved Issuer would never produce.
func signRaw(t *testing.T, method jwt.SigningMethod, key interface{}, claims *Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func accessClaims(tenant string, exp time.Time) *Claims {
	return &Claims{
		Tenant:    tenant,
		Username:  "alice",
		TokenType: TokenTypeAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(exp.Add(-time.Hour)),
		},
	}
}

func TestValidate_RoundTrip(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueAccess("tenant-a", "alice", []string{"admin"}, []string{string(AuthorityAll)}, "jti-1")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	claims, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Tenant != "tenant-a" || claims.Username != "alice" || claims.TokenType != TokenTypeAccess {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("unexpected roles: %+v", claims.Roles)
	}
	if !claims.HasAuthority(DeviceWrite) {
		t.Fatalf("expected super-authority to grant device:write: %+v", claims.Authorities)
	}
}

// The algorithm-confusion attack: forge an HS256 token using the RSA *public*
// key bytes as the HMAC secret. A naive verifier that trusts the token's "alg"
// would accept it. Our validator pins RS256 and must reject.
func TestValidate_RejectsAlgConfusion(t *testing.T) {
	key := mustKey(t)
	v := NewValidator(&key.PublicKey)

	pubPEM, err := EncodePublicKeyPEM(&key.PublicKey)
	if err != nil {
		t.Fatalf("EncodePublicKeyPEM: %v", err)
	}
	forged := signRaw(t, jwt.SigningMethodHS256, pubPEM, accessClaims("tenant-a", time.Now().Add(time.Hour)))

	if _, err := v.Validate(forged); err == nil {
		t.Fatal("validator accepted an HS256 token signed with the public key — alg-confusion not blocked")
	}
}

func TestValidate_RejectsExpired(t *testing.T) {
	key := mustKey(t)
	v := NewValidator(&key.PublicKey)
	expired := signRaw(t, jwt.SigningMethodRS256, key, accessClaims("tenant-a", time.Now().Add(-time.Minute)))
	if _, err := v.Validate(expired); err == nil {
		t.Fatal("validator accepted an expired token")
	}
}

func TestValidate_RejectsWrongKey(t *testing.T) {
	signer := mustKey(t)
	other := mustKey(t)
	v := NewValidator(&other.PublicKey)
	tok := signRaw(t, jwt.SigningMethodRS256, signer, accessClaims("tenant-a", time.Now().Add(time.Hour)))
	if _, err := v.Validate(tok); err == nil {
		t.Fatal("validator accepted a token signed by a different key")
	}
}

func TestValidate_RejectsMissingTenant(t *testing.T) {
	key := mustKey(t)
	v := NewValidator(&key.PublicKey)
	tok := signRaw(t, jwt.SigningMethodRS256, key, accessClaims("", time.Now().Add(time.Hour)))
	if _, err := v.Validate(tok); err == nil {
		t.Fatal("validator accepted an access token with no tenant claim")
	}
}

// A refresh token must not be accepted on the API path, and vice-versa.
func TestValidate_TokenTypeSeparation(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	access, _ := iss.IssueAccess("tenant-a", "alice", nil, nil, "a")
	refresh, _ := iss.IssueRefresh("tenant-a", "alice", nil, nil, "r")

	if _, err := v.Validate(refresh.Token); err == nil {
		t.Fatal("Validate accepted a refresh token")
	}
	if _, err := v.ValidateRefresh(access.Token); err == nil {
		t.Fatal("ValidateRefresh accepted an access token")
	}
	if _, err := v.ValidateRefresh(refresh.Token); err != nil {
		t.Fatalf("ValidateRefresh rejected a valid refresh token: %v", err)
	}
}

func TestKeyPEM_RoundTrip(t *testing.T) {
	key := mustKey(t)
	privPEM, err := EncodePrivateKeyPEM(key)
	if err != nil {
		t.Fatalf("EncodePrivateKeyPEM: %v", err)
	}
	gotPriv, err := DecodePrivateKeyPEM(privPEM)
	if err != nil {
		t.Fatalf("DecodePrivateKeyPEM: %v", err)
	}
	if gotPriv.N.Cmp(key.N) != 0 {
		t.Fatal("private key round-trip mismatch")
	}
	pubPEM, err := EncodePublicKeyPEM(&key.PublicKey)
	if err != nil {
		t.Fatalf("EncodePublicKeyPEM: %v", err)
	}
	if _, err := NewValidatorFromPEM(pubPEM); err != nil {
		t.Fatalf("NewValidatorFromPEM: %v", err)
	}
}

func TestIssueIdentity_RoundTripAndTierIsolation(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	// An identity token carries no tenant but a system authority, and round-trips
	// through ValidateIdentity.
	idt, err := iss.IssueIdentity("alice@example.com", []string{"superuser"}, []string{string(AuthorityAll)}, "jti-id")
	if err != nil {
		t.Fatalf("IssueIdentity: %v", err)
	}
	claims, err := v.ValidateIdentity(idt.Token)
	if err != nil {
		t.Fatalf("ValidateIdentity: %v", err)
	}
	if claims.Tenant != "" || claims.Email != "alice@example.com" || claims.TokenType != TokenTypeIdentity {
		t.Fatalf("unexpected identity claims: %+v", claims)
	}

	// Tier isolation: the data-plane Validate must reject an identity token (wrong
	// type, and it has no tenant claim either).
	if _, err := v.Validate(idt.Token); err == nil {
		t.Fatal("data-plane Validate accepted an identity token")
	}

	// And ValidateIdentity must reject a tenant access token (wrong type).
	at, err := iss.IssueAccess("tenant-a", "alice", nil, nil, "jti-a")
	if err != nil {
		t.Fatalf("IssueAccess: %v", err)
	}
	if _, err := v.ValidateIdentity(at.Token); err == nil {
		t.Fatal("ValidateIdentity accepted a tenant access token")
	}
}

func TestIssueTenantAccess_CarriesEmailAndSudo(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)
	v := NewValidator(&key.PublicKey)

	tok, err := iss.IssueTenantAccess("tenant-a", "su@example.com", []string{"superuser"}, []string{string(AuthorityAll)}, true, "jti-su")
	if err != nil {
		t.Fatalf("IssueTenantAccess: %v", err)
	}
	claims, err := v.Validate(tok.Token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.Tenant != "tenant-a" || claims.Email != "su@example.com" || !claims.ActingAsSuperuser {
		t.Fatalf("unexpected tenant claims: %+v", claims)
	}
}
