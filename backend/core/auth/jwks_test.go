// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rsa"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The kid stamped by an Issuer must equal the RFC 7638 thumbprint of its public
// key, and must be stable across recomputation.
func TestThumbprint_MatchesIssuerKidAndStable(t *testing.T) {
	key := mustKey(t)
	iss := NewIssuer(key, "test", time.Minute, time.Hour)

	tp := Thumbprint(&key.PublicKey)
	if tp == "" {
		t.Fatal("empty thumbprint")
	}
	if iss.Kid() != tp {
		t.Fatalf("issuer kid %q != thumbprint %q", iss.Kid(), tp)
	}
	if Thumbprint(&key.PublicKey) != tp {
		t.Fatal("thumbprint not deterministic")
	}
	other := mustKey(t)
	if Thumbprint(&other.PublicKey) == tp {
		t.Fatal("distinct keys produced the same thumbprint")
	}
}

// A public key survives the JWK encode/decode round-trip and a JWKS document
// round-trips into a key map keyed by thumbprint.
func TestJWKS_RoundTrip(t *testing.T) {
	k1, k2 := mustKey(t), mustKey(t)

	jwk := PublicKeyToJWK(&k1.PublicKey)
	got, err := jwk.PublicKey()
	if err != nil {
		t.Fatalf("JWK.PublicKey: %v", err)
	}
	if got.N.Cmp(k1.N) != 0 || got.E != k1.E {
		t.Fatal("JWK public-key round-trip mismatch")
	}

	doc, err := BuildJWKS([]*rsa.PublicKey{&k1.PublicKey, &k2.PublicKey})
	if err != nil {
		t.Fatalf("BuildJWKS: %v", err)
	}
	set, err := ParseJWKS(doc)
	if err != nil {
		t.Fatalf("ParseJWKS: %v", err)
	}
	keys, err := set.keyMap()
	if err != nil {
		t.Fatalf("keyMap: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[Thumbprint(&k1.PublicKey)] == nil || keys[Thumbprint(&k2.PublicKey)] == nil {
		t.Fatal("keyMap not keyed by thumbprint")
	}
}

func TestKeyMap_RejectsEmptyJWKS(t *testing.T) {
	set := &JWKS{}
	if _, err := set.keyMap(); err == nil {
		t.Fatal("keyMap accepted an empty JWKS")
	}
}

// A validator holding several keys selects the one named by the token's kid and
// rejects a token whose signing key it does not hold.
func TestValidate_SelectsKeyByKid(t *testing.T) {
	k1, k2, k3 := mustKey(t), mustKey(t), mustKey(t)
	v := NewValidatorFromKeys(map[string]*rsa.PublicKey{
		Thumbprint(&k1.PublicKey): &k1.PublicKey,
		Thumbprint(&k2.PublicKey): &k2.PublicKey,
	})

	tok2, _ := NewIssuer(k2, "test", time.Minute, time.Hour).IssueAccess("tenant-a", "alice", nil, "j2")
	if _, err := v.Validate(tok2.Token); err != nil {
		t.Fatalf("validator rejected a token signed by a held key: %v", err)
	}

	tok3, _ := NewIssuer(k3, "test", time.Minute, time.Hour).IssueAccess("tenant-a", "alice", nil, "j3")
	if _, err := v.Validate(tok3.Token); err == nil {
		t.Fatal("validator accepted a token whose kid it does not hold")
	}
}

// A refreshing validator picks up a rotated-in key the first time it sees that
// key's kid, without being rebuilt.
func TestValidate_LazyRefreshOnUnknownKid(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	refreshed := map[string]*rsa.PublicKey{Thumbprint(&newKey.PublicKey): &newKey.PublicKey}

	v := NewRefreshingValidator(
		map[string]*rsa.PublicKey{Thumbprint(&oldKey.PublicKey): &oldKey.PublicKey},
		func() (map[string]*rsa.PublicKey, error) { return refreshed, nil },
		time.Minute,
	)

	tok, _ := NewIssuer(newKey, "test", time.Minute, time.Hour).IssueAccess("tenant-a", "alice", nil, "jn")
	if _, err := v.Validate(tok.Token); err != nil {
		t.Fatalf("validator did not pick up the rotated-in key: %v", err)
	}
}

// The on-demand refresh is throttled: a burst of tokens bearing unknown kids
// triggers at most one refetch per interval.
func TestValidate_RefreshThrottled(t *testing.T) {
	k1, k2 := mustKey(t), mustKey(t)
	var mu sync.Mutex
	calls := 0
	v := NewRefreshingValidator(
		map[string]*rsa.PublicKey{}, // start empty so every token is an unknown kid
		func() (map[string]*rsa.PublicKey, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			// Return a set that still lacks the requested kids, forcing repeated misses.
			return map[string]*rsa.PublicKey{Thumbprint(&k1.PublicKey): &k1.PublicKey}, nil
		},
		time.Hour,
	)

	for i := 0; i < 5; i++ {
		tok, _ := NewIssuer(k2, "test", time.Minute, time.Hour).IssueAccess("tenant-a", "alice", nil, "j")
		_, _ = v.Validate(tok.Token) // expected to fail; we only assert refetch count
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly one throttled refresh, got %d", calls)
	}
}

// Regression: a multi-key validator still pins RS256 (alg-confusion blocked).
func TestValidate_MultiKeyStillRejectsAlgConfusion(t *testing.T) {
	key := mustKey(t)
	v := NewValidatorFromKeys(map[string]*rsa.PublicKey{Thumbprint(&key.PublicKey): &key.PublicKey})
	pubPEM, _ := EncodePublicKeyPEM(&key.PublicKey)
	forged := signRaw(t, jwt.SigningMethodHS256, pubPEM, accessClaims("tenant-a", time.Now().Add(time.Hour)))
	if _, err := v.Validate(forged); err == nil {
		t.Fatal("multi-key validator accepted an HS256 alg-confusion token")
	}
}
