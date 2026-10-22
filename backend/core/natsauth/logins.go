// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/config"
)

// Login is one principal's broker credential: the username it presents, the plaintext that goes to
// that principal's own Secret, and the bcrypt hash that goes to the broker.
type Login struct {
	User           string
	Password       string
	PasswordBcrypt string
}

// AdminUser is the broker-administration login's username. Nothing mounts its Secret yet: it is
// minted now so that the step that gives stream administration to one credential finds it already
// recorded and escrowed on every instance, instead of being a second introduction.
var AdminUser = config.NatsLoginUser(config.NatsAdminPrincipal, config.NatsLoginGeneration)

// LoginUser is the username of principal's login at the generation minted today.
func LoginUser(principal string) string {
	return config.NatsLoginUser(principal, config.NatsLoginGeneration)
}

// WithLogins completes a credential set with a login for every principal in
// config.NatsLoginPrincipals. A principal with a password in existing is REUSED (its hash is the
// deployed one if that still verifies, else a fresh hash of the same password); a principal with
// none is MINTED.
//
// 🔑 INTRODUCED, NOT EVOLVED, the same class as the callout's curve key (WithXKey) and the identity
// login (WithIdentity). A per-area login is absent from the starting state of any instance that
// predates it, and nothing authenticates with it until the broker is told, so minting it replaces
// nothing. That is the only reason an upgrade may mint it. A password that is present is NEVER
// replaced, per principal: an instance that has some logins (a run that died partway, an area
// enabled later) keeps those and gets the rest.
//
// existing is keyed by principal, deployedHashes by username.
func (c Credentials) WithLogins(existing map[string]string, deployedHashes map[string]string) (Credentials, error) {
	out := make(map[string]Login, len(config.NatsLoginPrincipals()))
	for _, principal := range config.NatsLoginPrincipals() {
		user := LoginUser(principal)
		if pw := existing[principal]; pw != "" {
			hash, err := reuseOrRehash(pw, deployedHashes[user])
			if err != nil {
				return Credentials{}, fmt.Errorf("hashing the running instance's broker login for %s: %w", principal, err)
			}
			out[principal] = Login{User: user, Password: pw, PasswordBcrypt: hash}
			continue
		}
		pw, hash, err := generateSysPassword()
		if err != nil {
			return Credentials{}, fmt.Errorf("minting the broker login for %s: %w", principal, err)
		}
		out[principal] = Login{User: user, Password: pw, PasswordBcrypt: hash}
	}
	c.Logins = out
	return c, nil
}
