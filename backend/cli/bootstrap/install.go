// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/devicechain-io/dcctl/dcdir"
	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"
)

// DefaultClusterName is the local cluster `dcctl install local` creates, and the one
// `dcctl bootstrap local` builds on, when neither is told another.
const DefaultClusterName = "devicechain"

// defaultMaxConnections is the relational store's connection budget on a first install
// that does not ask for one. See postgres_max_connections in the cluster root.
const defaultMaxConnections = 600

// InstallOptions drives `dcctl install`.
type InstallOptions struct {
	Options
	// BackupDestination is an off-site archive the operator already owns. Nil means
	// the in-cluster object store.
	BackupDestination *BackupDestination
	// MaxConnections is the relational store's connection budget. Zero keeps what the
	// cluster was installed with, or the default on a first install.
	MaxConnections int
	DcctlVersion   string
}

// Install prepares a cluster for instances: it creates or names the cluster, applies
// the cluster prerequisites, makes the base database identity, and records the install.
//
// 🔴 THE RECORD IS WRITTEN LAST, AND ONLY ON SUCCESS. `dcctl bootstrap` refuses a
// cluster without an `installed` record and builds every instance from what it says,
// so a record that claimed an install which did not finish would put instances on
// prerequisites that are not there. markInstallApplying brackets the apply for exactly
// that reason.
func Install(ctx context.Context, provider Provider, opts InstallOptions) error {
	opts.CreateCluster = true
	binding, err := provider.EnsureCluster(ctx, opts.Options)
	if err != nil {
		return err
	}

	st := &State{
		KubeContext:          binding.KubeContext,
		Binding:              binding,
		Provider:             provider.Name(),
		DcctlVersion:         opts.DcctlVersion,
		DryRun:               opts.DryRun,
		AssumeYes:            opts.AssumeYes,
		NoTLS:                opts.NoTLS,
		NoMonitoring:         opts.NoMonitoring,
		NoCNPG:               opts.NoCNPG,
		AllowLegacyDbRemoval: opts.AllowLegacyDbRemoval,
		Compact:              opts.Compact,
		HA:                   opts.HA,
		BackupDestination:    opts.BackupDestination,
		MaxConnections:       opts.MaxConnections,
		Values:               map[string]string{},
	}
	settings := installSettingsFor(st)

	fmt.Println(GreenUnderline(fmt.Sprintf("\nInstall DeviceChain prerequisites on cluster %s", binding.Describe())))
	fmt.Printf("  %s %s\n", color.WhiteString("Settings:"), color.GreenString(describeInstallSettings(settings)))

	if err := checkHaNodeCapacity(ctx, st); err != nil {
		return err
	}
	if st.DryRun {
		wouldDo("tofu init+apply deploy/opentofu/cluster — once per cluster, shared by every instance " +
			"(CloudNativePG operator + backup plugin, ingress, cert-manager, monitoring, the relational " +
			"store, the backup object store)")
		wouldDo("create the base database identity instances' logins are made with")
		wouldDo(fmt.Sprintf("record the install in ConfigMap %s/%s", infraNamespace, installRecordName))
		return nil
	}

	uid, err := IdentifyCluster(ctx, binding.KubeContext)
	if err != nil {
		return fmt.Errorf("reading the identity of cluster %s: %w\n"+
			"  dcctl files this cluster's prerequisite state under that identity, so it cannot "+
			"install them without it", binding.Describe(), err)
	}
	st.ClusterUID, st.Binding.ClusterUID = uid, uid
	if err := WriteClusterRecord(ClusterRecord{
		UID: uid, Cluster: binding.Cluster, KubeContext: binding.KubeContext, Managed: binding.Managed,
		FirstSeenAt: time.Now().UTC(), DcctlVersion: opts.DcctlVersion,
	}); err != nil {
		fmt.Println(color.YellowString("warning: could not record what is known about cluster %s (%v).",
			binding.Describe(), err))
	}

	dyn, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to cluster %s: %w", binding.Describe(), err)
	}
	prev, err := previousInstall(ctx, typed)
	if err != nil {
		return err
	}
	if st.MaxConnections == 0 {
		st.MaxConnections = defaultMaxConnections
		if last := prev.lastCompleted(); last != nil && last.Outputs.Rdb.MaxConnections > 0 {
			st.MaxConnections = last.Outputs.Rdb.MaxConnections
		}
	}
	if err := refuseAReinstallThatWouldHurt(ctx, st, prev, settings, localClusterStateExists); err != nil {
		return err
	}

	doing("settling the cluster's credentials")
	live, err := clusterArchivePath(ctx, dyn, infraNamespace, RdbClusterName)
	if err != nil {
		return fail("reading the relational store's archive state", err)
	}
	// The path the relational store archives under is permanent once it is archiving;
	// see resolveArchivePaths.
	if live.Exists {
		st.Values["backupServerNameRdb"] = live.Path
	}
	if st.Credentials, err = resolveCredentials(ctx, typed, st, liveArchiveState{Rdb: live}); err != nil {
		return fail("settling the cluster's credentials", err)
	}
	done()

	clusterVars, _, err := splitVars(infraVars(st))
	if err != nil {
		return err
	}
	if err := checkRelationalStoreOwner(ctx, st.KubeContext); err != nil {
		return err
	}
	if err := ensureInfraNamespace(ctx, typed, infraNamespace); err != nil {
		return err
	}
	if err := writeClusterSecrets(ctx, typed, st); err != nil {
		return err
	}
	if err := markInstallApplying(ctx, typed, st.ClusterUID, st.DcctlVersion, time.Now); err != nil {
		return err
	}
	var archive ClusterArchive
	var rdb ClusterRdb
	if err := runStreamed("applying cluster prerequisites (OpenTofu)", "cluster prerequisites", func() error {
		archive, rdb, err = applyClusterPrereqs(ctx, st, st.ClusterUID, clusterVars, infraNamespace)
		return err
	}); err != nil {
		return err
	}

	// 🔴 THE BASE IDENTITY BEFORE THE RECORD. Every instance's login is created as it, so
	// a cluster recorded as installed without it would refuse the first bootstrap.
	doing("creating the base database identity")
	if err := withProvisionerSession(ctx, st.KubeContext, rdb, func(instanceDBQuerier) error { return nil }); err != nil {
		return fail("creating the base database identity", err)
	}
	done()

	if err := writeInstalled(ctx, typed, InstallRecord{
		ClusterUID:   st.ClusterUID,
		DcctlVersion: st.DcctlVersion,
		Settings:     settings,
		Outputs:      installOutputsFrom(st, archive, rdb),
	}, time.Now); err != nil {
		return err
	}
	reportInstall(st, provider.Name())
	return nil
}

// previousInstall reads whatever install record the cluster already has, parsed but not
// validated: a re-install is asking what was there, including a half-finished one.
func previousInstall(ctx context.Context, typed kubernetes.Interface) (*InstallRecord, error) {
	cm, err := getInstallRecordMap(ctx, typed)
	if err != nil || cm == nil {
		return nil, err
	}
	var rec InstallRecord
	if json.Unmarshal([]byte(cm.Data[installRecordKey]), &rec) != nil {
		// A record that does not parse describes nothing; markInstallApplying replaces it.
		return nil, nil
	}
	return &rec, nil
}

// localClusterStateExists reports whether this machine holds the cluster root's state
// for the cluster.
func localClusterStateExists(uid string) (bool, error) {
	dir, err := dcdir.Cluster(uid)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, prereqStateSubdir, assets.ClusterRootDir, "terraform.tfstate"))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// refuseAReinstallThatWouldHurt stops a re-install that would damage what the cluster
// already runs.
//
// Two ways, and both are green runs without this:
//
//   - 🔴 AN INSTALL FROM A MACHINE THAT DID NOT DO IT. The cluster root's state lives
//     on the machine that applied it. Another machine starts from empty state, plans
//     every prerequisite as new, and dies on "cannot re-use a name that is still in
//     use" part-way through — after marking the record as applying, which is what
//     every bootstrap then refuses.
//   - 🔴 NEW SETTINGS UNDER RUNNING INSTANCES. Every instance was built to the settings
//     it found — its HA, its sizing, whether it archives — and none of them is
//     re-applied when the cluster's change. Turning backups off or HA down underneath
//     them leaves instances that believe in a cluster that no longer exists. Raising
//     the connection budget is the one change that cannot hurt them, so it is allowed.
func refuseAReinstallThatWouldHurt(ctx context.Context, st *State, prev *InstallRecord, settings InstallSettings,
	stateExists func(uid string) (bool, error)) error {
	last := prev.lastCompleted()
	if last == nil {
		return nil
	}
	exists, err := stateExists(st.ClusterUID)
	if err != nil {
		return fmt.Errorf("checking this machine for the cluster's prerequisite state: %w", err)
	}
	if !exists {
		return fmt.Errorf("cluster %s is already installed (by dcctl %s, %s), but this machine holds no "+
			"state for it under ~/.devicechain/clusters/%s. It was installed from another machine, and "+
			"re-applying from empty state would try to create every prerequisite again. Run "+
			"`dcctl install` from the machine that installed it",
			st.Binding.Describe(), last.DcctlVersion, last.UpdatedAt.Format(time.RFC3339), st.ClusterUID)
	}

	var changed []string
	if last.Settings != settings {
		changed = append(changed, fmt.Sprintf("settings %s → %s",
			describeInstallSettings(last.Settings), describeInstallSettings(settings)))
	}
	if st.MaxConnections < last.Outputs.Rdb.MaxConnections {
		changed = append(changed, fmt.Sprintf("connection budget %d → %d",
			last.Outputs.Rdb.MaxConnections, st.MaxConnections))
	}
	// An off-site destination is more than a boolean: every instance's event store
	// archives to the endpoint and bucket it was built with, and none is re-pointed.
	if d, a := st.BackupDestination, last.Outputs.Archive; last.Settings.BackupsExternal && d.Configured() &&
		(d.EndpointURL != a.EndpointURL || d.BucketTsdb != a.BucketTsdb) {
		changed = append(changed, fmt.Sprintf("off-site archive %s/%s → %s/%s",
			a.EndpointURL, a.BucketTsdb, d.EndpointURL, d.BucketTsdb))
	}
	if len(changed) == 0 {
		return nil
	}
	held, err := readClusterInstances(ctx, st.KubeContext)
	if err != nil {
		return fmt.Errorf("this install changes the cluster (%s), and dcctl cannot tell whether any "+
			"instance runs on it to be hurt by that: %w", strings.Join(changed, "; "), err)
	}
	if len(held.IDs) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to change cluster %s (%s) while instance(s) %s run on it: each was "+
		"built to the settings it found, and none is rebuilt when they change. Re-run `dcctl install` "+
		"with the settings it was installed with, or destroy those instances first",
		st.Binding.Describe(), strings.Join(changed, "; "), strings.Join(held.IDs, ", "))
}

// describeInstallSettings renders settings as the flags that produce them.
func describeInstallSettings(s InstallSettings) string {
	parts := []string{}
	if s.HA {
		parts = append(parts, "ha")
	}
	if s.Compact {
		parts = append(parts, "compact")
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{s.Monitoring, "monitoring"},
		{s.CNPG, "cloudnative-pg"},
		{s.CertManager, "cert-manager"},
		{s.DatabaseBackups, "backups"},
	} {
		if f.on {
			parts = append(parts, f.name)
		} else {
			parts = append(parts, "no "+f.name)
		}
	}
	if s.BackupsExternal {
		parts = append(parts, "off-site archive")
	}
	return strings.Join(parts, ", ")
}

// FollowInstall shapes a bootstrap from the cluster's install record: the half of an
// instance the cluster decides, and what the cluster apply built.
//
// 🔴 EVERY FIELD IS OVERWRITTEN, NONE MERGED. A bootstrap has no flags for these any
// more, so anything already in them is a zero value, and a zero value here is a
// decision — "no HA", "no monitoring" — about a cluster that may have both.
func FollowInstall(st *State, rec *InstallRecord) {
	st.Install = rec
	st.HA = rec.Settings.HA
	st.Compact = rec.Settings.Compact
	st.NoMonitoring = !rec.Settings.Monitoring
	st.NoCNPG = !rec.Settings.CNPG
	st.Values[databaseNamespaceKey] = rec.Outputs.Rdb.Namespace
	st.Values[cnpgNamespaceKey] = rec.Outputs.CNPGNamespace
	st.Values["grafanaService"] = rec.Outputs.GrafanaService
	st.Values["grafanaNamespace"] = rec.Outputs.GrafanaNamespace
	st.Values[databaseBackupOffsiteKey] = fmt.Sprint(rec.Outputs.BackupSurvivesClusterLoss)
}

// ReadInstall reads the install record a bootstrap follows, refusing — with the command
// that fixes it — a cluster that has not been installed.
func ReadInstall(ctx context.Context, kubeContext, clusterUID, installCommand string) (*InstallRecord, error) {
	_, _, typed, err := kubeClients(kubeContext)
	if err != nil {
		return nil, fmt.Errorf("connecting to the cluster to read its install record: %w", err)
	}
	rec, err := readInstallRecord(ctx, typed, clusterUID)
	if err != nil {
		return nil, refuseUninstalled(err, installCommand)
	}
	return rec, nil
}

// refuseUninstalled turns an unusable install record into the refusal an operator can
// act on.
func refuseUninstalled(err error, installCommand string) error {
	if errors.Is(err, ErrNotInstalled) {
		return fmt.Errorf("this cluster is not ready for instances — %w.\n"+
			"  Prepare it once with:\n\n    %s\n\n"+
			"  then build any number of instances on it with `dcctl bootstrap`", err, installCommand)
	}
	return err
}

// InstallCommand is the `dcctl install` invocation that prepares the cluster a command
// was aimed at, for refusals to print.
func InstallCommand(provider, cluster, kubeContext string) string {
	cmd := "dcctl install " + provider
	switch {
	case kubeContext != "":
		cmd += " --kube-context " + kubeContext
	case cluster != "" && cluster != DefaultClusterName:
		cmd += " --cluster " + cluster
	}
	return cmd
}

// reportInstall prints what the install left in place.
func reportInstall(st *State, provider string) {
	rec := st.Values
	fmt.Println(color.HiGreenString("\nDeviceChain prerequisites installed"))
	fmt.Printf("  %s %s\n", color.WhiteString("Cluster:"), color.GreenString(st.Binding.Describe()))
	fmt.Printf("  %s %s\n", color.WhiteString("Kube context:"), color.GreenString(st.KubeContext))
	fmt.Printf("  %s %s\n", color.WhiteString("Connection budget:"),
		color.GreenString("%d (each instance reserves its own share when it is bootstrapped)", st.MaxConnections))
	switch {
	case !databaseBackupsEnabled(st):
		fmt.Printf("  %s %s\n", color.WhiteString("Backups:"),
			color.YellowString("NONE — instances on this cluster archive no WAL and take no base backups"))
	case backupsAreExternal(st):
		fmt.Printf("  %s %s\n", color.WhiteString("Backups:"),
			color.GreenString("WAL archiving + scheduled base backups, to storage outside this cluster"))
	default:
		fmt.Printf("  %s %s\n", color.WhiteString("Backups:"),
			color.GreenString("WAL archiving + scheduled base backups, to an object store IN THIS CLUSTER"))
		fmt.Printf("           %s\n", color.YellowString(
			"point-in-time recovery, NOT disaster recovery — lost with the cluster. Pass --backup-credentials-file for off-site."))
	}
	if p := rec["backupServerNameRdb"]; p != "" && databaseBackupsEnabled(st) {
		fmt.Printf("  %s %s\n", color.WhiteString("Relational archive:"), color.GreenString(p))
	}
	fmt.Println(color.HiGreenString("\nNext: build an instance on it:\n\n    dcctl bootstrap %s <instance>%s\n",
		provider, bootstrapTargetFlag(st.Binding)))
}

// bootstrapTargetFlag is what a bootstrap needs to be told to land on this cluster.
func bootstrapTargetFlag(b ClusterBinding) string {
	switch {
	case !b.Managed:
		return " --kube-context " + b.KubeContext
	case b.Cluster != "" && b.Cluster != DefaultClusterName:
		return " --cluster " + b.Cluster
	}
	return ""
}
