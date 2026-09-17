// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// credentialPlacement says where one field of credentialSet lives once it has been
// written.
//
// 🔴 IT EXISTS SO THE WRITER AND THE READER CANNOT DRIFT. planOwnedSecrets decides
// where a minted value goes; readInstanceCredentials has to look in exactly those
// places, and a second hand-written list of names and keys is a list that goes stale
// the first time a Secret is renamed — silently, because a reader looking in the
// wrong place finds nothing and "nothing" is a word this package is careful never to
// act on. One table, and a test that holds it against what the writer actually
// plans.
type credentialPlacement struct {
	// Field names the credentialSet member, for messages. The reflection-driven test
	// checks this against the struct, so it cannot quietly describe the wrong one.
	Field string
	Ref   mintedCredentialRef
	// Scope is the owner the value was written under — the same as the Secret's in
	// planOwnedSecrets, which the placement test holds this against.
	Scope ownerKind
	Into  func(*credentialSet) *string
	// WhenAbsent, if set, replaces the "it is gone" refusal for a credential whose
	// absence has a more likely cause than deletion.
	WhenAbsent func(instance string) error
}

// credentialPlacements lists every credential this configuration has, and where.
//
// The conditions mirror planOwnedSecrets exactly — monitoring decides the dashboard
// login, and the object store's root credential exists only when this instance
// stands one up rather than archiving to somebody else's. They mirror it rather than
// re-deciding it: TestThePlacementsAreWhereThePlanActuallyPutsThem renders both and
// compares, so a condition that moves in one and not the other fails the build.
//
// 🔴 THE SUPPLIED BACKUP CREDENTIAL IS DELIBERATELY ABSENT. It is not minted and not
// recovered — it comes from the operator's file, and an upgrade that "recovered" it
// would be reading back the value it was handed. Nothing in credentialSet carries it.
func credentialPlacements(st *State) []credentialPlacement {
	out := []credentialPlacement{
		{
			Field: "RDBPassword",
			Ref:   mintedCredentialRef{infraNamespace, rdbClusterName + "-app-credentials", secretKeyPassword},
			Scope: ownerCluster,
			Into:  func(s *credentialSet) *string { return &s.RDBPassword },
		},
		{
			Field: "RDBProvisionerPassword",
			Ref:   mintedCredentialRef{infraNamespace, rdbProvisionerSecretName, secretKeyPassword},
			Scope: ownerCluster,
			Into:  func(s *credentialSet) *string { return &s.RDBProvisionerPassword },
		},
		{
			Field: "RDBInstancePassword",
			Ref:   mintedCredentialRef{instanceNamespace(st.Instance), instanceRdbSecretName(st.Instance), secretKeyPassword},
			Into:  func(s *credentialSet) *string { return &s.RDBInstancePassword },
			// 🔴 NOT "restore it from a backup". An instance built before each instance had
			// a database login of its own never had this Secret, and restoring nothing
			// is not a remedy. That instance's services connect as the store's shared
			// owner, and the way to a login of its own is a rebuild.
			WhenAbsent: func(instance string) error {
				return fmt.Errorf("instance %q has no database login of its own (no Secret %s/%s): it was "+
					"built before each instance had one, and its data belongs to the shared owner. An "+
					"upgrade cannot move it; recreate the instance (`dcctl destroy` then `dcctl bootstrap`) "+
					"on a cluster built by this dcctl", instance, instanceNamespace(instance), instanceRdbSecretName(instance))
			},
		},
		{
			Field: "TSDBPassword",
			Ref:   mintedCredentialRef{instanceNamespace(st.Instance), tsdbClusterName + "-app-credentials", secretKeyPassword},
			Into:  func(s *credentialSet) *string { return &s.TSDBPassword },
		},
	}

	if monitoringEnabled(st) {
		out = append(out, credentialPlacement{
			Field: "GrafanaAdminPassword",
			Ref:   mintedCredentialRef{monitoringNamespace, grafanaSecretName, keyGrafanaAdminPass},
			Scope: ownerCluster,
			Into:  func(s *credentialSet) *string { return &s.GrafanaAdminPassword },
		})
	}

	if databaseBackupsEnabled(st) && !backupsAreExternal(st) {
		name := objectStoreName + "-credentials"
		out = append(out,
			credentialPlacement{
				Field: "ObjectStoreUser",
				Ref:   mintedCredentialRef{infraNamespace, name, keyMinioUser},
				Scope: ownerCluster,
				Into:  func(s *credentialSet) *string { return &s.ObjectStoreUser },
			},
			credentialPlacement{
				Field: "ObjectStoreSecret",
				Ref:   mintedCredentialRef{infraNamespace, name, keyMinioPassword},
				Scope: ownerCluster,
				Into:  func(s *credentialSet) *string { return &s.ObjectStoreSecret },
			},
		)
	}

	return out
}

// readInstanceCredentials recovers every credential a live instance is already
// running on. It mints nothing, and it refuses rather than substituting.
//
// 🔴 THIS IS THE HALF OF THE CREATE/EVOLVE SPLIT THAT MAKES THE OTHER HALF POSSIBLE.
// bootstrap mints, because it is building an instance that does not exist yet;
// upgrade reads, because every one of these values is already load-bearing somewhere
// that will not be told it changed. A database owner's password was set when
// CloudNativePG created the Cluster and is reconciled by nothing afterwards; the
// object store reads its root credential at start-up while the archiver is handed it
// through a different object on a different schedule. Minting a replacement for
// either is not an update, it is a break — and it is a break that reports success.
//
// So there is no "mint if missing" branch here, and adding one would dissolve the
// distinction this verb exists to draw. An absent credential is a refusal with a
// sentence explaining which value is gone and what it was authenticating.
//
// 🔑 REUSE STOPPED BEING A MINTING DECISION AND BECAME A READ. That is why this is
// short: bootstrap's resolveCredentials has to decide, per credential, whether a
// fresh value would be destructive, because it mints first and asks afterwards. Here
// the answer is uniform — everything is kept — and the only question left is whether
// it is actually there.
// describeForeign reads a Secret back to say why it is not `want`'s.
func describeForeign(ctx context.Context, typed kubernetes.Interface, ref mintedCredentialRef, want secretOwner) string {
	s, err := typed.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Sprintf("it could not be read again to say why (%v)", err)
	}
	return foreignReason(readOwnership(s), want)
}

func readInstanceCredentials(ctx context.Context, typed kubernetes.Interface, st *State) (*credentialSet, error) {
	var set credentialSet

	for _, p := range credentialPlacements(st) {
		owner := ownerFor(p.Scope, st)
		found, value, err := reuseMintedCredential(ctx, typed, owner, p.Ref)
		if err != nil {
			return nil, err
		}
		switch found {
		case reuseRecovered:
			*p.Into(&set) = value

		case reuseForeign:
			// Named as ownership rather than as absence, which is the distinction a
			// live cluster had to teach this package once already. A Secret sitting
			// right there, written by something else, is a different problem with a
			// different answer than one that is gone.
			//
			// The reason comes from foreignReason, the same sentence the writer uses, so the
			// one case with a specific remedy — a Secret stamped before shared credentials
			// were the cluster's — says so instead of claiming dcctl did not write it.
			return nil, fmt.Errorf(
				"Secret %s/%s holds instance %q's %s, but it is not %s's: %s. This upgrade "+
					"cannot tell whether the value in it is the one the instance is running on, "+
					"and an upgrade that read the wrong value would hand every service a "+
					"credential nothing has been told about",
				p.Ref.Namespace, p.Ref.Name, st.Instance, p.Field, owner,
				describeForeign(ctx, typed, p.Ref, owner))

		default: // reuseAbsent
			if p.WhenAbsent != nil {
				return nil, p.WhenAbsent(st.Instance)
			}
			return nil, fmt.Errorf(
				"instance %q is running but Secret %s/%s — which holds its %s — is gone. An "+
					"upgrade keeps the credentials an instance is already running on and mints "+
					"nothing, because the things authenticating with this one (the database "+
					"role, the object store, the dashboard) were configured with it and are not "+
					"reconciled from anywhere else. Restore the Secret from a backup if you have "+
					"one; a fresh value would leave this instance refusing its own services "+
					"while looking healthy",
				st.Instance, p.Ref.Namespace, p.Ref.Name, p.Field)
		}
	}

	return &set, nil
}
