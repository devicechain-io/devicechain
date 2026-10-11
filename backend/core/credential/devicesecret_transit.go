// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import (
	"crypto/aes"
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
//	dcs1$<key id, 8 hex>$<nonce ‖ ciphertext ‖ tag, base64 raw std>
//
// Anything that is not that form, including a plaintext secret left in a message from
// before this existed, is refused by Open, so a stranded old message fails closed.

const secretTransitInfo = "devicechain/device-credential-transit/v1"
const secretTransitKeyIdLabel = "devicechain/device-credential-transit/key-id"

const (
	sealedSecretPrefix = "dcs1"
	sealedSecretSep    = "$"
)

// ErrSealedSecretInvalid is Open's refusal: the value is not a sealed secret this key made
// for this tenant and credential id. A plaintext secret is one.
var ErrSealedSecretInvalid = errors.New("credential: not a sealed device secret for this credential")

// SecretTransit seals and opens a presented device secret for the hop between services.
type SecretTransit struct {
	aead cipher.AEAD
	id   string
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
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, raw)
	m.Write([]byte(secretTransitKeyIdLabel))
	return &SecretTransit{aead: aead, id: hex.EncodeToString(m.Sum(nil)[:4])}, nil
}

func transitAAD(tenant, credentialId string) []byte {
	return []byte(tenant + "\x00" + credentialId)
}

// Seal returns the sealed form of secret for tenant's credential credentialId. An empty
// secret, tenant or credential id is refused.
func (t *SecretTransit) Seal(tenant, credentialId, secret string) (string, error) {
	if tenant == "" || credentialId == "" || secret == "" {
		return "", errors.New("credential: sealing a device secret needs a tenant, a credential id and a secret")
	}
	nonce := make([]byte, t.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := t.aead.Seal(nonce, nonce, []byte(secret), transitAAD(tenant, credentialId))
	return strings.Join([]string{sealedSecretPrefix, t.id, base64.RawStdEncoding.EncodeToString(sealed)}, sealedSecretSep), nil
}

// Open returns the secret a sealed value carries, or ErrSealedSecretInvalid. The check is
// the AEAD's authentication tag, not a compare of the secret.
func (t *SecretTransit) Open(tenant, credentialId, sealed string) (string, error) {
	parts := strings.Split(sealed, sealedSecretSep)
	if t == nil || len(parts) != 3 || parts[0] != sealedSecretPrefix || parts[1] != t.id {
		return "", ErrSealedSecretInvalid
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < t.aead.NonceSize()+t.aead.Overhead() {
		return "", ErrSealedSecretInvalid
	}
	nonce, ct := raw[:t.aead.NonceSize()], raw[t.aead.NonceSize():]
	plain, err := t.aead.Open(nil, nonce, ct, transitAAD(tenant, credentialId))
	if err != nil {
		return "", ErrSealedSecretInvalid
	}
	return string(plain), nil
}
