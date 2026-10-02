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
func brokerCredentialsFromRunning(ctx context.Context, st *State, seed, password, sysPassword string) (natsauth.Credentials, error) {
	return natsauth.CredentialsFromDeployed(seed, password, sysPassword,
		lookupDeployedBrokerHashes(ctx, st.KubeContext, InstanceNamespace(st.Instance), natsStatefulSetName))
}

// setBrokerCredentialValues is where the broker's credentials enter the values both
// halves are rendered from: the public issuer and the hashes for the infrastructure
// apply (infraVars), the seed and the plaintexts for the instance configuration.
func setBrokerCredentialValues(st *State, creds natsauth.Credentials) {
	st.Values["natsCalloutIssuerPublic"] = creds.IssuerPublic
	st.Values["natsCalloutIssuerSeed"] = creds.IssuerSeed
	st.Values["natsServicePassword"] = creds.ServicePassword
	st.Values["natsServicePasswordBcrypt"] = creds.ServicePasswordBcrypt
	st.Values["natsSysPassword"] = creds.SysPassword
	st.Values["natsSysPasswordBcrypt"] = creds.SysPasswordBcrypt
}
