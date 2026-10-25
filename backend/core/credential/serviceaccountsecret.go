// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

// A service account's client secret is stored as a KEYED DIGEST, by the construction the
// device secrets use (devicesecret.go): HMAC-SHA-256 over the account's tenant, a per-row
// salt and the secret, under a key derived from the instance root key. A copy of the
// database alone yields nothing that can be presented or guessed against.
//
// 🔴 IT IS A SEPARATE KEY. The HKDF info string differs from the device secrets', so a
// device password's digest and a service account secret's are made under unrelated keys:
// a digest copied from one table into the other verifies nothing.
//
// Why a fast keyed hash rather than bcrypt: the secret is minted by the server from 256
// random bits, so a slow hash buys no guessing resistance, and each bcrypt compare on the
// unauthenticated token endpoint would be CPU an anonymous caller could spend at will.

// serviceAccountSecretKeyInfo is the HKDF info that separates this key from every other key
// formed from the same root key.
const serviceAccountSecretKeyInfo = "devicechain/service-account-secret/v1"

// serviceAccountSecretKeyIdLabel is what the key id is the MAC of.
const serviceAccountSecretKeyIdLabel = "devicechain/service-account-secret/key-id"

// DeriveServiceAccountSecretKey derives the service-account secret key from the instance
// root key (HKDF-SHA-256). The root key must be the 32 bytes config.SecretsConfiguration
// DecodedRootKey returns. The key digests and verifies exactly as a DeviceSecretKey does;
// it is a different key.
func DeriveServiceAccountSecretKey(rootKey []byte) (*DeviceSecretKey, error) {
	return deriveKeyedSecretKey(rootKey, serviceAccountSecretKeyInfo, serviceAccountSecretKeyIdLabel,
		"service account secret")
}
