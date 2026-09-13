// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

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
	Into  func(*credentialSet) *string
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
			Into:  func(s *credentialSet) *string { return &s.RDBPassword },
		},
		{
			Field: "TSDBPassword",
			Ref:   mintedCredentialRef{infraNamespace, tsdbClusterName + "-app-credentials", secretKeyPassword},
			Into:  func(s *credentialSet) *string { return &s.TSDBPassword },
		},
	}

	if monitoringEnabled(st) {
		out = append(out, credentialPlacement{
			Field: "GrafanaAdminPassword",
			Ref:   mintedCredentialRef{monitoringNamespace, grafanaSecretName, keyGrafanaAdminPass},
			Into:  func(s *credentialSet) *string { return &s.GrafanaAdminPassword },
		})
	}

	if databaseBackupsEnabled(st) && !backupsAreExternal(st) {
		name := objectStoreName + "-credentials"
		out = append(out,
			credentialPlacement{
				Field: "ObjectStoreUser",
				Ref:   mintedCredentialRef{infraNamespace, name, keyMinioUser},
				Into:  func(s *credentialSet) *string { return &s.ObjectStoreUser },
			},
			credentialPlacement{
				Field: "ObjectStoreSecret",
				Ref:   mintedCredentialRef{infraNamespace, name, keyMinioPassword},
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
func readInstanceCredentials(ctx context.Context, typed kubernetes.Interface, st *State) (*credentialSet, error) {
	var set credentialSet

	for _, p := range credentialPlacements(st) {
		found, value, err := reuseMintedCredential(ctx, typed, st.Instance, st.InstanceUID, p.Ref)
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
			return nil, fmt.Errorf(
				"Secret %s/%s holds instance %q's %s, but it was not written by dcctl for this "+
					"instance — so this upgrade cannot tell whether the value in it is the one "+
					"the instance is running on. Refusing rather than guessing: an upgrade that "+
					"read the wrong value here would hand every service a credential nothing "+
					"has been told about",
				p.Ref.Namespace, p.Ref.Name, st.Instance, p.Field)

		default: // reuseAbsent
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
