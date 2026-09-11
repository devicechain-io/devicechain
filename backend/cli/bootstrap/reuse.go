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
// Three answers, and the middle one is the one that is easy to get wrong:
//
//   - ABSENT — nothing here yet. Returns "" so the caller mints. A missing namespace
//     reads the same way, which is what a first bootstrap looks like.
//   - PRESENT BUT NOT OURS — returns "" as well, deliberately. writeOwnedSecret
//     already refuses a foreign or previous-generation Secret, with a message that
//     says which case it is and what to do; answering that question a second time
//     here would give the same run two refusals that could drift apart.
//   - PRESENT, OURS, AND EMPTY — an ERROR. 🔴 MALFORMED IS NOT ABSENT. Reading a
//     blank key as "nothing to reuse" mints a fresh password over a live database
//     whose role keeps the old one, which is a green run that leaves every service
//     unable to log in.
//
// A read that FAILS is also an error rather than a mint, for the same reason it is in
// clusterArchivePath: "we could not tell" must never resolve to the destructive
// answer.
func reuseMintedCredential(
	ctx context.Context,
	typed kubernetes.Interface,
	instance, instanceUID string,
	ref mintedCredentialRef,
) (string, error) {
	s, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading Secret %s/%s to see whether this instance already has a "+
			"credential there: %w. Refusing to continue: minting a fresh one is the answer that "+
			"cannot be undone, so it must not be reached by failing to look",
			ref.Namespace, ref.Name, err)
	}

	own := readOwnership(s)
	if !own.managed || own.instance != instance || own.uid != instanceUID {
		return "", nil
	}

	// StringData is write-only on a real API server — what comes back is Data.
	v := string(s.Data[ref.Key])
	if v == "" {
		return "", fmt.Errorf("Secret %s/%s was minted by this instance but its %q entry is "+
			"empty, so the credential it is running on cannot be recovered from it. Minting a "+
			"replacement would leave the services holding a value the database was never told "+
			"about", ref.Namespace, ref.Name, ref.Key)
	}
	return v, nil
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
func refuseUnrecoverableDatabaseCredential(clusterExists bool, reused, cluster, secretName string) error {
	if !clusterExists || reused != "" {
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
