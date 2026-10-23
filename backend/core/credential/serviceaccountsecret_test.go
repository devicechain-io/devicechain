// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
)

var saRoot = []byte("service-account-test-root-key-32")

func saChecker(t *testing.T, opts ...credential.Option) *credential.Checker {
	t.Helper()
	c, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindServiceAccount: {Unthrottled: true}}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func saKey(t *testing.T) *credential.DeviceSecretKey {
	t.Helper()
	k, err := credential.DeriveServiceAccountSecretKey(saRoot)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The kind cannot be declared without its key: a checker that could not compare the
// stored digests would refuse every correct secret, far from the cause.
func TestServiceAccountKindNeedsItsKey(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindServiceAccount: {Unthrottled: true}})
	if !errors.Is(err, credential.ErrNoServiceAccountSecretKey) {
		t.Fatalf("NewChecker without the key = %v, want ErrNoServiceAccountSecretKey", err)
	}
	// The DEVICE key does not stand in for it.
	dk, _ := credential.DeriveDeviceSecretKey(saRoot)
	_, err = credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindServiceAccount: {Unthrottled: true}},
		credential.WithDeviceSecretKey(dk))
	if !errors.Is(err, credential.ErrNoServiceAccountSecretKey) {
		t.Fatalf("NewChecker with only the device key = %v, want ErrNoServiceAccountSecretKey", err)
	}
}

func TestServiceAccountSecretCompare(t *testing.T) {
	k := saKey(t)
	c := saChecker(t, credential.WithServiceAccountSecretKey(k))
	stored, err := k.Digest("acme", "dcsas_correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	check := func(tenant, secret, hash string) error {
		return c.Check(context.Background(), credential.Principal{Kind: credential.KindServiceAccount, ID: "dcsa_x", Tenant: tenant},
			secret, func(context.Context) (string, error) { return hash, nil })
	}
	if err := check("acme", "dcsas_correct-horse-battery-staple", stored); err != nil {
		t.Fatalf("correct secret refused: %v", err)
	}
	// Negative controls: each differs from the match above in exactly one input.
	if err := check("acme", "dcsas_correct-horse-battery-stapl", stored); !errors.Is(err, credential.ErrMismatch) {
		t.Fatalf("wrong secret = %v, want ErrMismatch", err)
	}
	if err := check("globex", "dcsas_correct-horse-battery-staple", stored); !errors.Is(err, credential.ErrMismatch) {
		t.Fatalf("digest transplanted to another tenant = %v, want ErrMismatch", err)
	}
	if err := check("", "dcsas_correct-horse-battery-staple", ""); !errors.Is(err, credential.ErrMismatch) {
		t.Fatalf("unknown account = %v, want ErrMismatch", err)
	}
}

// An unknown client_id pays a real compare against the kind's dummy, so it costs what a
// known account's wrong secret does.
func TestServiceAccountUnknownPaysTheDummy(t *testing.T) {
	k := saKey(t)
	c := saChecker(t, credential.WithServiceAccountSecretKey(k))
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, append([]byte(nil), h...)) })
	_ = c.Check(context.Background(), credential.Principal{Kind: credential.KindServiceAccount, ID: "dcsa_nobody"},
		"anything", func(context.Context) (string, error) { return "", nil })
	if len(seen) != 1 || !bytes.Equal(seen[0], credential.Dummy(c, credential.KindServiceAccount)) {
		t.Fatalf("unknown account compared against %q, want the dummy", seen)
	}
	if err := k.Recognizes(string(seen[0])); err != nil {
		t.Fatalf("the dummy is not a digest of this key: %v", err)
	}
}

// The service-account key and the device key are unrelated: a digest made under one is not
// recognized, let alone verified, under the other.
func TestServiceAccountKeyIsNotTheDeviceKey(t *testing.T) {
	sk := saKey(t)
	dk, err := credential.DeriveDeviceSecretKey(saRoot)
	if err != nil {
		t.Fatal(err)
	}
	if sk.KeyId() == dk.KeyId() {
		t.Fatal("service-account and device keys share a key id")
	}
	d, err := dk.Digest("acme", "a-device-password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := sk.Recognizes(d); !errors.Is(err, credential.ErrDeviceSecretUnrecognized) {
		t.Fatalf("a device digest was recognized by the service-account key: %v", err)
	}
}
