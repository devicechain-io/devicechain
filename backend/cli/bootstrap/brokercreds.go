// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"

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
// is true of identityPassword, the login for the identity account (see identitysecret.go).
func brokerCredentialsFromRunning(ctx context.Context, st *State, seed, xkeySeed, identityPassword, password, sysPassword string) (natsauth.Credentials, error) {
	hashes := lookupDeployedBrokerHashes(ctx, st.KubeContext, InstanceNamespace(st.Instance), natsStatefulSetName)
	creds, err := natsauth.CredentialsFromDeployed(seed, password, sysPassword, hashes)
	if err != nil {
		return natsauth.Credentials{}, err
	}
	creds, err = creds.WithXKey(xkeySeed)
	if err != nil {
		return natsauth.Credentials{}, err
	}
	return creds.WithIdentity(identityPassword, hashes.Identity)
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
}
