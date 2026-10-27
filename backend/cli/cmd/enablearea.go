// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Enable-area command flags.
var (
	enableAreaKubeContext string
	enableAreaDryRun      bool
	enableAreaEscrowFile  string
)

// enableAreaCmd turns on a functional area on a running instance.
//
// 🔑 IT IS `dcctl upgrade` WITH THE AREA APPENDED, NOT A SECOND PIPELINE. The area set
// decides the chart values, the database login's connection limit and, under --ha, the
// event store's reserve — all of which the upgrade already composes from the
// declaration. So this verb adds the areas to the declaration's delta and runs that
// upgrade at the version the instance is already on; the next ordinary upgrade then
// reads the same declaration and keeps the area, instead of pruning it.
var enableAreaCmd = &cobra.Command{
	Use:   "enable-area <provider> <instance> <area> [<area>...]",
	Short: "Turn on a functional area on a running instance",
	Long: `Deploys one or more functional areas on an instance that is already running —
for example outbound-connectors, ai-inference or mcp on an
instance bootstrapped with the default profile. It is the day-2 form of
'dcctl bootstrap --enable-area'.

The area names are checked against the area catalog before anything touches the
cluster, and the instance's resulting area set is checked for unmet dependencies
before anything is written. An unknown name or a missing dependency changes
nothing.

It records the areas in the instance's declaration, then applies the instance the
way 'dcctl upgrade' does — the instance's message broker and event store, the
configuration document its services read, and its Helm release — at the version
the instance is already on, and waits for every area to be ready. The next
'dcctl upgrade' reads the same declaration, so it keeps the areas.

It does not move the instance's version. It refuses when this dcctl belongs to a
different release than the instance's services, because it would apply this
release's chart and infrastructure configuration under the old images; upgrade
the instance first. Like 'dcctl upgrade', it needs 'tofu' on PATH and this
machine's state for the instance, and it mints no credentials.

Asking for an area the instance already runs is not an error. When every area
named is already declared and the instance's last run ended Ready, nothing is
changed. When the last run did not end Ready, it applies the instance again,
which finishes an earlier enable-area that stopped part of the way.

Some areas need configuration before they do useful work — ai-inference needs a
model provider registered by an administrator, for example. Enabling the area
deploys it; it does not configure it.

There is no command that disables an area. An enabled area may hold data, and
taking it out of the deployment would leave that data with nothing serving it.

  # Add the outbound connectors area to an instance
  dcctl enable-area local devicechain outbound-connectors

  # Add two areas at once, and see what would happen first
  dcctl enable-area local devicechain outbound-connectors mcp --dry-run`,
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		// Judged from the command line alone, before any cluster is read: a typo is
		// answered at once, with the names that would have been accepted.
		areas, err := bootstrap.CheckAreaRequest(args[2:])
		if err != nil {
			return err
		}
		opts := bootstrap.UpgradeOptions{
			Options: bootstrap.Options{
				Instance:    args[1],
				KubeContext: enableAreaKubeContext,
				DryRun:      enableAreaDryRun,
			},
			EscrowFile:   enableAreaEscrowFile,
			DcctlVersion: Version,
			EnableAreas:  areas,
		}
		return bootstrap.Upgrade(cmd.Context(), provider, opts)
	},
	SilenceUsage: true,
}

func init() {
	enableAreaCmd.Flags().StringVar(&enableAreaKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap; a different context is refused)")
	enableAreaCmd.Flags().BoolVar(&enableAreaDryRun, "dry-run", false, "print what would happen without changing anything")
	enableAreaCmd.Flags().StringVar(&enableAreaEscrowFile, "escrow-file", "", "where this instance's root-key escrow artifact lives (default ~/.devicechain/escrow/<instance>-rootkey.escrow). The run checks it still protects the key the instance is running on")

	rootCmd.AddCommand(enableAreaCmd)
}
