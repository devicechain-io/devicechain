// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// A device's ACCESS_TOKEN is stored as a KEYED DIGEST, never as the token. Unlike an MQTT
// password, the token is also the thing the device is LOOKED UP by (the credential id of
// the row), so its digest is DETERMINISTIC: HMAC-SHA-256 over the credential's tenant and
// the token, under a key derived from the instance root key. The same token always yields
// the same digest, which is what lets the credential lookup stay an indexed equality on
// the stored value, while a copy of the database alone yields nothing a device could
// present, or even guess against offline.
//
// There is no salt, and that is the cost of looking up by the digest. It is acceptable
// because an access token is a high-entropy value the platform or the operator generates
// (a UUID or longer), not a remembered one, and because the key is not in the database:
// without it a digest cannot be tested against a guess at any speed. Two credentials of
// one tenant with the same token have the same digest, which is the uniqueness the lookup
// index already enforces on the id.
//
// The stored form is
//
//	dctk1$<key id, 8 hex>$<mac, base64 raw url>
//
// The tenant is bound in so a digest copied into another tenant's row matches nothing
// there. The key id (the one DeviceSecretKey.KeyId returns) names the key that made the
// digest, so a digest made under another root key is told apart from a plain wrong token.

// accessTokenKeyInfo is the HKDF info that separates the access token key from the device
// secret key formed from the same root key.
const accessTokenKeyInfo = "devicechain/device-access-token-lookup/v1"

const (
	accessTokenVersion = "dctk1"
	// MaxAccessTokenBytes is the longest access token a credential may be SET to: the credential
	// id column's width. A longer one is refused where it is accepted, not truncated.
	MaxAccessTokenBytes = 256
	// MaxStoredAccessTokenBytes is the longest access token a credential may already HOLD, and
	// so the longest one that is digested and looked up. Before tokens were digested the column
	// bounded a token at 256 CHARACTERS, not bytes, so a token stored then can be up to four
	// bytes a character. Bounding the digest at the write limit instead would leave such a row
	// unconvertible: the startup conversion would fail and the service would not start.
	MaxStoredAccessTokenBytes = 4 * MaxAccessTokenBytes
)

// ErrAccessTokenUnrecognized is returned for a stored value that is not an access token
// digest this key made.
var ErrAccessTokenUnrecognized = errors.New("credential: stored access token is not a digest made by this key")

// AccessTokenDigest returns the stored form of tenant's access token. It is deterministic.
// An empty tenant or token, and a token over MaxStoredAccessTokenBytes, are refused.
func (k *DeviceSecretKey) AccessTokenDigest(tenant, token string) (string, error) {
	if tenant == "" {
		return "", errors.New("credential: an access token digest needs the credential's tenant")
	}
	if token == "" {
		return "", errors.New("credential: an empty access token has no digest")
	}
	if len(token) > MaxStoredAccessTokenBytes {
		return "", fmt.Errorf("credential: an access token is at most %d bytes, got %d", MaxStoredAccessTokenBytes, len(token))
	}
	return k.accessTokenDigest(tenant, token), nil
}

func (k *DeviceSecretKey) accessTokenDigest(tenant, token string) string {
	m := hmac.New(sha256.New, k.lookupKey[:])
	m.Write([]byte(tenant))
	m.Write([]byte{0})
	m.Write([]byte(token))
	return strings.Join([]string{accessTokenVersion, k.id,
		base64.RawURLEncoding.EncodeToString(m.Sum(nil))}, deviceSecretSep)
}

// IsAccessTokenDigest reports whether stored has the shape of an access token digest of any
// key (the version marker alone), so a startup step can tell a stored digest from a token
// still held as sent. It says nothing about which key made it: RecognizesAccessToken does.
func IsAccessTokenDigest(stored string) bool {
	return strings.HasPrefix(stored, accessTokenVersion+deviceSecretSep)
}

// AccessTokenDigestPrefix is the leading run of every digest this key makes, for a store to
// count the digests another key made.
func (k *DeviceSecretKey) AccessTokenDigestPrefix() string {
	return accessTokenVersion + deviceSecretSep + k.id + deviceSecretSep
}

// RecognizesAccessToken is nil when stored is a well-formed access token digest made by
// this key, and ErrAccessTokenUnrecognized otherwise.
func (k *DeviceSecretKey) RecognizesAccessToken(stored string) error {
	parts := strings.Split(stored, deviceSecretSep)
	if len(parts) != 3 || parts[0] != accessTokenVersion || parts[1] != k.id {
		return ErrAccessTokenUnrecognized
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(mac) != sha256.Size {
		return ErrAccessTokenUnrecognized
	}
	return nil
}

// VerifyAccessToken compares a presented access token against the stored digest of
// tenant's credential in constant time: nil on a match, ErrMismatch otherwise, including a
// nil key, an empty tenant or token, and a stored value that is not a digest of this key.
//
// The credential lookup already matched the row by this same digest, so this is the
// confirmation that the row it returned is the presented token's, made without a
// data-dependent branch. Both inputs are fixed-width digest strings of the same shape.
//
// It is unthrottled, like VerifyDeviceSecret, and hack/check-credential-compare.sh
// refuses any production caller without a stated exemption.
func VerifyAccessToken(k *DeviceSecretKey, tenant, stored, token string) error {
	if k == nil || tenant == "" || token == "" || len(token) > MaxStoredAccessTokenBytes {
		return ErrMismatch
	}
	want := []byte(stored)
	got := []byte(k.accessTokenDigest(tenant, token))
	if len(want) != len(got) {
		// Different length: not this key's digest (or malformed). Still pay one compare.
		constantTimeCompare(got, got)
		return ErrMismatch
	}
	if constantTimeCompare(got, want) != 1 || k.RecognizesAccessToken(stored) != nil {
		return ErrMismatch
	}
	return nil
}
