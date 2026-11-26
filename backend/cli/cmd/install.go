// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installchoice"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/spf13/cobra"
)

// Install command flags.
var (
	installKubeContext         string
	installCluster             string
	installDryRun              bool
	installAssumeYes           bool
	installSkipPreflight       bool
	installDev                 bool
	installHA                  bool
	installCompact             bool
	installSize                string
	installNoTLS               bool
	installNoMonitoring        bool
	installAlerts              alertFlags
	installNoCNPG              bool
	installAllowLegacyDb       bool
	installBackupCredentials   string
	installBackupSnapClass     string
	installDBNodeSelector      []string
	installDBTolerations       []string
	installMaxConnections      int
	installRelationalMaxWALMiB int
	installRestoreRdbFrom      string
	installRestoreRdbAt        string
	installRegistry            string
	installVersion             string
	installBuild               bool
	installRemoveLegacy        bool
	installAWSNLB              string
	installMQTTGateway         bool
)

func installChoices(cmd *cobra.Command) (installchoice.Choices, error) {
	cmd.InitDefaultHelpFlag()
	return installchoice.FromFlags(cmd.Flags())
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

It installs what every instance on the cluster shares: the DeviceChain operator and
its CRDs, the relational store and the backup object store in namespace dc-system,
and the CloudNativePG operator, cert-manager, ingress and the monitoring stack each
in a namespace of its own. It creates the base database identity each instance's own
login is made with, and records the install in the cluster. Every bootstrap follows
that record: an instance on an --ha cluster is HA, an instance on a --compact
cluster is compact.

The operator and its CRDs are the cluster's, not any instance's — there is one copy
shared by every instance, so the release a cluster is prepared at is chosen here with
--version, and moving it is this command's job rather than a side effect of building
or upgrading one instance.

An install by a dcctl older than the one that last installed the cluster is refused:
the operator and its definitions are shared by every instance, and there is no downgrade.
Use that release's dcctl or newer.

--database-node-selector places the relational store, and the event store of every
instance bootstrapped on the cluster, on the nodes it names; --database-toleration lets
them onto nodes tainted to keep other workloads off.

The ingress controller is Traefik, which serves Gateway API routes. ingress-nginx is
DEPRECATED (the Kubernetes project has retired it) and is no longer installed: a cluster that
already runs it keeps it, serving, and gains Traefik beside it; a cluster that does not gets
Traefik alone. --remove-legacy-ingress removes ingress-nginx, and refuses while any
Ingress object in the cluster still names its class: move each instance first with
'dcctl upgrade --drop-legacy-ingress'.

Running it again converges. Changing its settings is refused while any instance runs on
the cluster, with one exception: the connection budget may be raised.`,
	Example: `  # a local kind cluster named "devicechain"
  dcctl install local
  # a small-footprint cluster, for a laptop
  dcctl install local --compact
  # an existing cluster (any cloud): dcctl never creates or deletes it
  dcctl install local --kube-context prod
  dcctl install local --kube-context prod --ha --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		stream, err := beginInstallOutput(cmd.Flags())
		if err != nil {
			return err
		}
		defer func() { err = stream.end(cmd.Context(), err) }()
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		choices, err := installChoices(cmd)
		if err != nil {
			return err
		}
		choices.Provider = args[0]
		prepared, err := choices.Prepare(Version, func(s string) { fmt.Println(s) })
		if err != nil {
			return err
		}
		effective := prepared.Effective

		doctorOptions := doctorOptsFor(effective.KubeContext, effective.Cluster, effective.Build)
		doctorOptions.FullSizeInstall = !effective.Compact

		return runInstallEngine(cmd.Context(), &bootstrap.InstallEngine{
			Provider:      provider,
			Options:       bootstrap.InstallOptions{Options: prepared.BootstrapOptions()},
			SkipPreflight: effective.SkipPreflight,
			Cluster:       installEngine,
			ResolveOptions: func() (bootstrap.InstallOptions, error) {
				plan, err := prepared.ResolveWithReader(readAlertFile)
				return plan.Options, err
			},
		}, doctorRunner(args[0], doctorOptions))
	},
	SilenceUsage: true,
}

// installEngine is the cluster work the install engine runs after its preflight:
// bootstrap.Install, held in a variable so the characterisation goldens
// (install_golden_test.go) can stand a recording fake in for it and pin what the command
// layer and the engine's own first stages print and decide. Production never reassigns it.
var installEngine = bootstrap.Install

// runInstallEngine runs an install through the plain presenter: today's output, on the
// process's own streams, with the kind-cluster prompt answered from stdin. With
// --output json it runs through the event stream's presenter instead, which never
// reads stdin.
func runInstallEngine(ctx context.Context, e *bootstrap.InstallEngine, preflight installflow.PreflightRunner) error {
	if ctx == nil { // RunE called directly, as tests do, rather than through Execute
		ctx = context.Background()
	}
	if s := activeInstallStream; s != nil {
		return s.run(ctx, e, preflight)
	}
	return installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}.Run(ctx, e,
		installRequest(e), preflight)
}

func init() {
	installCmd.Flags().StringVar(&installKubeContext, "kube-context", "", "install into this existing cluster instead of a local kind cluster (never created or deleted by dcctl)")
	installCmd.Flags().StringVar(&installCluster, "cluster", bootstrap.DefaultClusterName, "local provider: the kind cluster to create or use")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "print what would happen without applying changes")
	installCmd.Flags().BoolVarP(&installAssumeYes, "yes", "y", false, "assume yes for prompts")
	installCmd.Flags().BoolVar(&installSkipPreflight, "skip-preflight", false, "skip the local-system preflight checks")
	installCmd.Flags().BoolVar(&installDev, "dev", false, "local-developer preset: --build --yes (builds the operator image from this source checkout); rejects contradictory flags")
	installCmd.Flags().BoolVar(&installHA, "ha", false, "a replicated relational store (3 CloudNativePG instances, synchronous), and every instance bootstrapped on this cluster HA too: a 3-server NATS cluster with replicated streams and a replicated event store. Needs at least 3 schedulable nodes; database volumes are sized per instance")
	installCmd.Flags().BoolVar(&installCompact, "compact", false, "small-footprint preset for the cluster and every instance on it: smaller volumes, lowered JetStream/KV ceilings and scheduling requests, no monitoring stack, and — unless --no-tls=false — no cert-manager and therefore no database backups. Instances on a compact cluster keep the default profile or a smaller one")
	installCmd.Flags().StringVar(&installSize, "size", "", sizeInstallUsage)
	installCmd.Flags().BoolVar(&installNoTLS, "no-tls", false, "with --compact: instances serve plain HTTP, so cert-manager is not installed and database backups (whose plugin needs it) are off. --compact --no-tls=false keeps both")
	installCmd.Flags().BoolVar(&installNoMonitoring, "no-monitoring", false, "skip the monitoring stack (Prometheus/Grafana); instances on the cluster render no ServiceMonitors or alerts")
	installAlerts.register(installCmd)
	installCmd.Flags().BoolVar(&installNoCNPG, "no-cnpg", false, "skip the CloudNativePG operator and the database backup plugin — for a cluster that ALREADY runs CNPG, since Helm cannot adopt objects another installer created")
	installCmd.Flags().BoolVar(&installAllowLegacyDb, "allow-legacy-db-removal", false,
		"proceed even though this cluster still runs the pre-CloudNativePG relational StatefulSet "+
			"(dc-postgresql). 🔴 This ASSERTS THAT YOU HAVE HANDLED THE DATA — applying with it set "+
			"destroys that StatefulSet and brings up an empty database on the same hostname")
	installCmd.Flags().StringVar(&installBackupCredentials, "backup-credentials-file", "", "send database backups to an object store you already own, described by this JSON file: {endpointUrl, bucketRdb, bucketTsdb, accessKeyId, secretAccessKey}. Without it the cluster provisions its own in-cluster store, which lives in the same failure domain as the databases it backs up. Keep the file readable only by you")
	installCmd.Flags().StringVar(&installBackupSnapClass, "backup-snapshot-class", "",
		"take each database's daily base backup as a CSI volume snapshot with this VolumeSnapshotClass "+
			"instead of a full copy in the backup store. A full copy still goes to the store weekly, WAL "+
			"archiving is unchanged, and restores still read the store. The class must exist, use "+
			"deletionPolicy Delete, and belong to the driver that provisions the database volumes. Every "+
			"instance on the cluster follows it")
	// Database placement. Install flags only: every instance's event store follows the
	// install record, so a bootstrap has none of its own.
	installCmd.Flags().StringSliceVar(&installDBNodeSelector, "database-node-selector", nil,
		"run the databases only on nodes with this label, as key=value (repeatable; every label must "+
			"match). Applies to the relational store and to the event store of every instance "+
			"bootstrapped on the cluster. Refused when too few schedulable nodes match")
	installCmd.Flags().StringSliceVar(&installDBTolerations, "database-toleration", nil,
		"let the databases run on nodes with this taint, written as kubectl taint writes it: "+
			"key=value:Effect, or key:Effect for any value (repeatable). Needs --database-node-selector")
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
	// The image source for the OPERATOR, which is the one workload `dcctl install`
	// deploys. The service images belong to an instance and are `dcctl bootstrap`'s
	// to choose — these three flags are spelled the same way there deliberately, but
	// they select different things, because a cluster and the instances on it are
	// versioned separately.
	installCmd.Flags().StringVar(&installRegistry, "registry", "",
		"pull the operator image from this registry (default: the published registry, or the "+
			"local one with --build)")
	installCmd.Flags().StringVar(&installVersion, "version", "",
		"install the operator at this released tag (default: the release this dcctl was built "+
			"for). This is the CLUSTER's version — the instances on it carry their own")
	installCmd.Flags().BoolVar(&installBuild, "build", false,
		"developer path: ko-build the operator image from this source checkout and push it to a "+
			"local registry, instead of pulling a published one. Builds ONLY the operator; the "+
			"service images are an instance's and are built by dcctl bootstrap --build")
	installCmd.Flags().BoolVar(&installRemoveLegacy, "remove-legacy-ingress", false, "remove ingress-nginx from a cluster that still runs it. An install otherwise leaves it serving and installs Traefik beside it. Refused while any Ingress object in the cluster still names its class (each instance must first be moved with 'dcctl upgrade --drop-legacy-ingress'); on a local cluster Traefik takes the node's ports 80 and 443 in the same apply")
	installCmd.Flags().StringVar(&installAWSNLB, "aws-nlb", "", "EKS: which ingress controllers' LoadBalancer Services get an AWS Network Load Balancer (service.beta.kubernetes.io/aws-load-balancer-type: nlb) instead of the Classic one a plain Service gets: traefik, nginx, all, or none (turns it off). Only a Network Load Balancer passes the client's address through, which the per-source rate limits and the trusted-proxy rule depend on. Left off, each controller's Service keeps what it has. A value that CHANGES an existing Service replaces its load balancer and so changes the hostname clients resolve: repoint DNS afterwards (the install says so). traefik alone leaves ingress-nginx's Service as it is, for a cluster mid-cutover")
	installCmd.Flags().StringVarP(&installOutput, "output", "o", installOutputText, installOutputUsage)
	installCmd.Flags().BoolVar(&installMQTTGateway, "mqtt-gateway", false, "open TCP 8883 on the ingress controller (Traefik) so an instance can serve MQTT over TLS to devices outside the cluster through its Gateway (dcctl bootstrap|upgrade --mqtt-exposure gateway). It publishes a NEW port on the controller's load balancer, so it is off unless asked for. The broker terminates TLS itself; the controller passes the connection through by SNI. Left off, the install keeps whatever the controller's Service has; --mqtt-gateway=false closes it")
	installCmd.Flags().IntVar(&installMaxConnections, "max-connections", 0, "the relational store's connection budget (default 700 on a first install, and what the cluster has on a re-run). Each instance reserves (its relational services x 40) of it when bootstrapped; the default admits two default-profile instances")

	installCmd.Flags().IntVar(&installRelationalMaxWALMiB, "relational-store-max-wal-mib", 0, "relational store max_wal_size soft checkpoint target in MiB (0: stock default; positive: at least 64). Omitted keeps the recorded choice, or the root default on a first install. Recorded before apply; limited to a quarter of storage by the root")

	rootCmd.AddCommand(installCmd)
}
