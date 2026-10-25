// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"maps"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/natsauth"
)

// 🔴 WHY THE BROKER CREDENTIALS ARE MEMOIZED PER TEST BINARY.
//
// Every broker login is a production-cost bcrypt hash, and a bootstrap or upgrade that reaches the
// broker settles roughly fifteen of them (one per area plus the administrator), as well as the shared
// set. Dozens of tests drive those paths and none is ABOUT the hashing; under the race detector
// bcrypt is an order of magnitude slower, which is what pushed this package's CI step past its
// timeout. So the two functions the production code mints through (generateCredentials, withLogins)
// are replaced here by wrappers that run the REAL function once per distinct input and hand every
// later caller a copy of that result.
//
// What this does not do: lower the cost, weaken a hash, or stand in a fake. The first caller pays the
// real price and the hashes every test sees are real ones, verifiable against their plaintexts. What
// it does give up is that two tests no longer see two DIFFERENT random mints; a test that needs that
// (or needs a mint from nothing) calls natsauth directly, as natsloginsecret_test.go's reuse and
// never-replace tests do.
//
// The cache is keyed by the INPUTS (the existing passwords and deployed hashes), so a test that
// passes its own passwords still gets hashes of exactly those passwords.
type memo[T any] struct {
	mu      sync.Mutex
	entries map[string]*memoEntry[T]
}

type memoEntry[T any] struct {
	once sync.Once
	val  T
	err  error
}

func (m *memo[T]) get(key string, compute func() (T, error)) (T, error) {
	m.mu.Lock()
	if m.entries == nil {
		m.entries = map[string]*memoEntry[T]{}
	}
	e, ok := m.entries[key]
	if !ok {
		e = &memoEntry[T]{}
		m.entries[key] = e
	}
	m.mu.Unlock()
	e.once.Do(func() { e.val, e.err = compute() })
	return e.val, e.err
}

func copyCredentials(c natsauth.Credentials) natsauth.Credentials {
	if c.Logins != nil {
		c.Logins = maps.Clone(c.Logins)
	}
	return c
}

// realBrokerMinting holds the un-memoized functions, for the tests that are ABOUT minting.
var realBrokerMinting = struct {
	generate     func() (natsauth.Credentials, error)
	fromDeployed func(string, string, string, natsauth.DeployedHashes) (natsauth.Credentials, error)
	identity     func(natsauth.Credentials, string, string) (natsauth.Credentials, error)
	logins       func(natsauth.Credentials, map[string]string, map[string]string) (natsauth.Credentials, error)
}{generateCredentials, credentialsFromDeployed, withIdentity, withLogins}

// useRealBrokerMinting restores the real, un-memoized minting functions for the length of one test.
// Call it from a test whose assertion is that a fresh mint is FRESH (two installs differ); every
// other test is better served by the memoized ones.
func useRealBrokerMinting(t *testing.T) {
	t.Helper()
	g, f, i, l := generateCredentials, credentialsFromDeployed, withIdentity, withLogins
	generateCredentials = realBrokerMinting.generate
	credentialsFromDeployed = realBrokerMinting.fromDeployed
	withIdentity = realBrokerMinting.identity
	withLogins = realBrokerMinting.logins
	t.Cleanup(func() { generateCredentials, credentialsFromDeployed, withIdentity, withLogins = g, f, i, l })
}

// useRealCredentialGeneration restores only the shared-set mint, for a test whose assertion is that
// two fresh mints DIFFER but which says nothing about the per-area logins or the identity login. A
// mint of those is fifteen more production-cost hashes a run, and leaving them memoized keeps
// that test from paying for what it does not assert.
func useRealCredentialGeneration(t *testing.T) {
	t.Helper()
	g := generateCredentials
	generateCredentials = realBrokerMinting.generate
	t.Cleanup(func() { generateCredentials = g })
}

func installMemoizedBrokerCredentials() (prewarm func()) {
	var fromDeployed memo[natsauth.Credentials]
	realFromDeployed := credentialsFromDeployed
	credentialsFromDeployed = func(seed, password, sysPassword string, hashes natsauth.DeployedHashes) (natsauth.Credentials, error) {
		key, err := json.Marshal([]any{seed, password, sysPassword, hashes})
		if err != nil {
			return natsauth.Credentials{}, err
		}
		c, err := fromDeployed.get(string(key), func() (natsauth.Credentials, error) {
			return realFromDeployed(seed, password, sysPassword, hashes)
		})
		return copyCredentials(c), err
	}

	// ...and the rebuild for an instance with no shared login, which hashes the system login when the
	// broker's hash is not supplied. Keyed the same way, for the same reason.
	var withoutShared memo[natsauth.Credentials]
	realWithoutShared := credentialsFromDeployedWithoutSharedLogin
	credentialsFromDeployedWithoutSharedLogin = func(seed, sysPassword string, hashes natsauth.DeployedHashes) (natsauth.Credentials, error) {
		key, err := json.Marshal([]any{seed, sysPassword, hashes})
		if err != nil {
			return natsauth.Credentials{}, err
		}
		c, err := withoutShared.get(string(key), func() (natsauth.Credentials, error) {
			return realWithoutShared(seed, sysPassword, hashes)
		})
		return copyCredentials(c), err
	}

	var identity memo[[2]string]
	realIdentity := withIdentity
	withIdentity = func(c natsauth.Credentials, existingPassword, deployedHash string) (natsauth.Credentials, error) {
		pair, err := identity.get(existingPassword+"|"+deployedHash, func() ([2]string, error) {
			done, err := realIdentity(natsauth.Credentials{}, existingPassword, deployedHash)
			return [2]string{done.IdentityPassword, done.IdentityPasswordBcrypt}, err
		})
		if err != nil {
			return natsauth.Credentials{}, err
		}
		c.IdentityPassword, c.IdentityPasswordBcrypt = pair[0], pair[1]
		return c, nil
	}

	var shared memo[natsauth.Credentials]
	realGenerate := generateCredentials
	generateCredentials = func() (natsauth.Credentials, error) {
		c, err := shared.get("shared", realGenerate)
		return copyCredentials(c), err
	}

	var logins memo[map[string]natsauth.Login]
	realWithLogins := withLogins
	withLogins = func(c natsauth.Credentials, existing, deployedHashes map[string]string) (natsauth.Credentials, error) {
		key, err := json.Marshal([2]map[string]string{existing, deployedHashes}) // map keys marshal sorted
		if err != nil {
			return natsauth.Credentials{}, err
		}
		got, err := logins.get(string(key), func() (map[string]natsauth.Login, error) {
			done, err := realWithLogins(natsauth.Credentials{}, existing, deployedHashes)
			return done.Logins, err
		})
		if err != nil {
			return natsauth.Credentials{}, err
		}
		c.Logins = maps.Clone(got)
		return c, nil
	}

	// The wrappers are captured HERE, not read back from the package variables later: a test that
	// calls useRealBrokerMinting swaps those variables, and a prewarm reading them from another
	// goroutine would race with the swap.
	memoGenerate, memoFromDeployed, memoIdentity, memoLogins := generateCredentials, credentialsFromDeployed, withIdentity, withLogins
	return func() {
		// The shared set and the two planted ones are independent mints (two hashes each), and the
		// derivations every deployed-instance test goes through hang off them. Warming them on
		// separate goroutines while the first tests run turns the package's single-core start-up
		// (those tests wait on exactly these values) into parallel work, on a machine whose other
		// cores are idle then. It computes the same values under the same keys the tests ask for,
		// so it can only make a first call cheaper, never change its answer.
		var wg sync.WaitGroup
		run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
		run(func() {
			c, err := memoGenerate()
			if err == nil {
				_, _ = memoLogins(c, nil, nil) // the fresh-install path: fifteen hashes
			}
		})
		run(func() { _, _ = memoIdentity(natsauth.Credentials{}, "", "") })
		for _, planted := range []func() (natsauth.Credentials, error){existingCredentials, otherExistingCredentials} {
			run(func() {
				c, err := planted()
				if err != nil {
					return
				}
				_, _ = memoFromDeployed(c.IssuerSeed, c.ServicePassword, c.SysPassword, natsauth.DeployedHashes{})
			})
		}
		wg.Wait()
	}
}

// 🔴 CREDENTIALS THAT STAND IN FOR "ALREADY THERE" MUST DIFFER FROM WHAT A FRESH MINT YIELDS.
//
// The memoized generateCredentials hands every caller the same set. A test that seeds a deployed
// instance or a bootstrap record with credentials and then asserts a re-run REUSED them would, if
// it seeded with that same set, pass for an implementation that ignored the deployed state and
// minted fresh: the fresh mint IS the seeded set. So the sets a test plants as pre-existing come
// from here, minted once per test binary like the shared one, and they are not the set a mint
// returns. existingCredentials is the deployed (or recorded) set; otherExistingCredentials is a
// second one, for a test that plants two sources that disagree. TestThePlantedCredentialSets...
// holds the three apart.
var existingCredentials, otherExistingCredentials = distinctMint("existing"), distinctMint("other-existing")

func distinctMint(name string) func() (natsauth.Credentials, error) {
	var m memo[natsauth.Credentials]
	return func() (natsauth.Credentials, error) {
		c, err := m.get(name, realBrokerMinting.generate)
		return copyCredentials(c), err
	}
}

func TestThePlantedCredentialSetsDifferFromAFreshMint(t *testing.T) {
	fresh, err := generateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	a, err := existingCredentials()
	if err != nil {
		t.Fatal(err)
	}
	b, err := otherExistingCredentials()
	if err != nil {
		t.Fatal(err)
	}
	sets := []natsauth.Credentials{fresh, a, b}
	for i := range sets {
		for j := i + 1; j < len(sets); j++ {
			x, y := sets[i], sets[j]
			if x.IssuerSeed == y.IssuerSeed || x.ServicePassword == y.ServicePassword ||
				x.SysPassword == y.SysPassword || x.XKeySeed == y.XKeySeed {
				t.Fatalf("credential sets %d and %d share a secret, so a test planting one and "+
					"expecting it reused cannot tell reuse from a fresh mint", i, j)
			}
		}
	}
}
