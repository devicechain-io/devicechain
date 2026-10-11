// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// A device that presents an MQTT password on an event body hands event-sources a secret
// that device-management must see again, one hop later, across a durable stream. The
// stream keeps its messages for its retention period, so the password must not cross it
// as sent. SecretTransit SEALS it: event-sources seals the presented secret under a key
// derived from the instance root key, the stream carries the sealed form, and
// device-management opens it to compare against the stored digest.
//
// 🔴 WHY SEALED AND NOT A DIGEST. The stored digest is salted per credential, so a digest
// made at the edge could only match it if the edge knew that salt, which needs the
// database. A sealed value needs no database and no stored state, and what is opened is
// compared by the same constant-time path as before.
//
// The key is derived from the root key under its own HKDF info, separate from the key the
// stored digests use, so a sealed value is never a valid stored digest and the two keys
// can be rotated independently. The tenant and credential id are authenticated data: a
// sealed secret cannot be moved onto another credential's event.
//
// The sealed form is
//
//	dcs2$<key id, 8 hex>$<nonce ‖ ciphertext ‖ tag, base64 raw std>
//
// Anything that is not that form, including a plaintext secret left in a message from
// before this existed, is refused by Open, so a stranded old message fails closed.

// THE CIPHER. Every event carrying a bearer token is sealed under one static key with a
// random nonce, so the number of seals is the event count. XChaCha20-Poly1305 takes a
// 192-bit random nonce, for which random nonces are safe at any volume; a 96-bit random
// nonce under one key is not (it passes the 2^32-seals bound within days at ingest rates).
//
// Passwords and tokens are sealed under SEPARATE subkeys (separate HKDF info), so a value
// sealed for one purpose is never valid for the other even before the bound data is checked.
const secretTransitInfo = "devicechain/device-credential-transit/v2/secret"
const tokenTransitInfo = "devicechain/device-credential-transit/v2/token"
const secretTransitKeyIdLabel = "devicechain/device-credential-transit/v2/key-id"

const (
	sealedSecretPrefix = "dcs2"
	sealedSecretSep    = "$"
)

// ErrSealedSecretInvalid is Open's refusal: the value is not a sealed secret this key made
// for this tenant and credential id. A plaintext secret is one.
var ErrSealedSecretInvalid = errors.New("credential: not a sealed device secret for this credential")

// SecretTransit seals and opens a presented device secret for the hop between services.
type SecretTransit struct {
	aead      cipher.AEAD // seals presented passwords
	tokenAEAD cipher.AEAD // seals presented bearer tokens
	id        string
}

// DeriveSecretTransit derives the transit key from the instance root key (32 bytes, as
// config.SecretsConfiguration.DecodedRootKey returns).
func DeriveSecretTransit(rootKey []byte) (*SecretTransit, error) {
	if len(rootKey) != 32 {
		return nil, fmt.Errorf("credential: the secret transit key needs a 32-byte root key, got %d bytes", len(rootKey))
	}
	raw, err := hkdf.Key(sha256.New, rootKey, nil, secretTransitInfo, sha256.Size)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(raw)
	if err != nil {
		return nil, err
	}
	tokenRaw, err := hkdf.Key(sha256.New, rootKey, nil, tokenTransitInfo, sha256.Size)
	if err != nil {
		return nil, err
	}
	tokenAEAD, err := chacha20poly1305.NewX(tokenRaw)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, raw)
	m.Write([]byte(secretTransitKeyIdLabel))
	return &SecretTransit{aead: aead, tokenAEAD: tokenAEAD, id: hex.EncodeToString(m.Sum(nil)[:4])}, nil
}

func transitAAD(tenant, credentialId string) []byte {
	return []byte(tenant + "\x00" + credentialId)
}

func tokenAAD(tenant, credentialType string) []byte {
	return []byte("token\x01" + tenant + "\x00" + credentialType)
}

// Seal returns the sealed form of secret for tenant's credential credentialId. An empty
// secret, tenant or credential id is refused.
func (t *SecretTransit) Seal(tenant, credentialId, secret string) (string, error) {
	if tenant == "" || credentialId == "" || secret == "" {
		return "", errors.New("credential: sealing a device secret needs a tenant, a credential id and a secret")
	}
	return t.seal(t.aead, secret, transitAAD(tenant, credentialId))
}

// SealToken returns the sealed form of a bearer token (an ACCESS_TOKEN credential id)
// for tenant's credentials of credentialType. A token cannot be its own authenticated
// data, so it is bound to the tenant and the credential type instead; the sealed form is
// otherwise the one Seal makes, and OpenToken is its only reader.
func (t *SecretTransit) SealToken(tenant, credentialType, token string) (string, error) {
	if tenant == "" || credentialType == "" || token == "" {
		return "", errors.New("credential: sealing a bearer token needs a tenant, a credential type and a token")
	}
	return t.seal(t.tokenAEAD, token, tokenAAD(tenant, credentialType))
}

func (t *SecretTransit) seal(aead cipher.AEAD, plain string, aad []byte) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), aad)
	return strings.Join([]string{sealedSecretPrefix, t.id, base64.RawStdEncoding.EncodeToString(sealed)}, sealedSecretSep), nil
}

// Open returns the secret a sealed value carries, or ErrSealedSecretInvalid. The check is
// the AEAD's authentication tag, not a compare of the secret.
func (t *SecretTransit) Open(tenant, credentialId, sealed string) (string, error) {
	return t.open(func() cipher.AEAD { return t.aead }, sealed, transitAAD(tenant, credentialId))
}

// OpenToken returns the bearer token a SealToken value carries for this tenant and
// credential type, or ErrSealedSecretInvalid. A plaintext token is one.
func (t *SecretTransit) OpenToken(tenant, credentialType, sealed string) (string, error) {
	return t.open(func() cipher.AEAD { return t.tokenAEAD }, sealed, tokenAAD(tenant, credentialType))
}

// open takes the cipher through a func so a nil SecretTransit is refused before any field is read.
func (t *SecretTransit) open(pick func() cipher.AEAD, sealed string, aad []byte) (string, error) {
	parts := strings.Split(sealed, sealedSecretSep)
	if t == nil || len(parts) != 3 || parts[0] != sealedSecretPrefix || parts[1] != t.id {
		return "", ErrSealedSecretInvalid
	}
	aead := pick()
	raw, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", ErrSealedSecretInvalid
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return "", ErrSealedSecretInvalid
	}
	return string(plain), nil
}
