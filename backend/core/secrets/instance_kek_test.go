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

func testRootKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return k
}

// TestInstanceKEKWrapUnwrap proves a DEK wrapped by the instance provider
// unwraps back to the same bytes with the current version, and the wrapped form
// exposes neither the DEK nor the KEK.
func TestInstanceKEKWrapUnwrap(t *testing.T) {
	ctx := context.Background()
	root := testRootKey(t)
	kp, err := NewInstanceKeyProvider(root)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	dek := testRootKey(t) // any 32-byte secret stands in for a DEK

	wrapped, version, err := kp.Wrap(ctx, dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if version != instanceKEKVersion {
		t.Fatalf("version = %d, want %d", version, instanceKEKVersion)
	}
	if bytes.Contains(wrapped, dek) || bytes.Contains(wrapped, root) {
		t.Fatal("wrapped DEK must not contain the DEK or the KEK")
	}
	got, err := kp.Unwrap(ctx, wrapped, version)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrap did not recover the DEK")
	}
}

// TestInstanceKEKRejectsBadRootKey proves a root key of the wrong length is
// rejected fail-closed (a service that cannot form its KEK must not start).
func TestInstanceKEKRejectsBadRootKey(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := NewInstanceKeyProvider(make([]byte, n)); err == nil {
			t.Fatalf("a %d-byte root key must be rejected", n)
		}
	}
	if _, err := NewInstanceKeyProvider(testRootKey(t)); err != nil {
		t.Fatalf("a 32-byte root key must be accepted: %v", err)
	}
}

// TestInstanceKEKUnwrapUnknownVersion proves unwrapping a version this
// single-version provider did not seal fails closed rather than silently using
// the current key.
func TestInstanceKEKUnwrapUnknownVersion(t *testing.T) {
	ctx := context.Background()
	kp, _ := NewInstanceKeyProvider(testRootKey(t))
	wrapped, version, err := kp.Wrap(ctx, testRootKey(t))
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if _, err := kp.Unwrap(ctx, wrapped, version+1); err == nil {
		t.Fatal("unwrap must fail on an unknown KEK version")
	}
}

// TestInstanceKEKUnwrapShortInput proves a wrapped blob shorter than the nonce is
// rejected rather than panicking on the slice.
func TestInstanceKEKUnwrapShortInput(t *testing.T) {
	ctx := context.Background()
	kp, _ := NewInstanceKeyProvider(testRootKey(t))
	if _, err := kp.Unwrap(ctx, []byte{0x00, 0x01}, instanceKEKVersion); err == nil {
		t.Fatal("unwrap must reject a too-short wrapped DEK")
	}
}

// TestInstanceKEKRootKeyCopied proves the provider copies the root key, so a
// caller zeroing its own buffer after construction cannot corrupt the KEK.
func TestInstanceKEKRootKeyCopied(t *testing.T) {
	ctx := context.Background()
	root := testRootKey(t)
	kp, _ := NewInstanceKeyProvider(root)
	wrapped, version, err := kp.Wrap(ctx, testRootKey(t))
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	zero(root) // caller wipes its buffer
	if _, err := kp.Unwrap(ctx, wrapped, version); err != nil {
		t.Fatalf("provider must hold its own KEK copy: %v", err)
	}
}

// TestInstanceKEKName pins the config identifier.
func TestInstanceKEKName(t *testing.T) {
	kp, _ := NewInstanceKeyProvider(testRootKey(t))
	if kp.Name() != InstanceKEKProvider {
		t.Fatalf("Name() = %q, want %q", kp.Name(), InstanceKEKProvider)
	}
}
