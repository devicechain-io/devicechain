// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

const testResource = "https://mcp.example.com"

func mustIssuerValidator(t *testing.T) (*coreauth.Issuer, func() *coreauth.Validator) {
	t.Helper()
	key, err := coreauth.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	iss := coreauth.NewIssuer(key, "https://as.example.com", time.Minute, time.Hour)
	v := coreauth.NewValidator(&key.PublicKey)
	return iss, func() *coreauth.Validator { return v }
}

// A token bound to this resource's audience validates and yields the caller's
// scope, tenant, and forwardable token.
func TestVerifier_AudienceBoundTokenAccepted(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	verify := NewTokenVerifier(validator, testResource)

	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"},
		[]string{"device:read"}, coreauth.ScopeReadOnly, []string{testResource}, false, "jti-1")
	if err != nil {
		t.Fatalf("IssueOAuthAccess: %v", err)
	}
	info, err := verify(context.Background(), tok.Token, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if info.UserID != "acme/a@b.c" {
		t.Errorf("UserID = %q, want acme/a@b.c (tenant-qualified)", info.UserID)
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != coreauth.ScopeReadOnly {
		t.Errorf("Scopes = %v, want [read-only]", info.Scopes)
	}
	if info.Extra[extraTokenKey] != tok.Token {
		t.Errorf("forwarded token not set in Extra")
	}
	if info.Extra[extraTenantKey] != "acme" {
		t.Errorf("tenant = %v, want acme", info.Extra[extraTenantKey])
	}
}

// A token bound to a DIFFERENT audience is rejected — the anti-confused-deputy
// binding. This is the load-bearing RS control.
func TestVerifier_WrongAudienceRejected(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	verify := NewTokenVerifier(validator, testResource)

	tok, _ := iss.IssueOAuthAccess("acme", "a@b.c", nil, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{"https://some-other-resource"}, false, "jti-2")
	_, err := verify(context.Background(), tok.Token, nil)
	if !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Errorf("wrong-audience token: err = %v, want ErrInvalidToken", err)
	}
}

// A token with NO audience (e.g. an ordinary console access token) is rejected —
// only tokens explicitly minted for this resource are accepted.
func TestVerifier_NoAudienceRejected(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	verify := NewTokenVerifier(validator, testResource)

	tok, _ := iss.IssueTenantAccess("acme", "a@b.c", nil, []string{"device:read"}, false, "jti-3")
	_, err := verify(context.Background(), tok.Token, nil)
	if !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Errorf("no-audience token: err = %v, want ErrInvalidToken", err)
	}
}

// Before the readiness gate opens the validator is nil — every token is rejected,
// fail closed.
func TestVerifier_NilValidatorFailsClosed(t *testing.T) {
	verify := NewTokenVerifier(func() *coreauth.Validator { return nil }, testResource)
	_, err := verify(context.Background(), "any-token", nil)
	if !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Errorf("nil validator: err = %v, want ErrInvalidToken", err)
	}
}

// A garbage / unsigned token is rejected.
func TestVerifier_BadTokenRejected(t *testing.T) {
	_, validator := mustIssuerValidator(t)
	verify := NewTokenVerifier(validator, testResource)
	_, err := verify(context.Background(), "not.a.jwt", nil)
	if !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Errorf("bad token: err = %v, want ErrInvalidToken", err)
	}
}
