// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

// The secrets a device is ENROLLED with are stored as KEYED DIGESTS, by the construction the
// device secrets use (devicesecret.go): HMAC-SHA-256 over the owning row's tenant, a per-row
// salt and the secret, under a key derived from the instance root key. They are the shared
// secret of a provisioning profile and the one-time secret of a device claim. A copy of the
// database alone yields nothing that can be presented, or even guessed against offline.
//
// 🔴 IT IS A SEPARATE KEY. The HKDF info string differs from the device secrets' and the
// service account secrets', so a digest copied from a credential or a service account row
// into an enrollment row verifies nothing, and the reverse.
//
// Both secrets are found by another identifier (a provision key, a device) and compared
// afterwards, so unlike an access token the digest need not be deterministic: it carries a
// random salt, and two rows with the same secret store different digests.

// enrollmentSecretKeyInfo is the HKDF info that separates this key from every other key
// formed from the same root key.
const enrollmentSecretKeyInfo = "devicechain/enrollment-secret/v1"

// enrollmentSecretKeyIdLabel is what the key id is the MAC of.
const enrollmentSecretKeyIdLabel = "devicechain/enrollment-secret/key-id"

// MaxEnrollmentSecretBytes is the longest enrollment secret a row may carry. The plaintext
// columns were varchar(256), which refused anything longer in characters; a digest column is
// fixed-width and would not, so the limit is stated here and enforced where a secret is
// accepted. It counts bytes of the secret as sent.
const MaxEnrollmentSecretBytes = 256

// DeriveEnrollmentSecretKey derives the enrollment secret key from the instance root key
// (HKDF-SHA-256). The root key must be the 32 bytes config.SecretsConfiguration
// DecodedRootKey returns. The key digests and verifies exactly as a DeviceSecretKey does; it
// is a different key.
func DeriveEnrollmentSecretKey(rootKey []byte) (*DeviceSecretKey, error) {
	return deriveKeyedSecretKey(rootKey, enrollmentSecretKeyInfo, enrollmentSecretKeyIdLabel,
		"enrollment secret")
}
