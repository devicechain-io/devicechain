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
	upgradeEscrowFile  string
	upgradeEscrowPass  string
	upgradeSkipInfra   bool

	upgradeMqttFailClosed bool
	upgradeTrustedProxies []string

	upgradeValues      []string
	upgradeResetValues bool
)

// upgradeOptions is the UpgradeOptions the upgrade flags describe, split out of RunE so
// the copy of each flag into its field can be tested.
func upgradeOptions(instance string, mqttFailClosed *bool, valuesOverlay map[string]interface{}) bootstrap.UpgradeOptions {
	return bootstrap.UpgradeOptions{
		Options: bootstrap.Options{
			Instance:      instance,
			KubeContext:   upgradeKubeContext,
			DryRun:        upgradeDryRun,
			ImageRegistry: upgradeRegistry,
			ImageVersion:  upgradeVersion,

			MqttFailClosed: mqttFailClosed,
			TrustedProxies: upgradeTrustedProxies,
			ValuesOverlay:  valuesOverlay,
			ResetValues:    upgradeResetValues,
		},
		EscrowFile:           upgradeEscrowFile,
		EscrowPassphraseFile: upgradeEscrowPass,
		DcctlVersion:         Version,
		SkipInfrastructure:   upgradeSkipInfra,
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

It mints no credentials. Every credential the instance is running on is read back
and kept: the database passwords, the broker's authority and logins, the
cross-service secret and the secret-store root key. A version change here cannot
become a credential change.

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
		mqttFailClosed := mqttFailClosedFlag(cmd.Flags().Changed, upgradeMqttFailClosed)
		if upgradeResetValues && len(upgradeValues) > 0 {
			return fmt.Errorf("--values replaces the recorded overlay and --reset-values removes it; give one")
		}
		var valuesOverlay map[string]interface{}
		if len(upgradeValues) > 0 {
			if valuesOverlay, err = bootstrap.LoadValuesOverlay(upgradeValues); err != nil {
				return err
			}
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
	upgradeCmd.Flags().StringVar(&upgradeEscrowFile, "escrow-file", "", "where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The upgrade checks it still protects the key the instance is running on")
	upgradeCmd.Flags().BoolVar(&upgradeSkipInfra, "skip-infrastructure", false, "move only the services: leave this instance's message broker and event store on the configuration they were built with")
	upgradeCmd.Flags().StringArrayVar(&upgradeValues, "values", nil, valuesUsage)
	upgradeCmd.Flags().BoolVar(&upgradeMqttFailClosed, "mqtt-fail-closed", false, mqttFailClosedUsage)
	upgradeCmd.Flags().StringSliceVar(&upgradeTrustedProxies, "trusted-proxies", nil, trustedProxiesUsage)
	upgradeCmd.Flags().BoolVar(&upgradeResetValues, "reset-values", false, "remove the tuning overlay recorded on the instance: it returns to the values dcctl computes. Cannot be combined with --values")
	upgradeCmd.Flags().StringVar(&upgradeEscrowPass, "escrow-passphrase-file", "", "read the escrow passphrase from this file (or $"+bootstrap.EscrowPassphraseEnv+"). Only needed to WRITE an escrow for an instance that has none — checking an existing one needs no passphrase")

	rootCmd.AddCommand(upgradeCmd)
}
