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

func installMemoizedBrokerCredentials() {
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
}
