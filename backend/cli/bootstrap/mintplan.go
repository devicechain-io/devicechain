// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	corev1 "k8s.io/api/core/v1"
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

	if databaseBackupsEnabled(st) {
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

// mintNewCredentials generates the entropy for everything in credentialSet.
//
// 🔴 EVERY FIELD HERE REPLACES A LITERAL COMMITTED TO THIS REPOSITORY. Until this
// runs, an instance's database password, object-store root credentials and dashboard
// login are the same value on every installation anyone has ever built. They are
// generated together, before anything is applied, so there is no ordering in which
// some of them are real and the rest are the default.
func mintNewCredentials(st *State) (*credentialSet, error) {
	var set credentialSet
	var err error

	if set.RDBPassword, err = mintPassword(); err != nil {
		return nil, err
	}
	if set.TSDBPassword, err = mintPassword(); err != nil {
		return nil, err
	}
	if databaseBackupsEnabled(st) {
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
