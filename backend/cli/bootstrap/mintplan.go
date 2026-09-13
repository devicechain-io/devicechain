// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// Names and keys the consumers of these Secrets already expect. They are restated
// here because dcctl becomes the writer, and every one of them is a contract with
// something outside this file: the two Cluster resources name their credential
// Secret by convention (`<cluster>-app-credentials`), and the object store's key
// names are read both by its own container and by the backup plugin's reference.
const (
	rdbClusterName  = "dc-rdb"
	tsdbClusterName = "dc-tsdb"
	objectStoreName = "dc-object-store"

	// 🔴 THE ROLE NAME IS NOT A SECRET AND MUST NOT BE MINTED. It appears in the
	// infrastructure variables, in the chart's connection defaults and in the
	// compiled-in configuration, and a value that disagrees across those is a
	// database that refuses its own services. Only the password moves here.
	dbRoleUsername = "devicechain"

	// The object store's access key is an identity, not entropy, for the same
	// reason — but unlike the database role nothing outside these Secrets names it,
	// so it is minted alongside its secret half.
	secretKeyUsername = "username"
	secretKeyPassword = "password"
	keyMinioUser      = "MINIO_ROOT_USER"
	keyMinioPassword  = "MINIO_ROOT_PASSWORD"

	// cnpgReloadLabel is presence-checked by the database operator. 🔴 Without it a
	// credential change lands in the Secret and the database keeps the old password,
	// silently.
	cnpgReloadLabel = "cnpg.io/reload"

	// The dashboard login. Its Secret lives in the MONITORING namespace, not the
	// infrastructure one — Grafana is installed by its own release there, and a
	// Secret a chart reads has to be in the chart's namespace.
	//
	// The key names are the Grafana chart's, read through admin.existingSecret with
	// admin.userKey / admin.passwordKey. They are restated here because they are that
	// chart's contract and nothing in this repository would fail to build if they
	// moved.
	monitoringNamespace = "monitoring"
	grafanaSecretName   = "dc-grafana-admin"
	grafanaAdminUser    = "admin"
	keyGrafanaAdminUser = "admin-user"
	keyGrafanaAdminPass = "admin-password"
)

// credentialSet is the entropy one run generates for the credentials that have no
// generator at all today — the ones whose "default" is a literal committed to this
// repository, shared by every instance ever built from it.
//
// One struct, produced in one call, because §5.1a's rule is that entropy is minted
// once before anything is applied. A rule spread across the call sites that happen to
// need a value is not checkable; this is.
type credentialSet struct {
	RDBPassword          string
	TSDBPassword         string
	ObjectStoreUser      string
	ObjectStoreSecret    string
	GrafanaAdminPassword string
}

// databaseBackupsEnabled reports whether this run provisions a backup destination.
//
// 🔴 ONE DEFINITION, CROSS-CHECKED AGAINST THE VARIABLES THAT CARRY IT. infraVars
// decides the same thing by appending `enable_database_backups=false` from two
// separate branches, and a second copy of that reasoning here would drift the day
// either branch moves — minting an object-store credential nothing reads, or worse,
// not minting one the store needs. TestTheBackupPredicateMatchesTheVariablesEmitted
// holds the two together.
func databaseBackupsEnabled(st *State) bool {
	if st.NoCNPG {
		return false
	}
	// The compact preset drops cert-manager when it is also serving plain HTTP, and
	// the backup plugin renders a cert-manager Issuer, so backups go with it.
	if st.Compact && st.NoTLS {
		return false
	}
	return true
}

// monitoringEnabled reports whether the observability stack is part of this run, and
// therefore whether a dashboard credential is needed at all.
func monitoringEnabled(st *State) bool { return !st.NoMonitoring }

// backupsAreExternal reports whether this run archives to an object store the
// operator already owns rather than one it stands up.
//
// 🔴 THE TWO DESTINATIONS ARE MUTUALLY EXCLUSIVE, AND GETTING THAT WRONG WRITES A
// CREDENTIAL NOTHING READS. An external destination provisions no object store, so
// minting root credentials for one produces `dc-object-store-credentials` with no
// MinIO to authenticate against — while `dc-backup-credentials`, the Secret the
// archiver actually presents, goes unwritten. Measured as a real gap in this package
// before the supplied path existed: dcctl knew only "backups on or off".
func backupsAreExternal(st *State) bool { return st.BackupDestination.Configured() }

// planOwnedSecrets says which Secrets this run writes, and what goes in each.
//
// Deciding the whole set before writing any of it is deliberate: a plan can be shown
// under --dry-run, counted in a test, and compared against what the apply expects to
// find, none of which is possible if placement is a side effect of walking the
// pipeline.
//
// 🔴 THE INSTANCE CONFIG DOCUMENT IS NOT HERE. The credentials services read travel
// inside one JSON document rather than one Secret each, and that document cannot be
// composed until the values that are derived from an apply are known. It is written
// by the composition step, through the same writer.
func planOwnedSecrets(st *State, set *credentialSet) []ownedSecret {
	dbLabels := func(cluster string) map[string]string {
		return map[string]string{
			"app.kubernetes.io/name":      cluster,
			"app.kubernetes.io/component": "database",
			cnpgReloadLabel:               "true",
		}
	}

	// Both database stores, always. 🔴 Neither Cluster is gated on the flag that
	// skips the operator install — that flag means "an operator is already here",
	// and gating the stores on it would turn that into "this platform has no
	// database". So a run that skips the operator still needs these credentials.
	out := []ownedSecret{
		{
			Name:      rdbClusterName + "-app-credentials",
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeBasicAuth,
			Labels:    dbLabels(rdbClusterName),
			Data: map[string]string{
				secretKeyUsername: dbRoleUsername,
				secretKeyPassword: set.RDBPassword,
			},
		},
		{
			Name:      tsdbClusterName + "-app-credentials",
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeBasicAuth,
			Labels:    dbLabels(tsdbClusterName),
			Data: map[string]string{
				secretKeyUsername: dbRoleUsername,
				secretKeyPassword: set.TSDBPassword,
			},
		},
	}

	if monitoringEnabled(st) {
		// 🔴 THIS WAS MINTED AND PLACED NOWHERE. mintNewCredentials generated a
		// dashboard password whenever monitoring was on, and no Secret in this plan
		// ever carried it — a credential produced on every run and dropped on the
		// floor. Nothing could see it: the mutation suite tests this code against the
		// model behind it, and the model itself had forgotten the field. What catches
		// it now is TestEveryMintedCredentialIsPlacedSomewhere, which walks the struct
		// rather than the cases anybody thought to list.
		out = append(out, ownedSecret{
			Name:      grafanaSecretName,
			Namespace: monitoringNamespace,
			Type:      corev1.SecretTypeOpaque,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "grafana",
				"app.kubernetes.io/component": "monitoring",
			},
			Data: map[string]string{
				keyGrafanaAdminUser: grafanaAdminUser,
				keyGrafanaAdminPass: set.GrafanaAdminPassword,
			},
		})
	}

	if databaseBackupsEnabled(st) && backupsAreExternal(st) {
		// Supplied, not minted — see BackupDestination. It is written through the same
		// writer as everything else, because ownership is about who writes the object.
		out = append(out, backupCredentialsSecret(st.BackupDestination))
	}

	if databaseBackupsEnabled(st) && !backupsAreExternal(st) {
		out = append(out, ownedSecret{
			Name:      objectStoreName + "-credentials",
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeOpaque,
			Labels: map[string]string{
				"app.kubernetes.io/name":      objectStoreName,
				"app.kubernetes.io/component": "object-store",
			},
			Data: map[string]string{
				keyMinioUser:     set.ObjectStoreUser,
				keyMinioPassword: set.ObjectStoreSecret,
			},
		})
	}

	return out
}

// resolveCredentials settles every credential this run needs: REUSED where the
// instance already has one and a fresh value would be destructive, MINTED otherwise.
//
// 🔴 REUSE IS NOT UNIFORM, AND TREATING IT AS ONE RULE IS HOW THE BROKER'S
// CERTIFICATE EXPIRES. Two of these credentials cannot be re-minted under a running
// instance without breaking it, and the rest can. The split is stated here, once,
// rather than being a property of whichever call site was written last:
//
//   - THE DATABASE PASSWORDS ARE REUSED, ALWAYS. CloudNativePG sets the owner role's
//     password when it CREATES the Cluster and never again — the role is declared
//     under `managed.roles` with no `passwordSecret`, so nothing reconciles it. A
//     fresh value would reach every service and none of the two databases.
//   - THE OBJECT-STORE CREDENTIALS ARE REUSED. Re-minting them is recoverable, but
//     not cheaply: the store reads its root credentials at start-up while the backup
//     archiver is handed them through a different object on a different schedule, so
//     a new value opens a window in which the archiver cannot authenticate and the
//     first visible symptom is that WAL stopped being shipped.
//   - THE DASHBOARD PASSWORD IS MINTED EVERY RUN, deliberately. Both halves are
//     written by the same run, so the cost is a rollout-length window of failing
//     logins — the same trade the Grafana SSO client secret already makes, and named
//     the same way in the operations document rather than quietly differing from it.
//
// 🔴 WHAT IS DELIBERATELY NOT HERE: an expiry-aware renewal for the broker's leaf
// certificate. Reuse keeps a value; renewal replaces one on a clock, and the two
// answer opposite questions — a leaf reused forever expires a year after bootstrap
// with nothing to re-issue it. That belongs to the verb that evolves a live instance,
// not to the one that creates it, and building half of it here would make the gap
// look closed.
func resolveCredentials(
	ctx context.Context,
	typed kubernetes.Interface,
	st *State,
	live liveArchiveState,
) (*credentialSet, error) {
	set, err := mintNewCredentials(st)
	if err != nil {
		return nil, err
	}

	// A dry run deploys nothing, so it has nothing to preserve and mints throwaway
	// values purely so the rendered plan is complete — the same reasoning, and the
	// same asymmetry, as the deployed-instance lookup in stepRenderConfig.
	if st.DryRun {
		return set, nil
	}

	for _, c := range []struct {
		into    *string
		ref     mintedCredentialRef
		cluster string
		exists  bool
	}{
		{&set.RDBPassword, mintedCredentialRef{
			infraNamespace, rdbClusterName + "-app-credentials", secretKeyPassword,
		}, rdbClusterName, live.Rdb.Exists},
		{&set.TSDBPassword, mintedCredentialRef{
			infraNamespace, tsdbClusterName + "-app-credentials", secretKeyPassword,
		}, tsdbClusterName, live.Tsdb.Exists},
	} {
		found, reused, err := reuseMintedCredential(ctx, typed, st.Instance, st.InstanceUID, c.ref)
		if err != nil {
			return nil, err
		}
		if err := refuseUnrecoverableDatabaseCredential(c.exists, found, c.cluster, c.ref.Name); err != nil {
			return nil, err
		}
		if found == reuseRecovered {
			*c.into = reused
		}
	}

	if databaseBackupsEnabled(st) {
		name := objectStoreName + "-credentials"
		foundUser, user, err := reuseMintedCredential(ctx, typed, st.Instance, st.InstanceUID,
			mintedCredentialRef{infraNamespace, name, keyMinioUser})
		if err != nil {
			return nil, err
		}
		foundPass, pass, err := reuseMintedCredential(ctx, typed, st.Instance, st.InstanceUID,
			mintedCredentialRef{infraNamespace, name, keyMinioPassword})
		if err != nil {
			return nil, err
		}
		// 🔴 BOTH HALVES OR NEITHER. These are one identity, and reusing the access
		// key while minting a new secret key produces a credential that has never
		// existed — which authenticates against nothing, on a run that reports
		// success. An object store holding one half and not the other is malformed,
		// and malformed is not absent.
		//
		// Keyed on the OUTCOMES rather than on the strings being non-empty: a Secret
		// that is present but not ours yields two empty strings, and treating that as
		// "nothing to reuse" is how the database guard came to report a live Secret as
		// gone. Here it means the same thing it means there — leave it for the writer
		// to refuse by name.
		switch {
		case foundUser == reuseRecovered && foundPass == reuseRecovered:
			set.ObjectStoreUser, set.ObjectStoreSecret = user, pass
		case foundUser == reuseRecovered || foundPass == reuseRecovered:
			return nil, fmt.Errorf("Secret %s/%s holds only one half of the object store's "+
				"root credential (%s recovered=%t, %s recovered=%t), so the identity it is "+
				"running under cannot be recovered. Minting a replacement would leave the "+
				"archiver presenting a credential the store has never been told about",
				infraNamespace, name,
				keyMinioUser, foundUser == reuseRecovered,
				keyMinioPassword, foundPass == reuseRecovered)
		}
	}

	return set, nil
}

// mintNewCredentials generates the entropy for everything in credentialSet.
//
// 🔴 EVERY FIELD HERE REPLACES A LITERAL COMMITTED TO THIS REPOSITORY. Until this
// runs, an instance's database password, object-store root credentials and dashboard
// login are the same value on every installation anyone has ever built. They are
// generated together, before anything is applied, so there is no ordering in which
// some of them are real and the rest are the default.
//
// Called only through resolveCredentials, which decides which of these values a live
// instance keeps. Kept separate so the entropy budget and the reuse policy are two
// readable decisions rather than one function doing both.
func mintNewCredentials(st *State) (*credentialSet, error) {
	var set credentialSet
	var err error

	if set.RDBPassword, err = mintPassword(); err != nil {
		return nil, err
	}
	if set.TSDBPassword, err = mintPassword(); err != nil {
		return nil, err
	}
	// 🔴 ONLY FOR AN IN-CLUSTER STORE. An external destination's credentials are
	// supplied, so minting here would generate entropy that lands in a Secret no
	// workload reads — and TestEveryMintedCredentialIsPlacedSomewhere would then be
	// satisfied by a placement that is itself pointless.
	if databaseBackupsEnabled(st) && !backupsAreExternal(st) {
		if set.ObjectStoreUser, err = mintPassword(); err != nil {
			return nil, err
		}
		if set.ObjectStoreSecret, err = mintObjectStoreSecret(); err != nil {
			return nil, err
		}
	}
	if monitoringEnabled(st) {
		if set.GrafanaAdminPassword, err = mintPassword(); err != nil {
			return nil, err
		}
	}
	return &set, nil
}
