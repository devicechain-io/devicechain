// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Upgrade command flags.
var (
	upgradeKubeContext string
	upgradeRegistry    string
	upgradeVersion     string
	upgradeDryRun      bool
	upgradeAssumeYes   bool
	upgradeEscrowFile  string
	upgradeEscrowPass  string
	upgradeSkipInfra   bool

	upgradeAllowEnvelopeDowngrade bool

	upgradeMqttFailClosed bool
	upgradeTrustedProxies []string
	upgradeMQTTExposure   string
	upgradeMQTTHost       string
	// upgradeDevAllowEgress is --dev-allow-egress; upgradeDevAllowEgressList its parsed form.
	upgradeDevAllowEgress     []string
	upgradeDevAllowEgressList []string

	upgradeBrokerMaxConnections      int
	upgradeBrokerMaxConnectionsGiven bool
	upgradeEventStoreMaxWAL          int
	upgradeEventStoreMaxWALGiven     bool
	upgradeEventStoreCheckpoint      string
	upgradeEventStoreCheckpointGiven bool

	upgradeLwm2mIdentities   string
	upgradeWithdrawLwm2m     bool
	upgradeLwm2mIdentityList []bootstrap.Lwm2mIdentity
	upgradeSparkplugSource   string
	upgradeWithdrawSparkplug bool
	// upgradeSparkplugSources is the file above, parsed and validated by RunE.
	upgradeSparkplugSources []bootstrap.SparkplugSource

	upgradeValues      []string
	upgradeResetValues bool

	upgradeIngressController string
	upgradeDropLegacyIngress bool

	// upgradeAlerts holds the alert-routing flags; upgradeAlertRequest is what RunE settled
	// them to (files read, request validated), which upgradeOptions copies.
	upgradeAlerts       alertFlags
	upgradeAlertRequest bootstrap.AlertRoutingRequest
)

const (
	ingressControllerUsage = "which front end serves the instance's host: gateway (a Gateway API Gateway and HTTPRoutes, served by Traefik), nginx (Ingress objects, served by ingress-nginx) or auto. nginx is DEPRECATED: ingress-nginx is retired upstream, and the choice is kept for one more minor so an instance can be moved off it in its own time. " +
		"The choice is RECORDED on the instance and kept by every later upgrade, so 'nginx' stays a rollback until 'gateway' or 'auto' says otherwise; auto drops the record and decides from the cluster. " +
		"With no choice recorded or given, where Traefik runs the instance is served by the Gateway, and while it still has Ingress objects and ingress-nginx remains they are rendered beside it, so the host keeps answering there until --drop-legacy-ingress. " +
		"nginx needs ingress-nginx on the cluster, and gateway needs Traefik"
	dropLegacyIngressUsage = "stop rendering the Ingress objects beside the Gateway: run it once DNS points at the new controller, " +
		"and before 'dcctl install --remove-legacy-ingress' removes ingress-nginx, which it refuses to do while any Ingress object still names its class"
)

const (
	eventStoreMaxWALUsage     = "set the event store's max_wal_size, in MiB (at least 64). An upgrade derives it as a fifth of the event store volume, never below 1024, so a store close to full can keep the previous allowance with 1024. 0 returns to the derived value. Applied now and kept by later upgrades when the flag is omitted (recorded beside the instance's infrastructure state on this machine). Reload-only: the event store does not restart"
	eventStoreCheckpointUsage = "set the event store's checkpoint_timeout, a Postgres time such as 15min (between 30s and 1d; default 15min). An empty value returns to the default. Applied now and kept by later upgrades when the flag is omitted (recorded beside the instance's infrastructure state on this machine). Reload-only: the event store does not restart"
)

// eventStoreIntFlag and eventStoreStringFlag turn a flag's (given, value) pair into the pointer
// the options carry: nil is "keep what the instance has".
func eventStoreIntFlag(given bool, v int) *int {
	if !given {
		return nil
	}
	return &v
}

func eventStoreStringFlag(given bool, v string) *string {
	if !given {
		return nil
	}
	return &v
}

// upgradeOptions is the UpgradeOptions the upgrade flags describe, split out of RunE so
// the copy of each flag into its field can be tested.
func upgradeOptions(instance string, mqttFailClosed *bool, valuesOverlay map[string]interface{}) bootstrap.UpgradeOptions {
	return bootstrap.UpgradeOptions{
		Options: bootstrap.Options{
			Instance:      instance,
			KubeContext:   upgradeKubeContext,
			DryRun:        upgradeDryRun,
			AssumeYes:     upgradeAssumeYes,
			ImageRegistry: upgradeRegistry,
			ImageVersion:  upgradeVersion,

			MqttFailClosed: mqttFailClosed,
			TrustedProxies: upgradeTrustedProxies,
			MQTTExposure:   upgradeMQTTExposure,
			MQTTHost:       upgradeMQTTHost,
			DevAllowEgress: upgradeDevAllowEgressList,

			BrokerMaxConnections: bootstrap.BrokerMaxConnectionsFromFlag(upgradeBrokerMaxConnectionsGiven, upgradeBrokerMaxConnections),
			ValuesOverlay:        valuesOverlay,

			EventStoreMaxWALMiB:         eventStoreIntFlag(upgradeEventStoreMaxWALGiven, upgradeEventStoreMaxWAL),
			EventStoreCheckpointTimeout: eventStoreStringFlag(upgradeEventStoreCheckpointGiven, upgradeEventStoreCheckpoint),

			IngressController: upgradeIngressController,
			DropLegacyIngress: upgradeDropLegacyIngress,
			ResetValues:       upgradeResetValues,

			Lwm2mIdentities:          upgradeLwm2mIdentityList,
			WithdrawLwm2mIdentities:  upgradeWithdrawLwm2m,
			SparkplugSources:         upgradeSparkplugSources,
			WithdrawSparkplugSources: upgradeWithdrawSparkplug,
		},
		EscrowFile:           upgradeEscrowFile,
		EscrowPassphraseFile: upgradeEscrowPass,
		DcctlVersion:         Version,
		SkipInfrastructure:   upgradeSkipInfra,

		AllowEnvelopeDowngrade: upgradeAllowEnvelopeDowngrade,
		AlertRouting:           upgradeAlertRequest,
	}
}

// upgradeCmd moves a live instance onto a release's version. It began as the half of
// an upgrade `helm upgrade` could not reach — the operator is not in the chart — and
// it is now the whole of it, because once dcctl owns the configuration document the
// chart no longer renders one for `helm upgrade` to move.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade <provider> <instance>",
	Short: "Move a live instance onto a released version",
	Long: `Upgrades an existing instance: its message broker and event store, applied
from this release's OpenTofu configuration; the configuration document its
services read; and the Helm release that runs them.

A DeviceChain release is one version across the service images, the Helm chart,
the operator and dcctl — but this command moves ONE INSTANCE, not the cluster.
The operator and its CRDs are cluster-scoped: there is one copy, shared by every
instance on the cluster. Moving them is 'dcctl install', so that a cluster-wide
change happens when somebody asks for one, rather than as a side effect of
upgrading whichever instance came first.

So an upgrade is two commands, in this order:

  dcctl install <provider> --version <tag>     # once, moves the cluster
  dcctl upgrade <provider> <instance> --version <tag>   # per instance

This command checks the cluster's operator and never moves it. It REFUSES when the
cluster has no operator, or has one identifiably from another release, naming the
install command that fixes it. An operator installed by hand carries no record of
which release put it there, and dcctl cannot tell that apart from one an older
dcctl overwrote — so that case is allowed through with a note rather than refused.

It also REFUSES, before anything is written, when the cluster-level install is older than
this dcctl, or cannot be shown to be current for it: the operator's definitions can stay
the same across a release while its controller changes, so the check reads the install
record in the cluster, which names the dcctl that last finished 'dcctl install'. A
re-run at the same version is not refused, and neither is a cluster installed by a
newer dcctl. A dcctl built from source is not compared.

And it REFUSES '--version' naming a release older than this dcctl: the chart and the
infrastructure configuration it applies are this release's, so they would be planned
around images that have never seen them. To go back, use the dcctl of the older release
(its own 'upgrade --version', the documented way back). A flag-less upgrade, a tag that
on a registry of your own (--registry) and this dcctl's own release are not affected.
A release newer than the install is refused until 'dcctl install' has been run at it.

It replaces no credential. Every credential the instance is running on is read back
and kept: the database passwords, the broker's authority and logins, the
cross-service secret and the secret-store root key. A version change here cannot
become a credential change. The one thing it can create is a credential that did not
exist yet: the first upgrade of an instance built before them mints the device
authentication callout's encryption key, the login of the broker's identity account
and the per-service broker logins, once each.

The one thing it removes is the shared message broker login of an instance upgraded
from a release that had one. The upgrade that gives every area a login of its own
keeps the shared one, so pods that have not restarted yet stay connected. Run the
upgrade once more: when every area already runs its own login, it drops the shared
login from the broker and from the instance configuration, and its closing summary
says which phase ran. A new instance never has the shared login. Once it is gone,
rolling back across this release is not supported; run the newer upgrade again.

It does not change an instance's shape. The profile, topology and functional
areas come from the instance's own declaration, which is what 'dcctl bootstrap'
recorded. Use it to move versions, not to reconfigure.

The fail-closed MQTT acknowledgement is the other: '--mqtt-fail-closed' (or
'--mqtt-fail-closed=false') sets it on a live instance, and the declaration records the
choice. A plain upgrade with no flag keeps whatever was recorded, so an instance built
with it stays on, and one told off stays off.

ingress-nginx is DEPRECATED. A new instance is served by the Gateway; an instance already on
ingress-nginx stays on it, with a warning, until the cluster has Traefik and you move it.

Where the cluster runs Traefik (the Gateway API controller that replaces ingress-nginx) an
upgrade moves the instance's routing to a Gateway and HTTPRoutes. While ingress-nginx is still
on the cluster the instance's Ingress objects are rendered beside them under a certificate
Secret of their own, so the host keeps answering where it did until DNS points at the new
controller; '--drop-legacy-ingress' then stops rendering them, and 'dcctl install
--remove-legacy-ingress' removes ingress-nginx once no instance has one. '--ingress-controller'
chooses the front end by hand, records the choice on the instance, and 'nginx' is the way back
that later upgrades keep ('--ingress-controller auto' returns to deciding from the cluster). The
Gateway names a new certificate Secret while the Ingress objects remain, so the host DNS still
reaches never waits for a certificate; the upgrade waits for the Gateway's own to be Ready.

LwM2M identities ('--lwm2m-identities <file>') are kept the same way. The pre-shared keys are not in the Helm
release: dcctl writes them into a Secret of its own, dci-<instance>-lwm2m-identities, which the upgrade leaves
alone, and '--withdraw-lwm2m-identities' removes both. An instance built before the keys moved out of the release is
migrated by its first upgrade: the keys are copied into the new Secret before the release changes, and Helm then
removes the chart-rendered one. Helm's earlier revisions still hold the old values, so rotate the keys afterwards
(give '--lwm2m-identities' with new keys) if those revisions matter to you.

A Sparkplug source ('--sparkplug-source <file>') is carried the same way as the tuning below: the
sources stay on the instance through every later upgrade, and a run given the file again replaces
them. The broker password is not in the Helm release: dcctl writes it into a Secret of its own,
which the upgrade leaves alone, and '--withdraw-sparkplug-sources' removes both. The area must already be enabled
('dcctl enable-area ... sparkplug-ingest'); a bootstrap with the flag enables it.

Operator tuning is the exception, and it is carried: a values overlay given to
'dcctl bootstrap --values' or to this command is recorded on the instance and applied
again by every later upgrade. '--values <file>' replaces the recorded overlay,
'--reset-values' removes it, and --dry-run prints what would change in it.

The broker and event store are applied from this machine's OpenTofu state for the
instance, in ~/.devicechain/instances/<instance>/, so the upgrade needs 'tofu' on
PATH and runs where the instance was bootstrapped. It plans first, prints what
changes, and REFUSES — changing nothing — when this machine has no state for the
instance, when the plan would delete or replace anything the configuration
manages, or when it would shorten the event store's recovery window or drop an
analytics reader nobody declared. Volume sizes are kept. NATS servers restart one
at a time under --ha; without it the single server restarts and the broker is
unavailable meanwhile. The upgrade waits for both to be healthy before it moves
the services. Values you set on the instance's OpenTofu configuration are kept
when they are in a terraform.tfvars beside its state
(~/.devicechain/instances/<instance>/infra/instance/), and not otherwise.
The event store's WAL allowance is the one setting with flags of its own: an upgrade derives
max_wal_size as a fifth of the event store volume (up from Postgres's 1 GiB on an existing
store), says so when it rises, and warns when the volume has less free space than that.
'--event-store-max-wal-mib 1024' keeps the previous allowance, and
'--event-store-checkpoint-timeout' sets the checkpoint cadence; later upgrades keep both.
--skip-infrastructure moves only the services, and says what it left.

  # Move an instance onto a published release
  dcctl upgrade local devicechain --version v1.3.0

  # Point at images you built yourself
  dcctl upgrade local devicechain --registry localhost:5000 --version my-build

  # See what it would do first, including the broker and event store plan
  dcctl upgrade local devicechain --version v1.3.0 --dry-run

  # Move only the services, leaving the broker and event store as they are
  dcctl upgrade local devicechain --version v1.3.0 --skip-infrastructure

  # Tune the instance; later upgrades keep applying it without --values
  dcctl upgrade local devicechain --values tuning.yaml

  # Keep the event store's previous 1 GiB WAL allowance (a nearly full volume)
  dcctl upgrade local devicechain --event-store-max-wal-mib 1024

  # Once DNS points at the new controller, stop serving the host from ingress-nginx too
  dcctl upgrade local devicechain --drop-legacy-ingress

  # Connect a Sparkplug broker (the file is kept: later upgrades carry it)
  dcctl upgrade local devicechain --sparkplug-source sources.yaml

  # Turn the fail-closed MQTT acknowledgement on (kept by later upgrades), or back off
  dcctl upgrade local devicechain --mqtt-fail-closed
  dcctl upgrade local devicechain --mqtt-fail-closed=false`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The provider is resolved rather than ignored: it is validated here, so a
		// typo fails against the argument instead of against a real cluster derived
		// from it, and the run reports which environment it believes it is acting on.
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		if err := bootstrap.ValidateFrontEnd(upgradeIngressController); err != nil {
			return err
		}
		if upgradeIngressController == "nginx" && upgradeDropLegacyIngress {
			return fmt.Errorf("--drop-legacy-ingress stops rendering the Ingress objects beside the Gateway, and --ingress-controller nginx makes them the only front end; give one")
		}
		alertRequest, err := upgradeAlerts.request()
		if err != nil {
			return err
		}
		upgradeAlertRequest = alertRequest
		mqttFailClosed := mqttFailClosedFlag(cmd.Flags().Changed, upgradeMqttFailClosed)
		upgradeBrokerMaxConnectionsGiven = cmd.Flags().Changed("broker-max-connections")
		upgradeEventStoreMaxWALGiven = cmd.Flags().Changed("event-store-max-wal-mib")
		upgradeEventStoreCheckpointGiven = cmd.Flags().Changed("event-store-checkpoint-timeout")
		if err := bootstrap.CheckMQTTExposureFlagsForUpgrade(upgradeMQTTExposure, upgradeMQTTHost); err != nil {
			return err
		}
		upgradeDevAllowEgressList = nil
		if cmd.Flags().Changed("dev-allow-egress") {
			if upgradeDevAllowEgressList, err = bootstrap.ParseDevAllowEgress(upgradeDevAllowEgress); err != nil {
				return err
			}
			if upgradeDevAllowEgressList == nil {
				return fmt.Errorf("--dev-allow-egress needs an IP address, a CIDR or 'none'")
			}
			if err := bootstrap.CheckDevAllowEgressContext(upgradeKubeContext, upgradeDevAllowEgressList); err != nil {
				return err
			}
		}
		if upgradeResetValues && len(upgradeValues) > 0 {
			return fmt.Errorf("--values replaces the recorded overlay and --reset-values removes it; give one")
		}
		var valuesOverlay map[string]interface{}
		if len(upgradeValues) > 0 {
			if valuesOverlay, err = bootstrap.LoadValuesOverlay(upgradeValues); err != nil {
				return err
			}
		}
		if upgradeLwm2mIdentityList, err = bootstrap.ParseLwm2mIdentities(upgradeLwm2mIdentities); err != nil {
			return fmt.Errorf("--lwm2m-identities: %w", err)
		}
		if err := bootstrap.CheckLwm2mFlags(upgradeLwm2mIdentityList, upgradeWithdrawLwm2m); err != nil {
			return err
		}
		if upgradeSparkplugSources, err = bootstrap.ParseSparkplugSources(upgradeSparkplugSource); err != nil {
			return fmt.Errorf("--sparkplug-source: %w", err)
		}
		if err := bootstrap.CheckSparkplugFlags(upgradeSparkplugSources, upgradeWithdrawSparkplug); err != nil {
			return err
		}
		opts := upgradeOptions(args[1], mqttFailClosed, valuesOverlay)
		return bootstrap.Upgrade(cmd.Context(), provider, opts)
	},
	SilenceUsage: true,
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)")
	upgradeCmd.Flags().StringVar(&upgradeRegistry, "registry", "", "image registry to pull from (default: "+bootstrap.DefaultImageRegistry+")")
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "release version to upgrade to (default: this dcctl's pinned version)")
	upgradeCmd.Flags().BoolVar(&upgradeDryRun, "dry-run", false, "print what would happen without changing anything")
	upgradeCmd.Flags().BoolVarP(&upgradeAssumeYes, "yes", "y", false, "assume yes for prompts (an upgrade asks none, so this changes nothing; it is accepted so a script can pass the same flag to install, bootstrap and upgrade)")
	upgradeCmd.Flags().StringVar(&upgradeEscrowFile, "escrow-file", "", "where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The upgrade checks it still protects the key the instance is running on")
	upgradeCmd.Flags().BoolVar(&upgradeSkipInfra, "skip-infrastructure", false, "move only the services: leave this instance's message broker and event store on the configuration they were built with")
	upgradeCmd.Flags().BoolVar(&upgradeAllowEnvelopeDowngrade, "allow-envelope-downgrade", false, "move to a release below the first one that reads event envelopes even though the streams may still hold one: a service on such a release dead-letters an envelope and the events in it are lost to that service")
	upgradeCmd.Flags().StringArrayVar(&upgradeValues, "values", nil, valuesUsage)
	upgradeCmd.Flags().StringVar(&upgradeLwm2mIdentities, "lwm2m-identities", "", lwm2mIdentitiesUsage)
	upgradeCmd.Flags().BoolVar(&upgradeWithdrawLwm2m, "withdraw-lwm2m-identities", false, withdrawLwm2mUsage)
	upgradeCmd.Flags().StringVar(&upgradeSparkplugSource, "sparkplug-source", "", sparkplugSourceUsage)
	upgradeCmd.Flags().BoolVar(&upgradeWithdrawSparkplug, "withdraw-sparkplug-sources", false, withdrawSparkplugUsage)
	upgradeCmd.Flags().BoolVar(&upgradeMqttFailClosed, "mqtt-fail-closed", false, mqttFailClosedUsage)
	upgradeCmd.Flags().StringSliceVar(&upgradeTrustedProxies, "trusted-proxies", nil, trustedProxiesUsage)
	upgradeCmd.Flags().IntVar(&upgradeBrokerMaxConnections, "broker-max-connections", 0, brokerMaxConnectionsUsage)
	upgradeCmd.Flags().IntVar(&upgradeEventStoreMaxWAL, "event-store-max-wal-mib", 0, eventStoreMaxWALUsage)
	upgradeCmd.Flags().StringVar(&upgradeEventStoreCheckpoint, "event-store-checkpoint-timeout", "", eventStoreCheckpointUsage)
	upgradeCmd.Flags().StringVar(&upgradeMQTTExposure, "mqtt-exposure", "", mqttExposureUsage)
	upgradeCmd.Flags().StringVar(&upgradeMQTTHost, "mqtt-host", "", mqttHostUsage)
	upgradeCmd.Flags().StringSliceVar(&upgradeDevAllowEgress, "dev-allow-egress", nil, devAllowEgressUsage)
	upgradeCmd.Flags().StringVar(&upgradeIngressController, "ingress-controller", "", ingressControllerUsage)
	upgradeCmd.Flags().BoolVar(&upgradeDropLegacyIngress, "drop-legacy-ingress", false, dropLegacyIngressUsage)
	upgradeAlerts.register(upgradeCmd)
	upgradeCmd.Flags().BoolVar(&upgradeResetValues, "reset-values", false, "remove the tuning overlay recorded on the instance: it returns to the values dcctl computes. Cannot be combined with --values")
	upgradeCmd.Flags().StringVar(&upgradeEscrowPass, "escrow-passphrase-file", "", "read the escrow passphrase from this file (or $"+bootstrap.EscrowPassphraseEnv+"). Only needed to WRITE an escrow for an instance that has none — checking an existing one needs no passphrase")

	rootCmd.AddCommand(upgradeCmd)
}
