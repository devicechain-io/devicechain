// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Install command flags.
var (
	installKubeContext       string
	installCluster           string
	installDryRun            bool
	installAssumeYes         bool
	installSkipPreflight     bool
	installDev               bool
	installHA                bool
	installCompact           bool
	installNoTLS             bool
	installNoMonitoring      bool
	installNoCNPG            bool
	installAllowLegacyDb     bool
	installBackupCredentials string
	installMaxConnections    int
	installRestoreRdbFrom    string
	installRestoreRdbAt      string
)

// installRestoreFlagsFromArgv assembles the relational-store restore inputs from the
// parsed flags.
//
// Extracted from RunE for the same reason restoreFlagsFromArgv is: it is two string
// copies and a derivation, exactly the kind of code that looks too trivial to test
// and then copies the source into the target — a mistake with no symptom at all
// until an operator's recovery stops at the wrong moment during an incident.
//
// 🔴 IT FILLS ONLY THE Rdb HALF. The event store belongs to an INSTANCE and is
// `dcctl bootstrap`'s to recover; a value landing in the Tsdb fields here would be
// emitted as restore_tsdb_from, routed by splitVars to the INSTANCE root, and
// applied to a store this command does not own.
func installRestoreFlagsFromArgv(backupsEnabled bool) bootstrap.RestoreFlags {
	return bootstrap.RestoreFlags{
		RdbFrom:        installRestoreRdbFrom,
		RdbTargetTime:  installRestoreRdbAt,
		BackupsEnabled: backupsEnabled,
	}
}

// compactModeResolution is the set of flag values the --compact preset settles on.
type compactModeResolution struct {
	NoTLS        bool
	NoMonitoring bool
}

// resolveCompactMode expands the --compact small-footprint preset on top of the
// user's explicit flags.
//
// Two of its levers live on flags that already exist, so they are resolved here
// rather than buried in the pipeline: the monitoring stack (~5 pods, the single
// largest consumer) is skipped, and TLS is off — which is what makes dropping
// cert-manager safe, since cert-manager is what issues the ingress certificate.
//
// An explicit --no-tls=false is HONOURED. It is not a contradiction, it is a
// dependency: TLS stays on, cert-manager stays installed to issue the cert, and every
// other compact lever still applies. Erroring here would cost real functionality to
// no benefit. Which profiles fit a compact cluster is a bootstrap's question, settled
// by followClusterShape.
//
// `changed` reports whether the user set a given flag explicitly.
func resolveCompactMode(changed func(string) bool, noTLS, noMonitoring bool) compactModeResolution {
	res := compactModeResolution{NoTLS: true, NoMonitoring: true}
	// An explicit --no-tls=false keeps TLS (and therefore cert-manager); an explicit
	// --no-monitoring=false keeps the observability stack. Both cost footprint, and
	// both are the operator's call to make.
	if changed("no-tls") {
		res.NoTLS = noTLS
	}
	if changed("no-monitoring") {
		res.NoMonitoring = noMonitoring
	}
	return res
}

// installOptions assembles what the install engine is told to do, from the parsed
// flags and the two plans RunE has already settled.
//
// 🔴 EXTRACTED FROM RunE BECAUSE NOTHING COULD OTHERWISE SEE IT. bootstrap.Install
// needs a provider and a cluster, so no test reaches this struct literal through the
// command — and a literal is precisely where a settled plan gets dropped or landed in
// the wrong field. That failure is silent in the worst direction: `dcctl install
// --restore-rdb-from` would report a perfectly ordinary, perfectly green install of an
// EMPTY relational store, during the recovery it was run for.
func installOptions(dest *bootstrap.BackupDestination, restore bootstrap.RestorePlan) bootstrap.InstallOptions {
	return bootstrap.InstallOptions{
		Options: bootstrap.Options{
			KubeContext:          installKubeContext,
			Cluster:              installCluster,
			DryRun:               installDryRun,
			AssumeYes:            installAssumeYes,
			NoTLS:                installNoTLS,
			AllowLegacyDbRemoval: installAllowLegacyDb,
		},
		NoMonitoring:      installNoMonitoring,
		NoCNPG:            installNoCNPG,
		Compact:           installCompact,
		HA:                installHA,
		BackupDestination: dest,
		MaxConnections:    installMaxConnections,
		Restore:           restore,
		DcctlVersion:      Version,
	}
}

// installCmd prepares a cluster for DeviceChain instances.
var installCmd = &cobra.Command{
	Use:   "install <provider>",
	Short: "Prepare a cluster for DeviceChain instances",
	Long: `Prepares a cluster once, so that any number of instances can be built on it with
"dcctl bootstrap".

For the local provider it creates a kind cluster (named by --cluster, default
"devicechain") if there is none, or uses the one that exists. --kube-context installs
into an existing cluster instead, which dcctl never creates or deletes.

It installs what every instance on the cluster shares: the relational store and the
backup object store in namespace dc-system, and the CloudNativePG operator,
cert-manager, ingress and the monitoring stack each in a namespace of its own. It
creates the base database identity each instance's own login is made with, and
records the install in the cluster. Every bootstrap follows that record: an instance
on an --ha cluster is HA, an instance on a --compact cluster is compact.

Running it again converges. Changing its settings is refused while any instance runs on
the cluster, with one exception: the connection budget may be raised.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		if installDev {
			installAssumeYes = true
			fmt.Println("dev mode: --yes")
		}
		// 🔴 --no-tls ALONE DOES NOTHING TO A CLUSTER, and accepting it would let an
		// operator believe cert-manager was left out.
		if cmd.Flags().Changed("no-tls") && installNoTLS && !installCompact {
			return fmt.Errorf("--no-tls on dcctl install only takes effect with --compact (it drops " +
				"cert-manager and, with it, database backups). Without --compact, choose plain HTTP per " +
				"instance with dcctl bootstrap --no-tls")
		}
		if installCompact {
			res := resolveCompactMode(cmd.Flags().Changed, installNoTLS, installNoMonitoring)
			installNoTLS, installNoMonitoring = res.NoTLS, res.NoMonitoring
			fmt.Printf("compact mode: %s\n", bootstrap.CompactSummary())
		}
		if installHA {
			fmt.Printf("ha mode: %s\n", bootstrap.HaSummary(true))
			if installCompact {
				fmt.Println("note: --ha adds two more NATS servers per instance and their volumes; the printed compact footprint is measured single-node and is a floor, not the total")
			}
		}
		if installMaxConnections != 0 && installMaxConnections < 100 {
			return fmt.Errorf("--max-connections must be at least 100 (got %d)", installMaxConnections)
		}

		// Validated before any cluster exists: a bad object-store credential crashes
		// nothing. WAL archiving simply stops, and the first symptom is an archive-lag
		// alert — or a restore that finds no base backup.
		backupDestination, err := bootstrap.ParseBackupDestination(installBackupCredentials)
		if err != nil {
			return fmt.Errorf("--backup-credentials-file: %w", err)
		}
		// 🔴 AN OFF-SITE ARCHIVE IS MEANINGLESS WITHOUT THE BACKUPS IT ARCHIVES, and the
		// flags that switch them off do so as a consequence of other choices — so this
		// combination is reachable without ever having asked for it.
		if backupDestination.Configured() &&
			!bootstrap.DatabaseBackupsEnabled(installNoCNPG, installCompact, installNoTLS) {
			return fmt.Errorf("--backup-credentials-file names an off-site archive, but this " +
				"combination of flags leaves the cluster with no database backups to send there " +
				"(--no-cnpg removes the operator the backup plugin extends; --compact with " +
				"--no-tls drops the cert-manager it needs). Drop the flag, or drop whichever of " +
				"those turned backups off")
		}

		// 🔴 SETTLED FROM ARGV, BEFORE ANY CLUSTER IS TOUCHED. Every way a restore's
		// flags can be wrong — a recovery target with nothing to recover, a timestamp
		// with no offset, an archive on a cluster that has no plugin to read it — is
		// knowable here, and finding out ten minutes into a rebuild, during an
		// incident, is the expensive time to find out.
		restorePlan, err := bootstrap.ResolveRestorePlan(installRestoreFlagsFromArgv(
			bootstrap.DatabaseBackupsEnabled(installNoCNPG, installCompact, installNoTLS)))
		if err != nil {
			return err
		}

		if !installSkipPreflight {
			if d := runDoctor(args[0]); d.fails > 0 {
				return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", d.fails)
			}
		}

		return bootstrap.Install(cmd.Context(), provider, installOptions(backupDestination, restorePlan))
	},
	SilenceUsage: true,
}

func init() {
	installCmd.Flags().StringVar(&installKubeContext, "kube-context", "", "install into this existing cluster instead of a local kind cluster (never created or deleted by dcctl)")
	installCmd.Flags().StringVar(&installCluster, "cluster", bootstrap.DefaultClusterName, "local provider: the kind cluster to create or use")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "print what would happen without applying changes")
	installCmd.Flags().BoolVarP(&installAssumeYes, "yes", "y", false, "assume yes for prompts")
	installCmd.Flags().BoolVar(&installSkipPreflight, "skip-preflight", false, "skip the local-system preflight checks")
	installCmd.Flags().BoolVar(&installDev, "dev", false, "local-developer preset: --yes")
	installCmd.Flags().BoolVar(&installHA, "ha", false, "a replicated relational store (3 CloudNativePG instances, synchronous), and every instance bootstrapped on this cluster HA too: a 3-server NATS cluster with replicated streams and a replicated event store. Needs at least 3 schedulable nodes; database volumes are sized per instance")
	installCmd.Flags().BoolVar(&installCompact, "compact", false, "small-footprint preset for the cluster and every instance on it: smaller volumes, lowered JetStream/KV ceilings and scheduling requests, no monitoring stack, and — unless --no-tls=false — no cert-manager and therefore no database backups. Instances on a compact cluster keep the default profile or a smaller one")
	installCmd.Flags().BoolVar(&installNoTLS, "no-tls", false, "with --compact: instances serve plain HTTP, so cert-manager is not installed and database backups (whose plugin needs it) are off. --compact --no-tls=false keeps both")
	installCmd.Flags().BoolVar(&installNoMonitoring, "no-monitoring", false, "skip the monitoring stack (Prometheus/Grafana); instances on the cluster render no ServiceMonitors or alerts")
	installCmd.Flags().BoolVar(&installNoCNPG, "no-cnpg", false, "skip the CloudNativePG operator and the database backup plugin — for a cluster that ALREADY runs CNPG, since Helm cannot adopt objects another installer created")
	installCmd.Flags().BoolVar(&installAllowLegacyDb, "allow-legacy-db-removal", false,
		"proceed even though this cluster still runs the pre-CloudNativePG relational StatefulSet "+
			"(dc-postgresql). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — applying with it set "+
			"destroys that StatefulSet and brings up an empty database on the same hostname")
	installCmd.Flags().StringVar(&installBackupCredentials, "backup-credentials-file", "", "send database backups to an object store you already own, described by this JSON file: {endpointUrl, bucketRdb, bucketTsdb, accessKeyId, secretAccessKey}. Without it the cluster provisions its own in-cluster store, which lives in the same failure domain as the databases it backs up. Keep the file readable only by you")
	// Database restore (ADR-028 / ADR-020 A2.5). These are the CLUSTER's half: the
	// relational store is installed once per cluster and shared by every instance, so
	// recovering it is an install operation. The event store is an instance's, and
	// `dcctl bootstrap --restore-tsdb-from` recovers that one.
	//
	// They are REBUILD-time levers, not repair levers. dcctl picks the path the
	// recovered store archives INTO by itself, and that is deliberately not a flag: it
	// must stay put across every later re-run, so it is read back off the live cluster
	// rather than re-derived from argv.
	installCmd.Flags().StringVar(&installRestoreRdbFrom, "restore-rdb-from", "",
		"disaster recovery: recover the RELATIONAL store from this archive path (the serverName "+
			"inside the backup bucket, e.g. dc-rdb) instead of initialising an empty database. "+
			"🔴 Only takes effect when the store is CREATED — recover by installing into a cluster "+
			"whose relational store is not there, not by re-running against a live one")
	installCmd.Flags().StringVar(&installRestoreRdbAt, "restore-rdb-at", "",
		"stop the relational store's recovery at this RFC3339 timestamp instead of replaying the "+
			"whole archive. For the disaster where the data was destroyed correctly — a bad "+
			"migration, a mistaken delete — so pick a moment strictly before the damage. Needs "+
			"--restore-rdb-from")
	installCmd.Flags().IntVar(&installMaxConnections, "max-connections", 0, "the relational store's connection budget (default 600 on a first install, and what the cluster has on a re-run). Each instance reserves (its relational services x 40) of it when bootstrapped; the default admits two default-profile instances")

	rootCmd.AddCommand(installCmd)
}
