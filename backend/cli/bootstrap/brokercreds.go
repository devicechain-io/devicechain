// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/natsauth"
)

// brokerCredentialsFromRunning rebuilds the broker's credentials from the plaintexts a
// running instance holds, reusing the bcrypt hashes the broker is ALREADY configured
// with.
//
// 🔴 REUSE MEANS THE SAME STRING, NOT A HASH OF THE SAME PASSWORD. bcrypt salts, so
// re-hashing an unchanged password writes a different string, and the broker adopts
// its configuration by rolling on a checksum of it — so a fresh hash restarts every
// broker server on a run that changed nothing. The lookup is best-effort: an empty
// result costs a fresh hash, and a supplied one is used only if it verifies against
// the plaintext.
//
// One function for every caller that reuses a running broker's logins — a bootstrap
// over a live instance, a bootstrap resuming from this machine's record, and an
// upgrade applying the instance's infrastructure — so they cannot come to read the
// hashes from different places.
//
// xkeySeed is the callout's curve seed if the instance already has one. An empty one is
// MINTED, not refused: the xkey is an introduced credential (see calloutsecret.go), absent from
// the starting state of any instance that predates it, and minting it replaces nothing. The same
// is true of identityPassword, the login for the identity account (see identitysecret.go), and of
// logins, the per-area ones keyed by principal (see natsloginsecret.go): a principal with none is
// minted, one that has a password keeps it.
func brokerCredentialsFromRunning(ctx context.Context, st *State, seed, xkeySeed, identityPassword string, logins map[string]string, password, sysPassword string) (natsauth.Credentials, error) {
	hashes := lookupDeployedBrokerHashes(ctx, st.KubeContext, InstanceNamespace(st.Instance), natsStatefulSetName)
	var creds natsauth.Credentials
	var err error
	if password == "" {
		// 🔴 NO SHARED LOGIN IS NOT AN OMISSION TO REPAIR, BUT IT IS ONLY EVER ANSWERED THIS WAY WHEN
		// EVERY SERVICE HAS A LOGIN OF ITS OWN. An instance that carries neither would otherwise be
		// rebuilt with no credential for anything to connect with, and the first sign would be every
		// pod refused at start. See sharedlogin.go.
		if err := requireEveryPrincipalHasALogin(logins); err != nil {
			return natsauth.Credentials{}, fmt.Errorf("the running instance carries no shared service password, and %w", err)
		}
		creds, err = credentialsFromDeployedWithoutSharedLogin(seed, sysPassword, hashes)
	} else {
		creds, err = credentialsFromDeployed(seed, password, sysPassword, hashes)
	}
	if err != nil {
		return natsauth.Credentials{}, err
	}
	creds, err = creds.WithXKey(xkeySeed)
	if err != nil {
		return natsauth.Credentials{}, err
	}
	creds, err = withIdentity(creds, identityPassword, hashes.Identity)
	if err != nil {
		return natsauth.Credentials{}, err
	}
	return withLogins(creds, logins, hashes.Logins)
}

// setBrokerCredentialValues is where the broker's credentials enter the values both
// halves are rendered from: the public issuer and the hashes for the infrastructure
// apply (infraVars), the seed and the plaintexts for the instance configuration.
func setBrokerCredentialValues(st *State, creds natsauth.Credentials) {
	st.Values["natsCalloutIssuerPublic"] = creds.IssuerPublic
	st.Values["natsCalloutIssuerSeed"] = creds.IssuerSeed
	st.Values["natsCalloutXKeyPublic"] = creds.XKeyPublic
	st.Values[brokerXKeyValue] = creds.XKeyPublic
	st.Values["natsCalloutXKeySeed"] = creds.XKeySeed
	st.Values["natsServicePassword"] = creds.ServicePassword
	st.Values["natsServicePasswordBcrypt"] = creds.ServicePasswordBcrypt
	st.Values["natsSysPassword"] = creds.SysPassword
	st.Values["natsSysPasswordBcrypt"] = creds.SysPasswordBcrypt
	setIdentityValues(st, creds)
	setLoginValues(st, creds)
}

// mintBrokerCredentials is the fresh mint of a bootstrap that has nothing to reuse: the shared set
// AND a login for every principal. The logins are asked for here, explicitly, because
// natsauth.GenerateCredentials no longer includes them; without this call a fresh instance would
// come up with the per-area logins switched off (areaLoginsEnabledFor needs every one).
//
// 🔴 A FRESH INSTALL NEVER HAS THE SHARED LOGIN. The shared password the generator mints is dropped
// here, once every principal has a login of its own, so nothing downstream (the broker's users, the
// instance document, the bootstrap record) is ever handed it. It is the transitional login of an
// upgraded instance and exists nowhere else.
func mintBrokerCredentials() (natsauth.Credentials, error) {
	creds, err := generateCredentials()
	if err != nil {
		return natsauth.Credentials{}, err
	}
	creds, err = withLogins(creds, nil, nil)
	if err != nil {
		return natsauth.Credentials{}, err
	}
	if err := requireEveryPrincipalHasALogin(loginPlaintexts(creds)); err != nil {
		return natsauth.Credentials{}, fmt.Errorf("minting the broker credentials: %w", err)
	}
	creds.ServicePassword, creds.ServicePasswordBcrypt = "", ""
	return creds, nil
}

// The calls that hash a broker password every bootstrap and upgrade path goes through, held as variables so a
// test can stand in a memoizing wrapper of the REAL function (see credentials_fixture_test.go).
// Production never reassigns them, and they carry no parameter that changes how a password is
// hashed: they are the dependency seam for "where do broker credentials come from", the same
// shape as namespacePrecheckClient.
var (
	generateCredentials     = natsauth.GenerateCredentials
	credentialsFromDeployed = natsauth.CredentialsFromDeployed
	// ...and the same for an instance with no shared login, so a test that settles one is memoized too.
	credentialsFromDeployedWithoutSharedLogin = natsauth.CredentialsFromDeployedWithoutSharedLogin
	withIdentity                              = func(c natsauth.Credentials, existingPassword, deployedHash string) (natsauth.Credentials, error) {
		return c.WithIdentity(existingPassword, deployedHash)
	}
	withLogins = func(c natsauth.Credentials, existing, deployedHashes map[string]string) (natsauth.Credentials, error) {
		return c.WithLogins(existing, deployedHashes)
	}
)
