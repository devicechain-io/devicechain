// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// settleCredentials is the seam the render step asks through, so the decision can be
// exercised without a cluster — the same indirection, for the same reason, as
// readLiveArchiveState and lookupDeployedInstance.
//
// The client is built here rather than threaded through State because a dry run
// needs none: it deploys nothing, so it has nothing to read back and nothing to
// preserve.
var settleCredentials = func(ctx context.Context, st *State, live liveArchiveState) (*credentialSet, error) {
	if st.DryRun {
		return resolveCredentials(ctx, nil, st, live)
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster to see which credentials this "+
			"instance is already running on: %w", err)
	}
	return resolveCredentials(ctx, typed, st, live)
}

// reuseOutcome says what was found where a previously minted credential would be.
// Three states rather than a string, because "not there" and "there but somebody
// else's" call for opposite handling and reading the second as the first produces a
// refusal that describes a cluster nobody has.
type reuseOutcome int

const (
	reuseAbsent reuseOutcome = iota
	reuseForeign
	reuseRecovered
)

// mintedCredentialRef locates one value dcctl has written on an earlier run.
type mintedCredentialRef struct {
	Namespace string
	Name      string
	Key       string
}

// reuseMintedCredential reads back a credential this instance is already running on.
//
// 🔴 IT READS THE SECRET, NOT THE INSTANCE CONFIG DOCUMENT, AND THAT IS THE WHOLE
// POINT. The document is what the SERVICES read; the Secret is what the database was
// BUILT from. CloudNativePG sets the owner's password once, when it creates the
// Cluster, out of `bootstrap.initdb.secret` — and the owner is declared under
// `managed.roles` with no `passwordSecret`, so nothing reconciles it afterwards.
// Where the two can disagree, the Secret is the one telling the truth about what the
// role will actually admit, so it is the one reuse follows.
//
// Three answers, and they are DISTINCT rather than collapsed into a string:
//
//   - reuseAbsent — nothing here yet. The caller mints. A missing namespace reads the
//     same way, which is what a first bootstrap looks like.
//   - reuseForeign — present, but dcctl did not write it. The caller must NOT mint
//     over it and must not report it as missing either. writeOwnedSecret refuses it
//     later with a message that says which case it is; this only has to avoid
//     claiming something untrue in the meantime.
//   - reuseRecovered — present, ours, and carrying a value.
//
// 🔴 ABSENT AND FOREIGN USED TO BE ONE ANSWER, AND THAT WAS A LIE THE CALLER TOLD.
// Both returned "", so refuseUnrecoverableDatabaseCredential said "the only copy of
// the password is gone" about a Secret sitting right there — which is the exact
// message an operator sees on any instance built before dcctl owned these, where the
// Secret exists and belongs to OpenTofu. Measured against a real 30-day-old instance.
//
// 🔴 PRESENT, OURS, AND EMPTY is still an ERROR. MALFORMED IS NOT ABSENT: reading a
// blank key as "nothing to reuse" mints a fresh password over a live database whose
// role keeps the old one, which is a green run that leaves every service unable to
// log in.
//
// A read that FAILS is also an error rather than a mint, for the same reason it is in
// clusterArchivePath: "we could not tell" must never resolve to the destructive
// answer.
func reuseMintedCredential(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, instanceUID string,
	ref mintedCredentialRef,
) (reuseOutcome, string, error) {
	s, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return reuseAbsent, "", nil
	case err != nil:
		return reuseAbsent, "", fmt.Errorf("reading Secret %s/%s to see whether this instance already has a "+
			"credential there: %w. Refusing to continue: minting a fresh one is the answer that "+
			"cannot be undone, so it must not be reached by failing to look",
			ref.Namespace, ref.Name, err)
	}

	own := readOwnership(s)
	if !own.managed || own.instance != instance || own.uid != instanceUID {
		return reuseForeign, "", nil
	}

	// StringData is write-only on a real API server — what comes back is Data.
	v := string(s.Data[ref.Key])
	if v == "" {
		return reuseAbsent, "", fmt.Errorf("Secret %s/%s was minted by this instance but its %q entry is "+
			"empty, so the credential it is running on cannot be recovered from it. Minting a "+
			"replacement would leave the services holding a value the database was never told "+
			"about", ref.Namespace, ref.Name, ref.Key)
	}
	return reuseRecovered, v, nil
}

// refuseUnrecoverableDatabaseCredential stops a run that would hand a live database a
// password it has never been told about.
//
// 🔴 THE TRAP IS A CLUSTER THAT OUTLIVED ITS CREDENTIALS SECRET. The password the
// owner role actually holds was written into it at CREATE time and exists nowhere
// else — `managed.roles` declares the owner with no `passwordSecret`, so CNPG never
// reconciles it, and PostgreSQL stores it hashed, so it cannot be read back out of
// the database either. Delete the Secret and the value is gone while the role that
// wants it keeps running.
//
// Minting a replacement in that state is the worst available outcome: every write
// succeeds, the apply is green, and every service fails authentication against a
// database that is itself perfectly healthy. So this refuses instead, and says the
// two things an operator in that position needs — that the value is unrecoverable,
// and that the way out is a rebuild rather than another run.
func refuseUnrecoverableDatabaseCredential(clusterExists bool, found reuseOutcome, cluster, secretName string) error {
	// 🔴 ONLY reuseAbsent. A FOREIGN Secret is not a missing one, and saying it is
	// describes a cluster the operator is not looking at. On every instance built
	// before dcctl owned these credentials the Secret is present and belongs to
	// OpenTofu — the exact case this refusal would otherwise fire on, with the exact
	// wrong sentence. That population is refused by checkNoRetiredInfrastructure,
	// which explains what actually happened; anything it does not catch is refused by
	// writeOwnedSecret, which names the ownership it found. Neither needs help here.
	if !clusterExists || found != reuseAbsent {
		return nil
	}
	return fmt.Errorf(
		"database %q is running but Secret %s/%s — the only copy of the password its owner role "+
			"was created with — is gone. PostgreSQL stores that password hashed and CloudNativePG "+
			"does not reconcile it after bootstrap, so it cannot be recovered from either of them, "+
			"and minting a new one would leave this cluster refusing every service while looking "+
			"healthy. Restore the Secret from a backup if you have one; otherwise this instance has "+
			"to be rebuilt (`dcctl destroy` then `dcctl bootstrap`), taking its data with it",
		cluster, infraNamespace, secretName)
}
