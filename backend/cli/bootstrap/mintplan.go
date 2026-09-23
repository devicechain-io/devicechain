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

	// The relational store's roles, restated for the same reason and held against
	// deploy/opentofu/cluster/variables.tf by TestTheRelationalRoleNamesMatchTheClusterRoot.
	//
	// 🔴 BOTH CARRY AN UNDERSCORE ON PURPOSE. Every instance gets a login and a
	// database named after its id, and an id is a DNS-1123 label, which cannot hold
	// one — so no instance can ever be named into either of these.
	rdbOwnerUsername       = "dc_owner"
	rdbProvisionerUsername = "dc_provisioner"

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
	RDBPassword string
	// RDBProvisionerPassword is the base identity's: the cluster's one role that may
	// create logins and databases. dcctl sets the role's password from its Secret.
	RDBProvisionerPassword string
	// RDBInstancePassword is this instance's own login on the relational store —
	// the one its services connect as, owning the one database they use.
	RDBInstancePassword  string
	TSDBPassword         string
	ObjectStoreUser      string
	ObjectStoreSecret    string
	GrafanaAdminPassword string
	// SuperuserPassword is the password user-management seeds the instance's global
	// superuser with, on its first start against an empty identity table. It replaced
	// a literal every earlier release seeded and published. See superuser.go.
	SuperuserPassword string
}

// databaseBackupsEnabled reports whether this run provisions a backup destination.
//
// 🔴 ONE DEFINITION, READ BY EVERYTHING THAT NEEDS IT. infraVars emits
// `enable_database_backups` from this predicate, the mint decides the object-store
// credential from it, and the install record stores it — so a second reading of the
// flags anywhere would be free to disagree with the store that exists, minting a
// credential nothing reads, or not minting one the store needs.
// TestTheBackupPredicateMatchesTheVariablesEmitted holds the emission to it.
//
// 🔑 A BOOTSTRAP ASKS THE INSTALL, NOT ITS OWN FLAGS. Backups, monitoring and
// cert-manager are what the cluster was installed with; an instance built on it
// follows the record, and reading its own flags instead would let `--no-tls` on one
// instance switch archiving off for a cluster whose store is archiving.
func databaseBackupsEnabled(st *State) bool {
	if st.Install != nil {
		return st.Install.Settings.DatabaseBackups
	}
	return DatabaseBackupsEnabled(st.NoCNPG, st.Compact, st.NoTLS)
}

// monitoringEnabled reports whether the observability stack is part of this run, and
// therefore whether a dashboard credential is needed at all.
func monitoringEnabled(st *State) bool {
	if st.Install != nil {
		return st.Install.Settings.Monitoring
	}
	return !st.NoMonitoring
}

// certManagerEnabled reports whether cert-manager is part of this run. The compact
// preset drops it only when it also serves plain HTTP — see infraVars, which
// TestTheBackupPredicateMatchesTheVariablesEmitted holds this against.
func certManagerEnabled(st *State) bool {
	if st.Install != nil {
		return st.Install.Settings.CertManager
	}
	return !(st.Compact && st.NoTLS)
}

// backupsAreExternal reports whether this run archives to an object store the
// operator already owns rather than one it stands up.
//
// 🔴 THE TWO DESTINATIONS ARE MUTUALLY EXCLUSIVE, AND GETTING THAT WRONG WRITES A
// CREDENTIAL NOTHING READS. An external destination provisions no object store, so
// minting root credentials for one produces `dc-object-store-credentials` with no
// MinIO to authenticate against — while `dc-backup-credentials`, the Secret the
// archiver actually presents, goes unwritten. Measured as a real gap in this package
// before the supplied path existed: dcctl knew only "backups on or off".
func backupsAreExternal(st *State) bool {
	if st.Install != nil {
		return st.Install.Settings.BackupsExternal
	}
	return st.BackupDestination.Configured()
}

// plansCluster and plansInstance say which half of the credentials a run owns. The
// install owns the cluster's and has no instance; a bootstrap owns its instance's and
// follows an install; an upgrade names an instance and reads both halves back.
func plansCluster(st *State) bool  { return st.Install == nil }
func plansInstance(st *State) bool { return st.Instance != "" }

// rdbProvisionerSecretName is the base identity's Secret. Only dcctl reads it: dcctl
// creates the role and sets its password from it (see withProvisionerSession).
const rdbProvisionerSecretName = rdbClusterName + "-provisioner-credentials"

// instanceRdbSecretName is where an instance's own relational login is kept.
func instanceRdbSecretName(instance string) string {
	return "dci-" + instance + "-rdb-credentials"
}

// dbLabels are the labels the database operator keys a credentials Secret on.
func dbLabels(cluster string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      cluster,
		"app.kubernetes.io/component": "database",
		cnpgReloadLabel:               "true",
	}
}

// planClusterSecrets is the install's half: the credentials the shared prerequisites
// are built from, all cluster-owned. It also returns the archive credential, when
// backups are on, for an instance's half to copy.
func planClusterSecrets(st *State, set *credentialSet) (_ []ownedSecret, archive *ownedSecret) {
	// 🔴 Neither store's credential is gated on the flag that skips the operator
	// install — that flag means "an operator is already here", and gating the stores on
	// it would turn that into "this platform has no database".
	out := []ownedSecret{
		{
			Name:      rdbClusterName + "-app-credentials",
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeBasicAuth,
			Labels:    dbLabels(rdbClusterName),
			// 🔴 THE CLUSTER'S. The relational store is a cluster prerequisite, created
			// by the cluster root and shared by every instance on the cluster, and
			// CloudNativePG reads this Secret once — when it creates that store.
			Scope: ownerCluster,
			Data: map[string]string{
				secretKeyUsername: rdbOwnerUsername,
				secretKeyPassword: set.RDBPassword,
			},
		},
		{
			Name:      rdbProvisionerSecretName,
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeBasicAuth,
			// No database operator reads it — dcctl creates the role and sets its
			// password from this Secret — so it carries no reload label.
			Labels: map[string]string{
				"app.kubernetes.io/name":      rdbClusterName,
				"app.kubernetes.io/component": "database",
			},
			Scope: ownerCluster,
			Data: map[string]string{
				secretKeyUsername: rdbProvisionerUsername,
				secretKeyPassword: set.RDBProvisionerPassword,
			},
		},
	}

	if monitoringEnabled(st) {
		// 🔴 THIS WAS MINTED AND PLACED NOWHERE. mintNewCredentials generated a
		// dashboard password whenever monitoring was on, and no Secret in this plan
		// ever carried it — a credential produced on every run and dropped on the
		// floor. What catches it now is TestEveryMintedCredentialIsPlacedSomewhere,
		// which walks the struct rather than the cases anybody thought to list.
		out = append(out, ownedSecret{
			Name:      grafanaSecretName,
			Namespace: monitoringNamespace,
			Type:      corev1.SecretTypeOpaque,
			// The monitoring stack is installed once per cluster.
			Scope: ownerCluster,
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

	switch {
	case !databaseBackupsEnabled(st):
	case backupsAreExternal(st):
		// Supplied, not minted — see BackupDestination. It is written through the same
		// writer as everything else, because ownership is about who writes the object.
		a := backupCredentialsSecret(st.BackupDestination)
		archive = &a
	default:
		archive = &ownedSecret{
			Name:      objectStoreName + "-credentials",
			Namespace: infraNamespace,
			Type:      corev1.SecretTypeOpaque,
			// The object store is a cluster prerequisite; both stores archive into it.
			Scope: ownerCluster,
			Labels: map[string]string{
				"app.kubernetes.io/name":      objectStoreName,
				"app.kubernetes.io/component": "object-store",
			},
			Data: map[string]string{
				keyMinioUser:     set.ObjectStoreUser,
				keyMinioPassword: set.ObjectStoreSecret,
			},
		}
	}
	if archive != nil {
		out = append(out, *archive)
	}
	return out, archive
}

// planInstanceSecrets is a bootstrap's half: the instance's own login, its event store's
// credentials, and — given the cluster's archive credential — the instance's copy of it.
func planInstanceSecrets(st *State, set *credentialSet, archive *ownedSecret) []ownedSecret {
	out := []ownedSecret{
		{
			// This instance's own login. No database operator reads it — dcctl sets
			// the role's password from it — so it carries no reload label.
			Name:      instanceRdbSecretName(st.Instance),
			Namespace: InstanceNamespace(st.Instance),
			Type:      corev1.SecretTypeBasicAuth,
			Labels: map[string]string{
				"app.kubernetes.io/component": "database",
			},
			Data: map[string]string{
				secretKeyUsername: st.Instance,
				secretKeyPassword: set.RDBInstancePassword,
			},
		},
		{
			// The event store is the instance's, in the instance's namespace, and
			// CloudNativePG reads a Cluster's credentials from its own namespace.
			Name:      tsdbClusterName + "-app-credentials",
			Namespace: InstanceNamespace(st.Instance),
			Type:      corev1.SecretTypeBasicAuth,
			Labels:    dbLabels(tsdbClusterName),
			Data: map[string]string{
				secretKeyUsername: dbRoleUsername,
				secretKeyPassword: set.TSDBPassword,
			},
		},
	}
	// Not written for a live instance that never had one: its superuser was seeded
	// before dcctl generated the value, and a Secret here would name a password it was
	// never given (resolveCredentials).
	if st.SuperuserSeed != superuserSeedAbsent {
		out = append(out, superuserSecret(st, set))
	}
	if archive != nil {
		out = append(out, instanceArchiveCredential(st, *archive))
	}
	return out
}

// instanceArchiveCredential is the instance's own copy of the cluster's archive
// credential, in the instance's namespace.
//
// 🔴 A COPY, BECAUSE THE ARCHIVER CANNOT READ ACROSS NAMESPACES. The event store's
// backup object names its credentials Secret by name alone, and resolves it in the
// namespace the event store runs in — the instance's. The destination is the cluster's
// and so is the credential; this is where the instance's archiver can reach it. Same
// name and same keys, so the archive contract read back from the cluster root describes
// the copy as exactly as it describes the original.
//
// 🔴 WRITTEN WHEN THE INSTANCE IS BUILT, AND NOT AGAIN. Credentials are written by the
// bootstrap, and an upgrade writes none; so a cluster archive credential rotated later
// does not reach an existing instance's copy on its own — the copy has to be rewritten
// with it.
func instanceArchiveCredential(st *State, cluster ownedSecret) ownedSecret {
	copied := cluster
	copied.Namespace = InstanceNamespace(st.Instance)
	copied.Scope = ownerInstance
	copied.Data = make(map[string]string, len(cluster.Data))
	for k, v := range cluster.Data {
		copied.Data[k] = v
	}
	return copied
}

// resolveCredentials settles every credential this run needs: REUSED where the
// instance already has one and a fresh value would be destructive, MINTED otherwise.
//
// 🔴 REUSE IS NOT UNIFORM, AND TREATING IT AS ONE RULE IS HOW THE BROKER'S
// CERTIFICATE EXPIRES. Two of these credentials cannot be re-minted under a running
// instance without breaking it, and the rest can. The split is stated here, once,
// rather than being a property of whichever call site was written last:
//
//   - THE DATABASE OWNER PASSWORDS ARE REUSED, ALWAYS. CloudNativePG sets the owner
//     role's password when it CREATES the Cluster and never again — the role is
//     declared under `managed.roles` with no `passwordSecret`, so nothing reconciles
//     it. A fresh value would reach every service and none of the two databases.
//   - THE PROVISIONER'S AND THE INSTANCE LOGIN'S PASSWORDS ARE REUSED WHEN PRESENT,
//     AND MINTED WHEN ABSENT — not refused. Both are recoverable: dcctl sets both
//     roles' passwords from their Secrets on every run. Refusing here would strand
//     a cluster over a Secret that a fresh value repairs.
//   - THE OBJECT-STORE CREDENTIALS ARE REUSED. Re-minting them is recoverable, but
//     not cheaply: the store reads its root credentials at start-up while the backup
//     archiver is handed them through a different object on a different schedule, so
//     a new value opens a window in which the archiver cannot authenticate and the
//     first visible symptom is that WAL stopped being shipped.
//   - THE SUPERUSER'S SEED PASSWORD IS REUSED WHEN PRESENT, AND MINTED WHEN ABSENT.
//     user-management reads it once, to seed an empty identity table, so a re-run
//     that minted over it would leave the Secret naming a password the superuser was
//     never given. A bootstrap re-run happens only before the instance's configuration
//     document exists (stepRefuseRebuild), but the Secret is written earlier than that,
//     and a value the report has not shown yet is still one to keep. The exception is
//     a carve-out re-run over a LIVE instance that has no such Secret: nothing is
//     minted for it at all (see the settlement below the loops).
//   - THE DASHBOARD PASSWORD IS REUSED WHEN PRESENT, AND MINTED WHEN ABSENT. A fresh
//     value is not a rotation: Grafana reads it through admin.existingSecret as an
//     environment variable, a changed Secret restarts nothing, and a Grafana whose
//     database persists ignores a changed admin password after its first start
//     anyway. So a new value lands in the Secret and nowhere else, and the Secret
//     then names a password Grafana has never seen. To rotate it deliberately, delete
//     Secret monitoring/dc-grafana-admin, re-run `dcctl install`, then restart
//     Deployment monitoring/kube-prometheus-stack-grafana — which holds only while
//     Grafana's persistence stays off, as the monitoring module leaves it at the
//     chart's default; with a persistent database it would also need
//     `grafana cli admin reset-admin-password`.
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

	type databaseCredential struct {
		into    *string
		ref     mintedCredentialRef
		scope   ownerKind
		cluster string
		exists  bool
	}
	type loginCredential struct {
		into  *string
		ref   mintedCredentialRef
		scope ownerKind
		// recovered, when set, is told whether the value was read back rather than
		// minted by this run.
		recovered *bool
	}
	var databases []databaseCredential
	var logins []loginCredential
	var superuserRecovered bool
	if plansCluster(st) {
		databases = append(databases, databaseCredential{&set.RDBPassword, mintedCredentialRef{
			infraNamespace, rdbClusterName + "-app-credentials", secretKeyPassword,
		}, ownerCluster, rdbClusterName, live.Rdb.Exists})
		logins = append(logins, loginCredential{&set.RDBProvisionerPassword, mintedCredentialRef{
			infraNamespace, rdbProvisionerSecretName, secretKeyPassword,
		}, ownerCluster, nil})
		// Gated exactly as it is minted and placed: with monitoring off there is no
		// Secret to read, and a cluster that turns it on later has none yet, so it mints.
		if monitoringEnabled(st) {
			logins = append(logins, loginCredential{&set.GrafanaAdminPassword, mintedCredentialRef{
				monitoringNamespace, grafanaSecretName, keyGrafanaAdminPass,
			}, ownerCluster, nil})
		}
	}
	if plansInstance(st) {
		databases = append(databases, databaseCredential{&set.TSDBPassword, mintedCredentialRef{
			InstanceNamespace(st.Instance), tsdbClusterName + "-app-credentials", secretKeyPassword,
		}, ownerInstance, tsdbClusterName, live.Tsdb.Exists})
		logins = append(logins, loginCredential{&set.RDBInstancePassword, mintedCredentialRef{
			InstanceNamespace(st.Instance), instanceRdbSecretName(st.Instance), secretKeyPassword,
		}, ownerInstance, nil})
		logins = append(logins, loginCredential{&set.SuperuserPassword, superuserSecretRef(st.Instance),
			ownerInstance, &superuserRecovered})
	}

	for _, c := range databases {
		found, reused, err := reuseMintedCredential(ctx, typed, ownerFor(c.scope, st), c.ref)
		if err != nil {
			return nil, err
		}
		if err := refuseUnrecoverableDatabaseCredential(c.exists, found, c.cluster, c.ref); err != nil {
			return nil, err
		}
		if found == reuseRecovered {
			*c.into = reused
		}
	}

	for _, c := range logins {
		// reuseForeign keeps the minted value: the writer refuses that Secret by name.
		found, reused, err := reuseMintedCredential(ctx, typed, ownerFor(c.scope, st), c.ref)
		if err != nil {
			return nil, err
		}
		if found == reuseRecovered {
			*c.into = reused
		}
		if c.recovered != nil {
			*c.recovered = found == reuseRecovered
		}
	}
	if plansInstance(st) {
		switch {
		case superuserRecovered:
			st.SuperuserSeed = superuserSeedRecovered
		case st.OverLiveInstance:
			// 🔴 A LIVE INSTANCE WITH NO SECRET IS ONE BUILT BEFORE dcctl GENERATED IT, and
			// its identity table was seeded with the literal those releases published. A
			// carve-out re-run (a restore, --allow-legacy-db-removal) reaches here over
			// exactly that instance — and a value minted now would seed nothing, yet be
			// written to the Secret, printed as the superuser's password, and trusted by
			// every tool that reads it. The upgrade's answer, for the upgrade's reason:
			// nothing minted, nothing written, and the report says what that means.
			set.SuperuserPassword = ""
			st.SuperuserSeed = superuserSeedAbsent
		default:
			st.SuperuserSeed = superuserSeedMinted
		}
	}

	if plansCluster(st) && databaseBackupsEnabled(st) && !backupsAreExternal(st) {
		name := objectStoreName + "-credentials"
		foundUser, user, err := reuseMintedCredential(ctx, typed, ownerFor(ownerCluster, st),
			mintedCredentialRef{infraNamespace, name, keyMinioUser})
		if err != nil {
			return nil, err
		}
		foundPass, pass, err := reuseMintedCredential(ctx, typed, ownerFor(ownerCluster, st),
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

	if plansInstance(st) {
		if set.RDBInstancePassword, err = mintPassword(); err != nil {
			return nil, err
		}
		if set.TSDBPassword, err = mintPassword(); err != nil {
			return nil, err
		}
		if set.SuperuserPassword, err = mintPassword(); err != nil {
			return nil, err
		}
	}
	if !plansCluster(st) {
		return &set, nil
	}
	if set.RDBPassword, err = mintPassword(); err != nil {
		return nil, err
	}
	if set.RDBProvisionerPassword, err = mintPassword(); err != nil {
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
