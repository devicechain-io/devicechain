// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package webhooksig

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Standard Webhooks reference vector (the example in the specification's README). The
// signature is the specification's own, not one this package produced, so a drift in the signed
// content, the key handling or the encoding fails here and not in a customer's verifier.
const (
	specSecret    = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	specID        = "msg_p5jXN8AQM9LWM0D4loKWxJek"
	specTimestamp = int64(1614265330)
	specBody      = `{"test": 2432232314}`
	specSignature = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
)

const (
	vecSecret    = "whsec_CwwNDg8QERITFBUWFxgZGhscHR4fICEi"
	vecSignature = "v1,jYu2Jbd7rWwzpf8sMZ6i/WprdZlQkyiD8uafra7JVIM="
)

func TestSignMatchesTheSpecVector(t *testing.T) {
	got, err := Sign(specID, time.Unix(specTimestamp, 0), []byte(specBody), specSecret)
	require.NoError(t, err)
	assert.Equal(t, specSignature, got)
}

// A second, independent vector, computed outside this package with
//
//	printf '%s' 'msg_dc_1.1700000000.{"alarm":"a1"}' | openssl dgst -sha256 -hmac "$KEY" -binary | base64
//
// over the key's decoded bytes (the 24 bytes 0x0b, 0x0c, ... 0x22), and cross-checked against
// Python's hmac. A vector that only the code
// under test has ever produced proves it agrees with itself.
func TestSignMatchesAnIndependentVector(t *testing.T) {
	got, err := Sign("msg_dc_1", time.Unix(1700000000, 0), []byte(`{"alarm":"a1"}`), vecSecret)
	require.NoError(t, err)
	assert.Equal(t, vecSignature, got)
}

func TestVerifyAcceptsTheSpecVector(t *testing.T) {
	now := time.Unix(specTimestamp, 0).Add(time.Minute)
	require.NoError(t, Verify(hdr(specID, "1614265330", specSignature), []byte(specBody), now, 0, specSecret))
}

func hdr(id, ts, sig string) http.Header {
	h := http.Header{}
	if id != "" {
		h.Set(HeaderID, id)
	}
	if ts != "" {
		h.Set(HeaderTimestamp, ts)
	}
	if sig != "" {
		h.Set(HeaderSignature, sig)
	}
	return h
}

func TestVerifyRejectsATamperedBody(t *testing.T) {
	now := time.Unix(specTimestamp, 0)
	err := Verify(hdr(specID, "1614265330", specSignature), []byte(`{"test": 2432232315}`), now, 0, specSecret)
	assert.ErrorIs(t, err, ErrBadSignature)
}

func TestVerifyRejectsATamperedIDOrTimestamp(t *testing.T) {
	now := time.Unix(specTimestamp, 0)
	assert.ErrorIs(t, Verify(hdr("msg_other", "1614265330", specSignature), []byte(specBody), now, 0, specSecret), ErrBadSignature)
	// A timestamp moved inside the window still changes the signed content.
	assert.ErrorIs(t, Verify(hdr(specID, "1614265331", specSignature), []byte(specBody), now, 0, specSecret), ErrBadSignature)
}

func TestVerifyRejectsAStaleTimestampInEitherDirection(t *testing.T) {
	sig, err := Sign(specID, time.Unix(specTimestamp, 0), []byte(specBody), specSecret)
	require.NoError(t, err)
	h := hdr(specID, "1614265330", sig)
	signedAt := time.Unix(specTimestamp, 0)

	assert.NoError(t, Verify(h, []byte(specBody), signedAt.Add(DefaultTolerance), 0, specSecret), "at the edge")
	assert.ErrorIs(t, Verify(h, []byte(specBody), signedAt.Add(DefaultTolerance+time.Second), 0, specSecret), ErrStale, "replayed late")
	assert.ErrorIs(t, Verify(h, []byte(specBody), signedAt.Add(-DefaultTolerance-time.Second), 0, specSecret), ErrStale, "from the future")
	assert.ErrorIs(t, Verify(h, []byte(specBody), signedAt.Add(time.Minute), 30*time.Second, specSecret), ErrStale, "custom tolerance")
}

func TestVerifyRejectsMissingAndMalformedHeaders(t *testing.T) {
	now := time.Unix(specTimestamp, 0)
	for name, h := range map[string]http.Header{
		"no id":        hdr("", "1614265330", specSignature),
		"no timestamp": hdr(specID, "", specSignature),
		"no signature": hdr(specID, "1614265330", ""),
	} {
		assert.ErrorIs(t, Verify(h, []byte(specBody), now, 0, specSecret), ErrMissingHeaders, name)
	}
	assert.ErrorIs(t, Verify(hdr(specID, "yesterday", specSignature), []byte(specBody), now, 0, specSecret), ErrBadTimestamp)
	assert.ErrorIs(t, Verify(hdr(specID, "1614265330", specSignature), []byte(specBody), now, 0), ErrNoSecrets)
}

func TestVerifyRejectsTheWrongKey(t *testing.T) {
	other, err := GenerateSecret()
	require.NoError(t, err)
	err = Verify(hdr(specID, "1614265330", specSignature), []byte(specBody), time.Unix(specTimestamp, 0), 0, other)
	assert.ErrorIs(t, err, ErrBadSignature)
}

// The rotation overlap: a delivery under two keys verifies under EITHER, so a receiver can swap
// at its own pace; a receiver holding neither still rejects it.
func TestRotationOverlapVerifiesUnderEitherKey(t *testing.T) {
	oldKey, err := GenerateSecret()
	require.NoError(t, err)
	newKey, err := GenerateSecret()
	require.NoError(t, err)
	at := time.Unix(specTimestamp, 0)

	sig, err := Sign(specID, at, []byte(specBody), newKey, oldKey)
	require.NoError(t, err)
	entries := strings.Fields(sig)
	require.Len(t, entries, 2, "one entry per key")
	assert.NotEqual(t, entries[0], entries[1])
	h := hdr(specID, Timestamp(at), sig)

	assert.NoError(t, Verify(h, []byte(specBody), at, 0, newKey), "receiver already on the new key")
	assert.NoError(t, Verify(h, []byte(specBody), at, 0, oldKey), "receiver still on the old key")
	assert.NoError(t, Verify(h, []byte(specBody), at, 0, oldKey, newKey))

	third, err := GenerateSecret()
	require.NoError(t, err)
	assert.ErrorIs(t, Verify(h, []byte(specBody), at, 0, third), ErrBadSignature)
}

func TestVerifySkipsUnknownSignatureVersions(t *testing.T) {
	at := time.Unix(specTimestamp, 0)
	h := hdr(specID, Timestamp(at), "v2,AAAA "+specSignature+" garbage")
	assert.NoError(t, Verify(h, []byte(specBody), at, 0, specSecret))
	assert.ErrorIs(t, Verify(hdr(specID, Timestamp(at), "v2,AAAA"), []byte(specBody), at, 0, specSecret), ErrBadSignature)
}

func TestSecretValidation(t *testing.T) {
	for _, bad := range []string{
		"", "whsec_", "whsec_!!!notbase64", "whsec_c2hvcnQ=", // 5 bytes
		"whsec_" + strings.Repeat("QUFB", 30), // 90 bytes
	} {
		assert.ErrorIs(t, ValidateSecret(bad), ErrInvalidSecret, "%q", bad)
		_, err := Sign("id", time.Now(), nil, bad)
		assert.ErrorIs(t, err, ErrInvalidSecret, "%q", bad)
	}
	assert.NoError(t, ValidateSecret(specSecret))
	// The prefix is a label: the same bytes without it are the same key.
	a, err := Sign(specID, time.Unix(specTimestamp, 0), []byte(specBody), strings.TrimPrefix(specSecret, SecretPrefix))
	require.NoError(t, err)
	assert.Equal(t, specSignature, a)

	_, err = Sign("id", time.Now(), nil)
	assert.ErrorIs(t, err, ErrNoSecrets)
}

// A bad secret in a list fails the whole signing: a delivery must not go out under fewer keys
// than were asked for.
func TestSignRefusesPartialKeySets(t *testing.T) {
	_, err := Sign("id", time.Now(), nil, specSecret, "whsec_bad")
	assert.ErrorIs(t, err, ErrInvalidSecret)
}

func TestGenerateSecretIsValidAndUnique(t *testing.T) {
	a, err := GenerateSecret()
	require.NoError(t, err)
	b, err := GenerateSecret()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, SecretPrefix))
	assert.NoError(t, ValidateSecret(a))
}

// --- keyring ---

type memStore struct {
	mu sync.Mutex
	m  map[secrets.SecretRef][]byte
}

func newMemStore() *memStore { return &memStore{m: map[secrets.SecretRef][]byte{}} }

func (s *memStore) Put(_ context.Context, r secrets.SecretRef, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[r] = append([]byte(nil), v...)
	return nil
}
func (s *memStore) Resolve(_ context.Context, r secrets.SecretRef) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[r]; ok {
		return v, nil
	}
	return nil, secrets.ErrSecretNotFound
}
func (s *memStore) Rotate(ctx context.Context, r secrets.SecretRef, v []byte) error {
	return s.Put(ctx, r, v)
}
func (s *memStore) Delete(_ context.Context, r secrets.SecretRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, r)
	return nil
}
func (s *memStore) Exists(_ context.Context, r secrets.SecretRef) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[r]
	return ok, nil
}
func (s *memStore) CreateIfAbsent(_ context.Context, r secrets.SecretRef, v []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[r]; ok {
		return false, nil
	}
	s.m[r] = append([]byte(nil), v...)
	return true, nil
}

func (s *memStore) resolver(tenant string) Resolve {
	return func(ctx context.Context, name string) (string, error) {
		v, err := s.Resolve(ctx, secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: name})
		return string(v), err
	}
}

func TestKeyringLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	kr := Keyring{Store: st}
	res := st.resolver("t1")

	_, err := ResolveKeys(ctx, "h", res)
	assert.ErrorIs(t, err, ErrNoSigningSecret, "nothing stored is not 'unsigned'")
	_, err = kr.Rotate(ctx, "t1", "h")
	assert.ErrorIs(t, err, ErrNoSigningSecret)

	first, err := kr.Create(ctx, "t1", "h")
	require.NoError(t, err)
	_, err = kr.Create(ctx, "t1", "h")
	assert.ErrorIs(t, err, ErrSigningSecretExists, "create never replaces a key")
	keys, err := ResolveKeys(ctx, "h", res)
	require.NoError(t, err)
	assert.Equal(t, []string{first}, keys)

	second, err := kr.Rotate(ctx, "t1", "h")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	keys, err = ResolveKeys(ctx, "h", res)
	require.NoError(t, err)
	assert.Equal(t, []string{second, first}, keys, "new key first, old key still signs during the overlap")
	exists, rotating, err := kr.Status(ctx, "t1", "h")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.True(t, rotating)

	require.NoError(t, kr.EndRotation(ctx, "t1", "h"))
	keys, err = ResolveKeys(ctx, "h", res)
	require.NoError(t, err)
	assert.Equal(t, []string{second}, keys)
	_, rotating, _ = kr.Status(ctx, "t1", "h")
	assert.False(t, rotating)

	// A third key retires the second only after ANOTHER rotation: previous is always the key just
	// replaced, never an older one.
	third, err := kr.Rotate(ctx, "t1", "h")
	require.NoError(t, err)
	keys, _ = ResolveKeys(ctx, "h", res)
	assert.Equal(t, []string{third, second}, keys)

	require.NoError(t, kr.Delete(ctx, "t1", "h"))
	_, err = ResolveKeys(ctx, "h", res)
	assert.ErrorIs(t, err, ErrNoSigningSecret)
	exists, _, _ = kr.Status(ctx, "t1", "h")
	assert.False(t, exists)
}

func TestKeyringIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	st := newMemStore()
	_, err := Keyring{Store: st}.Create(ctx, "t1", "h")
	require.NoError(t, err)
	_, err = ResolveKeys(ctx, "h", st.resolver("t2"))
	assert.ErrorIs(t, err, ErrNoSigningSecret)
}

// Slot names are injective: a base that looks like another base's slot cannot alias it.
func TestSlotNamesDoNotCollide(t *testing.T) {
	seen := map[string]string{}
	for _, base := range []string{"x", "x/previous", "x/current", "x/previous/previous", "x/previous/current"} {
		cur, prev := KeyNames(base)
		for _, n := range []string{cur, prev} {
			other, dup := seen[n]
			assert.False(t, dup, "%q (base %q) collides with base %q", n, base, other)
			seen[n] = base
		}
	}
}

func TestResolveKeysSurfacesTransientAndCorruptSlots(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("store down")

	_, err := ResolveKeys(ctx, "h", func(context.Context, string) (string, error) { return "", boom })
	assert.ErrorIs(t, err, boom, "a store error is not 'no secret'")

	_, err = ResolveKeys(ctx, "h", func(context.Context, string) (string, error) { return "not-a-secret", nil })
	assert.ErrorIs(t, err, ErrInvalidSecret)

	_, err = ResolveKeys(ctx, "h", func(_ context.Context, name string) (string, error) {
		if strings.HasSuffix(name, slotCurrent) {
			return specSecret, nil
		}
		return "", boom
	})
	assert.ErrorIs(t, err, boom, "a failing previous slot must not silently drop to one signature")
}
