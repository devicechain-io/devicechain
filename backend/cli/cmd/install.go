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
)

// installCmd prepares a cluster for DeviceChain instances.
var installCmd = &cobra.Command{
	Use:   "install <provider>",
	Short: "Prepare a cluster for DeviceChain instances",
	Long: `Prepares a cluster once, so that any number of instances can be built on it with
"dcctl bootstrap".

For the local provider it creates a kind cluster (named by --cluster, default
"devicechain") if there is none, or uses the one that exists. --kube-context installs
into an existing cluster instead, which dcctl never creates or deletes.

It installs what every instance on the cluster shares, in namespace dc-system: the
CloudNativePG operator, the relational store, the backup object store, cert-manager,
ingress and the monitoring stack. It creates the base database identity each
instance's own login is made with, and records the install in the cluster. Every
bootstrap follows that record: an instance on an --ha cluster is HA, an instance on a
--compact cluster is compact.

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
			res, err := resolveCompactMode(cmd.Flags().Changed, "", installNoTLS, installNoMonitoring)
			if err != nil {
				return err
			}
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

		if !installSkipPreflight {
			if d := runDoctor(args[0]); d.fails > 0 {
				return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", d.fails)
			}
		}

		return bootstrap.Install(cmd.Context(), provider, bootstrap.InstallOptions{
			Options: bootstrap.Options{
				KubeContext:          installKubeContext,
				Cluster:              installCluster,
				DryRun:               installDryRun,
				AssumeYes:            installAssumeYes,
				NoTLS:                installNoTLS,
				NoMonitoring:         installNoMonitoring,
				NoCNPG:               installNoCNPG,
				AllowLegacyDbRemoval: installAllowLegacyDb,
				Compact:              installCompact,
				HA:                   installHA,
			},
			BackupDestination: backupDestination,
			MaxConnections:    installMaxConnections,
			DcctlVersion:      Version,
		})
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
	installCmd.Flags().IntVar(&installMaxConnections, "max-connections", 0, "the relational store's connection budget (default 600 on a first install, and what the cluster has on a re-run). Each instance reserves (its relational services x 40) of it when bootstrapped; the default admits two default-profile instances")

	rootCmd.AddCommand(installCmd)
}
