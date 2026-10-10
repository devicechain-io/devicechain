// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// A device's MQTT password (an MQTT_BASIC credential) is stored as a KEYED DIGEST, never
// as the password. The digest is HMAC-SHA-256 over the credential's TENANT, a per-row
// random salt and the password, under a key derived from the instance root key, which is not in the
// database: a copy of the database alone gives nothing that can be presented, or even
// guessed against offline.
//
// 🔴 WHY A FAST KEYED HASH AND NOT bcrypt OR argon2. The password is compared on two hot
// paths: once per MQTT connect (the auth callout, up to 256 at a time, each with a 3 s
// deadline) and once per EVENT that carries a credential, cache hits included (the
// credential cache keeps the stored row, never a verdict). A ~60 ms slow hash makes a
// reconnect storm miss the callout's deadline and makes the per-event path impossible at
// the platform's event rates. The key is what makes a fast hash safe: without it a stolen
// digest cannot be guessed at any speed.
//
// The stored form is
//
//	v1$<key id, 8 hex>$<salt, base64 raw std>$<mac, base64 raw std>
//
// The tenant is bound in so that a digest copied from one tenant's row into another's
// verifies nothing there: a known password's digest cannot be transplanted across
// tenants. A credential never changes tenant. Its id is NOT bound, because an update may
// change the id without carrying the password to re-digest.
//
// The key id names the key that made the digest, so a digest made under another key (a
// database restored next to the wrong root key) is reported as misconfigured rather than
// as a wrong password. The version leaves room for a different key source: a connect
// presents the password, so a later version can be written on the next successful one.

// deviceSecretKeyInfo is the HKDF info that separates this key from every other key formed
// from the same root key (the secret store's KEK uses the root key directly).
const deviceSecretKeyInfo = "devicechain/device-credential-secret/v1"

// deviceSecretKeyIdLabel is what the key id is the MAC of.
const deviceSecretKeyIdLabel = "devicechain/device-credential-secret/key-id"

const (
	deviceSecretVersion  = "v1"
	deviceSecretSaltSize = 16
	deviceSecretSep      = "$"
)

// MaxDeviceSecretBytes is the longest secret a device credential may carry. The plaintext
// column was a varchar(4096), which refused anything longer; a digest column of fixed width
// would not, so the limit is stated here and enforced where a secret is accepted.
const MaxDeviceSecretBytes = 4096

// ErrNoDeviceSecretKey is NewChecker's refusal to declare KindDeviceCredential without a
// key (WithDeviceSecretKey): stored device secrets are digests under that key, so there
// would be nothing to compare them with.
var ErrNoDeviceSecretKey = errors.New("credential: KindDeviceCredential needs a device secret key (WithDeviceSecretKey)")

// ErrDeviceSecretUnrecognized is Recognizes' refusal: the stored value is not a digest
// this key made — malformed, another version, or made under another key.
var ErrDeviceSecretUnrecognized = errors.New("credential: stored device secret is not a digest made by this key")

// DeviceSecretKey digests and verifies device secrets. Build it with DeriveDeviceSecretKey.
type DeviceSecretKey struct {
	key [sha256.Size]byte
	id  string
}

// DeriveDeviceSecretKey derives the device secret key from the instance root key
// (HKDF-SHA-256). The root key must be the 32 bytes config.SecretsConfiguration
// DecodedRootKey returns.
func DeriveDeviceSecretKey(rootKey []byte) (*DeviceSecretKey, error) {
	if len(rootKey) != 32 {
		return nil, fmt.Errorf("credential: the device secret key needs a 32-byte root key, got %d bytes", len(rootKey))
	}
	raw, err := hkdf.Key(sha256.New, rootKey, nil, deviceSecretKeyInfo, sha256.Size)
	if err != nil {
		return nil, err
	}
	k := &DeviceSecretKey{}
	copy(k.key[:], raw)
	m := hmac.New(sha256.New, k.key[:])
	m.Write([]byte(deviceSecretKeyIdLabel))
	k.id = hex.EncodeToString(m.Sum(nil)[:4])
	return k, nil
}

// KeyId is the key's id, as it appears in every digest it makes. It is not secret.
func (k *DeviceSecretKey) KeyId() string { return k.id }

// Digest returns the stored form of tenant's secret, under a fresh random salt. An empty
// secret and one over MaxDeviceSecretBytes are refused: "no secret" is stored as NULL,
// never as a digest of nothing. An empty tenant is refused: every credential has one.
func (k *DeviceSecretKey) Digest(tenant, secret string) (string, error) {
	if tenant == "" {
		return "", errors.New("credential: a device secret digest needs the credential's tenant")
	}
	if secret == "" {
		return "", errors.New("credential: an empty device secret has no digest")
	}
	if len(secret) > MaxDeviceSecretBytes {
		return "", fmt.Errorf("credential: a device secret is at most %d bytes, got %d", MaxDeviceSecretBytes, len(secret))
	}
	salt := make([]byte, deviceSecretSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return k.digestWithSalt(tenant, salt, secret), nil
}

func (k *DeviceSecretKey) digestWithSalt(tenant string, salt []byte, secret string) string {
	return strings.Join([]string{deviceSecretVersion, k.id,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(k.mac(tenant, salt, []byte(secret)))}, deviceSecretSep)
}

// mac is HMAC(key, tenant ‖ 0x00 ‖ salt ‖ secret). The token grammar excludes NUL from a
// tenant id, and the salt is fixed-width, so the input splits one way only.
func (k *DeviceSecretKey) mac(tenant string, salt, secret []byte) []byte {
	m := hmac.New(sha256.New, k.key[:])
	m.Write([]byte(tenant))
	m.Write([]byte{0})
	m.Write(salt)
	m.Write(secret)
	return m.Sum(nil)
}

// parse splits a stored digest made by this key into its salt and mac.
func (k *DeviceSecretKey) parse(stored string) (salt, mac []byte, err error) {
	parts := strings.Split(stored, deviceSecretSep)
	if len(parts) != 4 || parts[0] != deviceSecretVersion || parts[1] != k.id {
		return nil, nil, ErrDeviceSecretUnrecognized
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != deviceSecretSaltSize {
		return nil, nil, ErrDeviceSecretUnrecognized
	}
	mac, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(mac) != sha256.Size {
		return nil, nil, ErrDeviceSecretUnrecognized
	}
	return salt, mac, nil
}

// Recognizes reports whether stored is a well-formed digest made by this key, and
// ErrDeviceSecretUnrecognized when it is not. A caller asks it BEFORE comparing, so a
// stored value that no password can ever match — a digest under another root key, or a
// corrupt one — is reported as the operator's problem instead of as a wrong password.
func (k *DeviceSecretKey) Recognizes(stored string) error {
	_, _, err := k.parse(stored)
	return err
}

// VerifyDeviceSecret compares a presented secret against a stored digest of tenant's
// credential in constant time: nil on a match, ErrMismatch otherwise, including for a
// stored value Recognizes refuses, a nil key, an empty tenant, and a digest made for
// another tenant. The two inputs to the compare are always 32-byte MACs, so neither the stored
// nor the presented secret's length shows in its timing.
//
// 🔴 IT IS UNTHROTTLED. A caller that answers the sender must compare through Checker
// (KindDeviceCredential), which runs this same compare behind the per-username backoff.
// It is exported for the per-event resolver alone, whose verdict never reaches the
// sender, and hack/check-credential-compare.sh refuses any other production caller
// without a stated exemption.
func VerifyDeviceSecret(k *DeviceSecretKey, tenant, stored, secret string) error {
	if k == nil {
		return ErrMismatch
	}
	return k.verify(tenant, stored, secret)
}

func (k *DeviceSecretKey) verify(tenant, stored, secret string) error {
	salt, want, err := k.parse(stored)
	if err != nil {
		// Still pay a MAC and a compare, so a malformed stored value costs what a real one
		// does. The answer is a mismatch either way.
		salt, want = make([]byte, deviceSecretSaltSize), make([]byte, sha256.Size)
		got := k.mac(tenant, salt, []byte(secret))
		constantTimeCompare(got, want)
		return ErrMismatch
	}
	got := k.mac(tenant, salt, []byte(secret))
	if constantTimeCompare(got, want) != 1 || tenant == "" {
		return ErrMismatch
	}
	return nil
}
