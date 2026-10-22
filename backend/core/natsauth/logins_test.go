// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestGenerateCredentialsMintsALoginForEveryPrincipalAndNoTwoAlike(t *testing.T) {
	c, err := GenerateCredentials()
	require.NoError(t, err)
	require.Len(t, c.Logins, len(config.NatsLoginPrincipals()))

	passwords, users := map[string]bool{}, map[string]bool{}
	for _, p := range config.NatsLoginPrincipals() {
		l, ok := c.Logins[p]
		require.True(t, ok, "no login for %s", p)
		assert.Equal(t, "dc-"+p+"-g1", l.User)
		assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(l.PasswordBcrypt), []byte(l.Password)),
			"the hash for %s does not verify against its plaintext", p)
		assert.False(t, passwords[l.Password], "two principals share a password: a login that is the same as another's is not its own")
		assert.False(t, users[l.User])
		passwords[l.Password], users[l.User] = true, true
	}
	// Distinct from the shared logins too.
	for _, shared := range []string{c.ServicePassword, c.SysPassword, c.IdentityPassword} {
		assert.False(t, passwords[shared])
	}
	assert.Equal(t, AdminUser, c.Logins[config.NatsAdminPrincipal].User)
}

// A login that is present is NEVER replaced; one that is absent is minted; a hash the broker holds
// is reused only if it verifies.
func TestWithLoginsReusesWhatIsPresentAndMintsWhatIsNot(t *testing.T) {
	first, err := GenerateCredentials()
	require.NoError(t, err)

	existing := map[string]string{}
	hashes := map[string]string{}
	for _, p := range []string{"device-management", "event-sources"} {
		existing[p] = first.Logins[p].Password
		hashes[first.Logins[p].User] = first.Logins[p].PasswordBcrypt
	}
	// A deployed hash that does not match the password must not be shipped.
	hashes[first.Logins["event-sources"].User] = first.Logins["device-management"].PasswordBcrypt

	got, err := Credentials{}.WithLogins(existing, hashes)
	require.NoError(t, err)

	assert.Equal(t, first.Logins["device-management"], got.Logins["device-management"], "a present login with a verifying hash is reused as it is")
	assert.Equal(t, first.Logins["event-sources"].Password, got.Logins["event-sources"].Password, "a present password was replaced")
	assert.NotEqual(t, first.Logins["event-sources"].PasswordBcrypt, got.Logins["event-sources"].PasswordBcrypt,
		"a hash of a different password was shipped")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(got.Logins["event-sources"].PasswordBcrypt), []byte(existing["event-sources"])))
	// Everything else was absent, so it is new.
	for _, p := range config.NatsLoginPrincipals() {
		if _, had := existing[p]; had {
			continue
		}
		assert.NotEmpty(t, got.Logins[p].Password, "%s was not introduced", p)
		assert.NotEqual(t, first.Logins[p].Password, got.Logins[p].Password, "%s was not freshly minted", p)
	}
}

// Principals the catalog does not know are ignored rather than becoming logins.
func TestWithLoginsIgnoresAPrincipalNobodyDeclared(t *testing.T) {
	got, err := Credentials{}.WithLogins(map[string]string{"not-an-area": "x"}, nil)
	require.NoError(t, err)
	assert.NotContains(t, got.Logins, "not-an-area")
}
