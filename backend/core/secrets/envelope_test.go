// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"testing"
)

// newTestKP builds an instance KEK provider over a fresh random root key.
func newTestKP(t *testing.T) KeyProvider {
	t.Helper()
	root := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, root); err != nil {
		t.Fatalf("gen root key: %v", err)
	}
	kp, err := NewInstanceKeyProvider(root)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return kp
}

// TestSealOpenRoundTrip proves a value encrypted with a fresh DEK and a wrapped
// DEK round-trips back to the original plaintext, and that the envelope holds no
// cleartext.
func TestSealOpenRoundTrip(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	secret := []byte("sk-provider-abc123")

	env, err := Seal(ctx, kp, secret, nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if env.Alg != AlgAES256GCM || env.KEKVersion != instanceKEKVersion {
		t.Fatalf("envelope metadata wrong: alg=%q version=%d", env.Alg, env.KEKVersion)
	}
	if bytes.Contains(env.Ciphertext, secret) || bytes.Contains(env.WrappedDEK, secret) {
		t.Fatal("envelope must not contain the cleartext value")
	}

	got, err := Open(ctx, kp, env, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, secret)
	}
}

// TestSealFreshDEKPerValue proves two seals of the same plaintext produce
// distinct ciphertext, nonce, and wrapped DEK (a fresh DEK + fresh nonces each
// time), so identical secrets are not correlatable at rest.
func TestSealFreshDEKPerValue(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	secret := []byte("same-value")

	a, err := Seal(ctx, kp, secret, nil)
	if err != nil {
		t.Fatalf("seal a: %v", err)
	}
	b, err := Seal(ctx, kp, secret, nil)
	if err != nil {
		t.Fatalf("seal b: %v", err)
	}
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.Nonce, b.Nonce) || bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Fatal("two seals of the same value must differ in ciphertext, nonce, and wrapped DEK")
	}
}

// TestOpenTamperedCiphertext proves a flipped ciphertext byte fails the GCM auth
// tag rather than returning corrupted plaintext.
func TestOpenTamperedCiphertext(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	env, err := Seal(ctx, kp, []byte("value"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.Ciphertext[0] ^= 0xFF
	if _, err := Open(ctx, kp, env, nil); err == nil {
		t.Fatal("open must fail on a tampered ciphertext")
	}
}

// TestOpenTamperedWrappedDEK proves a flipped wrapped-DEK byte fails closed (the
// wrap layer's own GCM auth tag catches it) instead of yielding a bad DEK.
func TestOpenTamperedWrappedDEK(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	env, err := Seal(ctx, kp, []byte("value"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.WrappedDEK[len(env.WrappedDEK)-1] ^= 0xFF
	if _, err := Open(ctx, kp, env, nil); err == nil {
		t.Fatal("open must fail on a tampered wrapped DEK")
	}
}

// TestOpenWrongKEK proves an envelope sealed under one instance root key cannot
// be opened under a different one — the wrong KEK fails the wrap auth tag.
func TestOpenWrongKEK(t *testing.T) {
	ctx := context.Background()
	sealer := newTestKP(t)
	other := newTestKP(t)
	env, err := Seal(ctx, sealer, []byte("value"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(ctx, other, env, nil); err == nil {
		t.Fatal("open must fail under a different KEK")
	}
}

// TestAADBinding proves the additional-authenticated-data (the secret's identity)
// is bound into the ciphertext: opening with a different aad fails, so a
// ciphertext cannot be relabeled onto another handle, while the matching aad
// still opens.
func TestAADBinding(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	aad := []byte("tenant/acme/ai/provider/anthropic")
	env, err := Seal(ctx, kp, []byte("key"), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(ctx, kp, env, []byte("tenant/evil/ai/provider/anthropic")); err == nil {
		t.Fatal("open must fail when the aad does not match")
	}
	if _, err := Open(ctx, kp, env, nil); err == nil {
		t.Fatal("open must fail when aad is dropped")
	}
	got, err := Open(ctx, kp, env, aad)
	if err != nil {
		t.Fatalf("open with matching aad: %v", err)
	}
	if string(got) != "key" {
		t.Fatalf("aad round-trip mismatch: %q", got)
	}
}

// TestOpenRejectsBadEnvelope proves the fail-closed guards on Open: a nil
// envelope and an unknown value algorithm are both rejected.
func TestOpenRejectsBadEnvelope(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	if _, err := Open(ctx, kp, nil, nil); err == nil {
		t.Fatal("open must reject a nil envelope")
	}
	env, err := Seal(ctx, kp, []byte("v"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	env.Alg = "rot13"
	if _, err := Open(ctx, kp, env, nil); err == nil {
		t.Fatal("open must reject an unknown value algorithm")
	}
}

// TestSealEmptyValue proves an empty (but present) secret round-trips — distinct
// from "no secret", which the store layer represents by absence, not an empty
// value.
func TestSealEmptyValue(t *testing.T) {
	ctx := context.Background()
	kp := newTestKP(t)
	env, err := Seal(ctx, kp, []byte{}, nil)
	if err != nil {
		t.Fatalf("seal empty: %v", err)
	}
	got, err := Open(ctx, kp, env, nil)
	if err != nil {
		t.Fatalf("open empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty round-trip yielded %d bytes", len(got))
	}
}
