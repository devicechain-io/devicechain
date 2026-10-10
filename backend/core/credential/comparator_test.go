// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// Each kind's comparator is fixed in core. These tests pin which compare each kind
// gets, from the outcomes a caller sees.

var device = credential.Principal{Kind: credential.KindDeviceCredential, ID: "acme:dev-1"}

// plaintext is a lookup returning a stored value as it is.
func plaintext(stored string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return stored, nil }
}

// testRootKey is a fixed root key, so a digest made in one test verifies in another.
var testRootKey = []byte("0123456789abcdef0123456789abcdef")

// testDeviceKey is the device secret key every test Checker is built with.
func testDeviceKey(t testing.TB) *credential.DeviceSecretKey {
	t.Helper()
	k, err := credential.DeriveDeviceSecretKey(testRootKey)
	require.NoError(t, err)
	return k
}

// digestOf is a lookup returning secret's stored digest under the test key.
func digestOf(t testing.TB, secret string) func(context.Context) (string, error) {
	t.Helper()
	d, err := testDeviceKey(t).Digest(secret)
	require.NoError(t, err)
	return plaintext(d)
}

func allKinds(p credential.Policy) map[credential.Kind]credential.Policy {
	return map[credential.Kind]credential.Policy{
		credential.KindIdentity: p, credential.KindOAuthClient: p, credential.KindDeviceCredential: p,
	}
}

// The device kind compares against a stored keyed digest: the right secret matches and a
// wrong one does not, byte for byte.
func TestDeviceKindComparesItsDigest(t *testing.T) {
	// Enough free attempts that every wrong secret below is evaluated, not throttled.
	c := newCheckerFor(t, credentialtest.NewStore(), allKinds(credential.Policy{Free: 10, Base: time.Second, Cap: time.Minute}))
	require.NoError(t, c.Check(context.Background(), device, "s3cret", digestOf(t, "s3cret")))
	for _, wrong := range []string{"s3creT", "s3cret-extra", "s3cre", "x", " s3cret"} {
		require.ErrorIs(t, c.Check(context.Background(), device, wrong, digestOf(t, "s3cret")),
			credential.ErrMismatch, "presented %q", wrong)
	}
}

// 🔴 A STORED PLAINTEXT NEVER AUTHENTICATES ANY KIND. For a person or an OAuth client the
// only stored value that may match is a bcrypt hash, and for a device it is a digest: a
// store still holding the password itself (a row the upgrade did not digest) must refuse,
// not fall back to comparing it.
func TestNoKindAuthenticatesAgainstAStoredPlaintext(t *testing.T) {
	c := newCheckerFor(t, credentialtest.NewStore(), allKinds(testPolicy))
	for _, k := range []credential.Kind{credential.KindIdentity, credential.KindOAuthClient, credential.KindDeviceCredential} {
		p := credential.Principal{Kind: k, ID: "someone"}
		require.ErrorIs(t, c.Check(context.Background(), p, secret, plaintext(secret)), credential.ErrMismatch,
			"kind %s authenticated against a stored PLAINTEXT secret", k)
	}
	// Controls: each kind does match its own stored form, so the refusals above are about
	// the stored value's form and not a checker that refuses everything.
	for _, k := range []credential.Kind{credential.KindIdentity, credential.KindOAuthClient} {
		require.NoError(t, c.Check(context.Background(), credential.Principal{Kind: k, ID: "someone"}, secret, newAccount(t).lookup), "kind %s", k)
	}
	require.NoError(t, c.Check(context.Background(), device, secret, digestOf(t, secret)))
}

// An unknown device credential pays exactly one compare, against the device kind's own
// dummy — a digest under the device key, the shape of a stored one, not the bcrypt hash
// the other kinds use — is refused, and is charged like a real one.
func TestDeviceUnknownPrincipalPaysDummyCompare(t *testing.T) {
	store := credentialtest.NewStore()
	c := newCheckerFor(t, store, allKinds(testPolicy))
	var seen [][]byte
	credential.ObserveCompares(c, func(h []byte) { seen = append(seen, h) })

	require.ErrorIs(t, c.Check(context.Background(), device, "anything", plaintext("")), credential.ErrMismatch)
	require.Len(t, seen, 1, "an unknown device credential must pay exactly one compare")
	assert.Equal(t, credential.Dummy(c, credential.KindDeviceCredential), seen[0])
	_, err := bcrypt.Cost(seen[0])
	assert.Error(t, err, "the device kind's dummy must be a digest like its stored values, not a bcrypt hash")
	assert.NoError(t, testDeviceKey(t).Recognizes(string(seen[0])), "the dummy must be a digest under the device key")
	assert.Equal(t, []string{"get:not-found", "create:ok"}, store.Ops(), "the attempt must be charged")
}

// A stored empty secret never matches, not even an empty presented one.
func TestDeviceEmptyStoredNeverMatchesEmpty(t *testing.T) {
	c := newCheckerFor(t, credentialtest.NewStore(), allKinds(testPolicy))
	require.ErrorIs(t, c.Check(context.Background(), device, "", plaintext("")), credential.ErrMismatch)
}

// Declaring the device kind without its key is refused at construction, not discovered
// at the first connect.
func TestNewCheckerRefusesTheDeviceKindWithoutAKey(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindDeviceCredential: testPolicy})
	require.ErrorIs(t, err, credential.ErrNoDeviceSecretKey)
	// A Checker of the bcrypt kinds needs no device key.
	_, err = credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindIdentity: testPolicy})
	require.NoError(t, err)
}

// Construction declares kinds by the policy map's keys, and refuses a map with none
// and a kind core has no comparator for.
func TestNewCheckerRefusesUnknownKindAndEmptyMap(t *testing.T) {
	_, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{})
	require.ErrorContains(t, err, "at least one kind")

	_, err = credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIdentity: testPolicy, "device-credentail": testPolicy,
	})
	require.ErrorIs(t, err, credential.ErrUnknownKind)
	require.ErrorContains(t, err, `"device-credentail"`)

	// A map declaring any subset of the known kinds is accepted.
	_, err = credential.NewChecker(credentialtest.NewStore(),
		map[credential.Kind]credential.Policy{credential.KindDeviceCredential: testPolicy},
		credential.WithDeviceSecretKey(testDeviceKey(t)))
	require.NoError(t, err)
}

// A Check on a kind the Checker was not built with fails with a named error, counted
// as an error, with nothing read, charged, looked up or compared.
func TestCheckUndeclaredKindFailsLoudly(t *testing.T) {
	store := credentialtest.NewStore()
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total"}, []string{"kind", "outcome"})
	c, err := credential.NewChecker(store,
		map[credential.Kind]credential.Policy{credential.KindDeviceCredential: testPolicy},
		credential.WithClock(newClock().Now), credential.WithCounter(counter), credential.WithDeviceSecretKey(testDeviceKey(t)))
	require.NoError(t, err)
	compares := 0
	credential.ObserveCompares(c, func([]byte) { compares++ })
	acct := newAccount(t)

	err = c.Check(context.Background(), alice, secret, acct.lookup)
	require.ErrorIs(t, err, credential.ErrUndeclaredKind)
	assert.False(t, errors.Is(err, credential.ErrMismatch))
	assert.Zero(t, acct.calls.Load(), "an undeclared kind must not be looked up")
	assert.Zero(t, compares, "an undeclared kind must not be compared")
	assert.Empty(t, store.Ops(), "an undeclared kind must not touch the attempt store")
	assert.Equal(t, 1.0, testutil.ToFloat64(counter.WithLabelValues(string(credential.KindIdentity), credential.OutcomeError)))
}

func newCheckerFor(t *testing.T, store credential.Store, p map[credential.Kind]credential.Policy) *credential.Checker {
	t.Helper()
	c, err := credential.NewChecker(store, p, credential.WithClock(newClock().Now), credential.WithDeviceSecretKey(testDeviceKey(t)))
	require.NoError(t, err)
	return c
}
