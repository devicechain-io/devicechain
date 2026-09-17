// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
)

// writeMintedSecrets puts every credential this run settled where its consumer
// expects to find it.
//
// 🔴 ONE LOOP OVER ONE PLAN, NOT A WRITE PER CALL SITE. Each of these Secrets used
// to be declared by OpenTofu, which meant the apply was the single thing that could
// not leave one out. Replacing that with writes scattered through the bring-up would
// trade a guarantee for a habit — and the failure mode of a forgotten write is not a
// missing Secret, it is a workload that starts with a credential nobody chose. So
// placement stays a plan (planClusterSecrets, planInstanceSecrets), this walks it, and
// TestEveryMintedCredentialIsPlacedSomewhere holds the plan against the struct.
//
// 🔑 THE BROKER'S TLS IS APPENDED HERE RATHER THAN LIVING IN THE PLAN, because it is
// not made of the same stuff: the plans place values from credentialSet, and
// a certificate is material with a lifetime rather than entropy with a length.
// Keeping it out of that struct is what stops the reflection test above from
// reporting a certificate as an unplaced password.
func writeMintedSecrets(ctx context.Context, typed kubernetes.Interface, st *State) error {
	if st.Credentials == nil {
		// Not defensive padding. The render step settles these and the pipeline runs
		// it first, so reaching here without them means a caller assembled its own
		// order — and the destructive reading of "no credentials" is to write
		// nothing and let the apply mint its own.
		return fmt.Errorf("refusing to apply infrastructure for instance %q: its credentials "+
			"were never settled, so the databases would be built with values nothing else "+
			"holds. The render step must run before the infrastructure step", st.Instance)
	}

	secrets := planInstanceSecrets(st, st.Credentials, st.InstanceArchive)
	if st.NATSTLS != nil {
		// Two objects, and which key goes in which is the security boundary. The
		// broker gets its leaf and the CA certificate; the authority's private key
		// goes somewhere the broker does not mount, so that only the thing which
		// re-issues certificates can read it. See natsAuthoritySecret for why it is
		// kept at all.
		secrets = append(secrets,
			natsTLSSecret(natsReleaseName, st.NATSTLS),
			natsAuthoritySecret(natsReleaseName, st.NATSTLS))
	}

	return writeSecrets(ctx, typed, st, secrets, "this instance's infrastructure")
}

// writeClusterSecrets is the install's counterpart: the credentials the shared
// prerequisites are built from, written before the cluster apply for the same reason.
func writeClusterSecrets(ctx context.Context, typed kubernetes.Interface, st *State) error {
	if st.Credentials == nil {
		return fmt.Errorf("refusing to apply the cluster prerequisites: their credentials were " +
			"never settled, so the relational store would be built with values nothing else holds")
	}
	// The dashboard credential's namespace is the monitoring stack's, and the apply
	// is what creates that — so it has to exist before this write rather than
	// part-way through the step this write precedes.
	if monitoringEnabled(st) {
		if err := ensureMonitoringNamespace(ctx, typed, monitoringNamespace); err != nil {
			return err
		}
	}
	secrets, _ := planClusterSecrets(st, st.Credentials)
	return writeSecrets(ctx, typed, st, secrets, "the cluster prerequisites")
}

func writeSecrets(ctx context.Context, typed kubernetes.Interface, st *State, secrets []ownedSecret, what string) error {
	for _, spec := range secrets {
		// Each Secret is written as its SCOPE's: the shared credentials the cluster
		// prerequisites are built from belong to the cluster, everything else to the
		// instance.
		if err := writeOwnedSecret(ctx, typed, ownerFor(spec.Scope, st), spec, time.Now); err != nil {
			return fmt.Errorf("writing the credential %s is built from: %w", what, err)
		}
	}
	return nil
}
