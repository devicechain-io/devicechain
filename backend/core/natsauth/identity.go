// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import "fmt"

// WithIdentity completes a credential set with the login for IdentityAccount. A non-empty
// existingPassword is REUSED (its hash is the deployed one if that still verifies, else a fresh
// hash of the same password); an empty one is MINTED.
//
// 🔑 INTRODUCED, NOT EVOLVED, the same class as the callout's curve key (WithXKey). The login is
// absent from the starting state of any instance that predates it and nothing authenticates with
// it until the broker is told, so minting it replaces nothing. That is the only reason an upgrade
// may mint it; every other broker credential is read back or the run is refused. A present
// password is never replaced.
func (c Credentials) WithIdentity(existingPassword, deployedHash string) (Credentials, error) {
	if existingPassword == "" {
		pw, hash, err := generateSysPassword()
		if err != nil {
			return Credentials{}, fmt.Errorf("minting the identity-account login: %w", err)
		}
		c.IdentityPassword, c.IdentityPasswordBcrypt = pw, hash
		return c, nil
	}
	hash, err := reuseOrRehash(existingPassword, deployedHash)
	if err != nil {
		return Credentials{}, fmt.Errorf("hashing the running instance's identity-account password: %w", err)
	}
	c.IdentityPassword, c.IdentityPasswordBcrypt = existingPassword, hash
	return c, nil
}
