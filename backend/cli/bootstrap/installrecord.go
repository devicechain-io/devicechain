// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The install record: what the cluster prerequisites were applied with, and what they
// built, kept IN the cluster.
//
// 🔴 IN THE CLUSTER, NOT UNDER ~/.devicechain. The prerequisite state is machine-local
// (no OpenTofu root has a backend), so a record there would make "this cluster is
// installed" true only on the machine that installed it — and a bootstrap from anywhere
// else would be refused for a cluster that is ready. The cluster is the one witness
// every machine can ask.
//
// 🔴 WRITTEN AS "applying" BEFORE THE APPLY AND "installed" ONLY AFTER IT SUCCEEDS. The
// alternatives each go green over a broken install. Nothing in the cluster before this
// meant "installed": the namespace is created before the apply, the claim is transient,
// and the presence of one Helm release says nothing about the others. A record written
// once, at the end, would still describe the PREVIOUS successful install while a later
// one sits half-applied; marking it first is what lets a failed re-install read as
// unfinished rather than as done.
//
// It holds no credential. The shared credentials are cluster-owned Secrets; this names
// where they are, never what they hold.
const (
	installRecordName = "dc-install"
	installRecordKey  = "install.json"
	// 3: outputs.rdb.maxConnections, the budget every instance is admitted against.
	installRecordSchema = 3

	installPhaseApplying  = "applying"
	installPhaseInstalled = "installed"
)

// InstallRecord is the document stored under installRecordKey.
type InstallRecord struct {
	Schema       int             `json:"schema"`
	Phase        string          `json:"phase"`
	ClusterUID   string          `json:"clusterUid"`
	DcctlVersion string          `json:"dcctlVersion,omitempty"`
	UpdatedAt    time.Time       `json:"updatedAt"`
	Settings     InstallSettings `json:"settings"`
	Outputs      InstallOutputs  `json:"outputs"`
}

// InstallSettings is what the cluster prerequisites were APPLIED WITH — the half of an
// instance's shape the cluster decides, which a bootstrap must follow rather than
// restate.
type InstallSettings struct {
	HA              bool `json:"ha"`
	Compact         bool `json:"compact"`
	Monitoring      bool `json:"monitoring"`
	CNPG            bool `json:"cnpg"`
	CertManager     bool `json:"certManager"`
	DatabaseBackups bool `json:"databaseBackups"`
	BackupsExternal bool `json:"backupsExternal"`
}

// InstallOutputs is what the cluster prerequisites BUILT, read back from the cluster
// root rather than derived from the settings above.
type InstallOutputs struct {
	// Rdb is the shared relational store: where it runs and which Secret holds the
	// identity that gives each instance a login and database of its own. Schema 2.
	Rdb                       ClusterRdb     `json:"rdb"`
	Archive                   InstallArchive `json:"archive"`
	BackupSurvivesClusterLoss bool           `json:"backupSurvivesClusterLoss"`
	CNPGNamespace             string         `json:"cnpgNamespace,omitempty"`
	GrafanaService            string         `json:"grafanaService,omitempty"`
	GrafanaNamespace          string         `json:"grafanaNamespace,omitempty"`
}

// InstallArchive is the archive contract an instance's event store is built against.
type InstallArchive struct {
	EndpointURL       string `json:"endpointUrl,omitempty"`
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	AccessKeyIDKey    string `json:"accessKeyIdKey,omitempty"`
	SecretAccessKey   string `json:"secretAccessKeyKey,omitempty"`
	BucketTsdb        string `json:"bucketTsdb,omitempty"`
}

// clusterArchive is the recorded archive contract in the shape an instance root is
// handed it.
func (a InstallArchive) clusterArchive() ClusterArchive {
	return ClusterArchive{
		EndpointURL:       a.EndpointURL,
		CredentialsSecret: a.CredentialsSecret,
		AccessKeyIDKey:    a.AccessKeyIDKey,
		SecretAccessKey:   a.SecretAccessKey,
		BucketTsdb:        a.BucketTsdb,
	}
}

// installSettingsFor is what this run applies the cluster root with. Every field comes
// from the predicate the OpenTofu variables are emitted from, never a second reading
// of the flags.
func installSettingsFor(st *State) InstallSettings {
	return InstallSettings{
		HA:              st.HA,
		Compact:         st.Compact,
		Monitoring:      monitoringEnabled(st),
		CNPG:            !st.NoCNPG,
		CertManager:     certManagerEnabled(st),
		DatabaseBackups: databaseBackupsEnabled(st),
		BackupsExternal: databaseBackupsEnabled(st) && backupsAreExternal(st),
	}
}

// installOutputsFrom collects what the cluster apply returned and recorded.
func installOutputsFrom(st *State, archive ClusterArchive, rdb ClusterRdb) InstallOutputs {
	offsite, _ := strconv.ParseBool(st.Values[databaseBackupOffsiteKey])
	return InstallOutputs{
		Rdb: rdb,
		Archive: InstallArchive{
			EndpointURL:       archive.EndpointURL,
			CredentialsSecret: archive.CredentialsSecret,
			AccessKeyIDKey:    archive.AccessKeyIDKey,
			SecretAccessKey:   archive.SecretAccessKey,
			BucketTsdb:        archive.BucketTsdb,
		},
		BackupSurvivesClusterLoss: offsite,
		CNPGNamespace:             st.Values[cnpgNamespaceKey],
		GrafanaService:            st.Values["grafanaService"],
		GrafanaNamespace:          st.Values["grafanaNamespace"],
	}
}

// markInstallApplying records that the cluster prerequisites are being applied, before
// anything is.
func markInstallApplying(ctx context.Context, typed kubernetes.Interface, clusterUID, dcctlVersion string,
	now func() time.Time) error {
	prev, err := getInstallRecordMap(ctx, typed)
	if err != nil {
		return err
	}
	if prev != nil {
		// 🔴 A NEWER dcctl'S RECORD IS NOT OURS TO REWRITE. Marking it would downgrade its
		// schema to this one's and discard whatever it recorded that this build cannot
		// name. A record that does not parse at all is overwritten: it describes nothing.
		var old InstallRecord
		if json.Unmarshal([]byte(prev.Data[installRecordKey]), &old) == nil && old.Schema > installRecordSchema {
			return fmt.Errorf("this cluster was installed by a newer dcctl (install record schema "+
				"%d; this build writes %d). Use that dcctl", old.Schema, installRecordSchema)
		}
	}
	// Nothing is carried from a previous record. Settings and outputs describe a
	// COMPLETED apply, and showing the last one's beside this run's version would tell
	// anyone reading it mid-apply that those are what is being applied.
	return putInstallRecord(ctx, typed, InstallRecord{
		Schema: installRecordSchema, Phase: installPhaseApplying,
		ClusterUID: clusterUID, DcctlVersion: dcctlVersion, UpdatedAt: now().UTC(),
	})
}

// writeInstalled records a completed install. Call it ONLY after the cluster apply
// succeeded and its outputs were read.
func writeInstalled(ctx context.Context, typed kubernetes.Interface, rec InstallRecord, now func() time.Time) error {
	rec.Schema, rec.Phase, rec.UpdatedAt = installRecordSchema, installPhaseInstalled, now().UTC()
	if err := rec.validate(rec.ClusterUID); err != nil {
		// Refusing to write a record the reader would refuse keeps the two from
		// disagreeing about what "installed" means.
		return fmt.Errorf("refusing to record an install the next bootstrap could not use: %w", err)
	}
	return putInstallRecord(ctx, typed, rec)
}

// ErrNotInstalled is returned when a cluster has no usable install record.
var ErrNotInstalled = errors.New("the cluster prerequisites are not installed")

// ErrInstallRecordSchema is a record written by a dcctl that reads a different schema.
var ErrInstallRecordSchema = errors.New("the install record is from a different dcctl")

// readInstallRecord returns this cluster's install record, or an error saying exactly
// why there is not a usable one.
//
// 🔴 EVERY FAILURE IS A REFUSAL, AND NONE IS AN EMPTY RECORD. A zero-valued record reads
// as "no HA, no monitoring, no backups", and an instance built to that describes a
// cluster nobody installed — archiving off, alerts unrendered — on a run that reports
// success.
func readInstallRecord(ctx context.Context, typed kubernetes.Interface, liveClusterUID string) (*InstallRecord, error) {
	cm, err := getInstallRecordMap(ctx, typed)
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, fmt.Errorf("%w: there is no install record (ConfigMap %s/%s)",
			ErrNotInstalled, infraNamespace, installRecordName)
	}
	if cm.GetAnnotations()[annotationManagedBy] != managedByDcctl {
		return nil, fmt.Errorf("ConfigMap %s/%s was not written by dcctl, so it cannot be "+
			"trusted to describe what is installed on this cluster", infraNamespace, installRecordName)
	}
	raw, ok := cm.Data[installRecordKey]
	if !ok {
		return nil, fmt.Errorf("%w: the install record has no %q entry", ErrNotInstalled, installRecordKey)
	}
	var rec InstallRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("the install record cannot be read: %w", err)
	}
	if err := rec.validate(liveClusterUID); err != nil {
		return nil, err
	}
	return &rec, nil
}

// validate is the one definition of a usable record, shared by the writer and the
// reader.
func (r InstallRecord) validate(liveClusterUID string) error {
	switch {
	case r.Schema != installRecordSchema:
		// Newer or older than this dcctl knows. Guessing at the fields would read a
		// record whose meaning changed as if it had not.
		return fmt.Errorf("%w: the install record is schema %d and this dcctl reads schema %d; "+
			"use the dcctl that installed this cluster", ErrInstallRecordSchema, r.Schema, installRecordSchema)
	case r.Phase == installPhaseApplying:
		return fmt.Errorf("%w: an install of this cluster started and did not finish, so the "+
			"prerequisites may be half-applied. Re-run the install", ErrNotInstalled)
	case r.Phase != installPhaseInstalled:
		return fmt.Errorf("%w: the install record's phase is %q", ErrNotInstalled, r.Phase)
	case liveClusterUID == "":
		return fmt.Errorf("cannot check the install record: this cluster's identity is not known")
	case r.ClusterUID != liveClusterUID:
		// A record carried in from elsewhere — a restored etcd snapshot, a copied
		// namespace — describes prerequisites this cluster may not have.
		return fmt.Errorf("the install record belongs to cluster %s, not this one (%s); it was "+
			"carried here from elsewhere and does not describe what is installed",
			r.ClusterUID, liveClusterUID)
	}

	// What the settings promise, the outputs must deliver. Each missing value is one an
	// instance would silently build without.
	// The relational store is not optional, so neither is knowing where it is: without
	// it no instance can be given a login, and none can be destroyed cleanly.
	if d := r.Outputs.Rdb; d.Namespace == "" || d.ClusterName == "" || d.ProvisionerSecret == "" || d.MaxConnections <= 0 {
		return fmt.Errorf("the install record does not say where the relational store is, which "+
			"Secret holds its provisioner, or what connection budget it has (%+v); no instance could "+
			"be given a database login", d)
	}
	a := r.Outputs.Archive
	if r.Settings.DatabaseBackups && (a.EndpointURL == "" || a.CredentialsSecret == "" ||
		a.AccessKeyIDKey == "" || a.SecretAccessKey == "" || a.BucketTsdb == "") {
		return fmt.Errorf("the install record says backups are on but its archive contract is "+
			"incomplete (%+v); an instance built from it would archive nowhere", a)
	}
	if r.Settings.CNPG && r.Outputs.CNPGNamespace == "" {
		return fmt.Errorf("the install record says the database operator is installed but does " +
			"not say where; an instance built from it would render no operator monitoring")
	}
	if r.Settings.Monitoring && (r.Outputs.GrafanaService == "" || r.Outputs.GrafanaNamespace == "") {
		return fmt.Errorf("the install record says monitoring is installed but does not name " +
			"its dashboard service")
	}
	return nil
}

func getInstallRecordMap(ctx context.Context, typed kubernetes.Interface) (*corev1.ConfigMap, error) {
	cm, err := typed.CoreV1().ConfigMaps(infraNamespace).Get(ctx, installRecordName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		// "Could not tell" is never "not installed": that answer sends an operator to
		// re-install a cluster that may be serving instances.
		return nil, fmt.Errorf("reading the install record %s/%s: %w", infraNamespace, installRecordName, err)
	}
	return cm, nil
}

func putInstallRecord(ctx context.Context, typed kubernetes.Interface, rec InstallRecord) error {
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	api := typed.CoreV1().ConfigMaps(infraNamespace)
	existing, err := getInstallRecordMap(ctx, typed)
	if err != nil {
		return err
	}
	if existing == nil {
		_, err := api.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: installRecordName, Namespace: infraNamespace,
				Annotations: map[string]string{annotationManagedBy: managedByDcctl},
			},
			Data: map[string]string{installRecordKey: string(body)},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("writing the install record: %w", err)
		}
		return nil
	}
	if existing.GetAnnotations()[annotationManagedBy] != managedByDcctl {
		return fmt.Errorf("refusing to overwrite ConfigMap %s/%s: dcctl did not write it",
			infraNamespace, installRecordName)
	}
	updated := existing.DeepCopy()
	updated.Data = map[string]string{installRecordKey: string(body)}
	if _, err := api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("writing the install record: %w", err)
	}
	return nil
}
