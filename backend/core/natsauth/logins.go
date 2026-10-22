// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"
	"runtime"
	"sync"

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
	principals := config.NatsLoginPrincipals()
	logins := make([]Login, len(principals))
	errs := make([]error, len(principals))

	// Each login is a production-cost bcrypt hash, independent of every other, so they are computed
	// concurrently (bounded by the CPUs available). The cost is unchanged; only the wall clock moves.
	var wg sync.WaitGroup
	slots := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, principal := range principals {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			user := LoginUser(principal)
			if pw := existing[principal]; pw != "" {
				hash, err := reuseOrRehash(pw, deployedHashes[user])
				if err != nil {
					errs[i] = fmt.Errorf("hashing the running instance's broker login for %s: %w", principal, err)
					return
				}
				logins[i] = Login{User: user, Password: pw, PasswordBcrypt: hash}
				return
			}
			pw, hash, err := generateSysPassword()
			if err != nil {
				errs[i] = fmt.Errorf("minting the broker login for %s: %w", principal, err)
				return
			}
			logins[i] = Login{User: user, Password: pw, PasswordBcrypt: hash}
		}()
	}
	wg.Wait()

	out := make(map[string]Login, len(principals))
	for i, principal := range principals {
		if errs[i] != nil {
			return Credentials{}, errs[i]
		}
		out[principal] = logins[i]
	}
	c.Logins = out
	return c, nil
}
