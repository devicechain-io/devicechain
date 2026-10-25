// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

import "fmt"

// The secrets a device is ENROLLED with are stored as KEYED DIGESTS, by the construction the
// device secrets use (devicesecret.go): HMAC-SHA-256 over the owning row's tenant, a per-row
// salt and the secret, under a key derived from the instance root key. They are the shared
// secret of a provisioning profile and the one-time secret of a device claim. A copy of the
// database alone yields nothing that can be presented, or even guessed against offline.
//
// 🔴 EACH PURPOSE HAS A KEY OF ITS OWN. The HKDF info strings differ from each other and from
// the device secrets' and the service account secrets', so a digest copied from a credential
// row into a claim, or from a profile into a claim, in the same tenant verifies nothing.
//
// Both secrets are found by another identifier (a provision key, a device) and compared
// afterwards, so unlike an access token the digest need not be deterministic: it carries a
// random salt, and two rows with the same secret store different digests.

const (
	provisioningSecretKeyInfo    = "devicechain/provisioning-secret/v1"
	provisioningSecretKeyIdLabel = "devicechain/provisioning-secret/key-id"
	claimSecretKeyInfo           = "devicechain/device-claim-secret/v1"
	claimSecretKeyIdLabel        = "devicechain/device-claim-secret/key-id"
)

// MaxEnrollmentSecretBytes is the longest enrollment secret a row may be SET to. The plaintext
// columns were varchar(256), which refused anything longer in characters; a digest column is
// fixed-width and would not, so the limit is stated here and enforced where a secret is
// accepted. It counts bytes of the secret as sent. A secret already stored may be up to four
// bytes a character, and is still digested (Digest allows MaxDeviceSecretBytes).
const MaxEnrollmentSecretBytes = 256

// EnrollmentSecretKeys are the keys enrollment secrets are digested and verified under, one
// per kind of secret.
type EnrollmentSecretKeys struct {
	// Provisioning is the key of a provisioning profile's shared secret.
	Provisioning *DeviceSecretKey
	// Claim is the key of a device claim's one-time secret.
	Claim *DeviceSecretKey
}

// DeriveEnrollmentSecretKeys derives the enrollment secret keys from the instance root key
// (HKDF-SHA-256). The root key must be the 32 bytes config.SecretsConfiguration
// DecodedRootKey returns. Each key digests and verifies exactly as a DeviceSecretKey does.
func DeriveEnrollmentSecretKeys(rootKey []byte) (*EnrollmentSecretKeys, error) {
	p, err := deriveKeyedSecretKey(rootKey, provisioningSecretKeyInfo, provisioningSecretKeyIdLabel,
		"provisioning secret")
	if err != nil {
		return nil, fmt.Errorf("provisioning secret key: %w", err)
	}
	c, err := deriveKeyedSecretKey(rootKey, claimSecretKeyInfo, claimSecretKeyIdLabel, "claim secret")
	if err != nil {
		return nil, fmt.Errorf("claim secret key: %w", err)
	}
	return &EnrollmentSecretKeys{Provisioning: p, Claim: c}, nil
}
