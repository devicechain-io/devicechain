// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Upgrade command flags.
var (
	upgradeKubeContext string
	upgradeRegistry    string
	upgradeVersion     string
	upgradeDryRun      bool
)

// upgradeCmd moves the operator install onto a release's version — the half of an
// upgrade that `helm upgrade` cannot reach, because the operator is not in the chart.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade <provider> <instance>",
	Short: "Upgrade an instance's operator to a released version",
	Long: `Upgrades the cluster-scoped operator install — namespace, CRDs, RBAC and the
controller Deployment — to a released version.

A DeviceChain release is one version across the service images, the Helm chart,
the operator and dcctl. The services are upgraded by 'helm upgrade', but the
operator is not part of the chart: it is applied from manifests embedded in this
binary. This command is what moves it, and it is a required step of an upgrade —
without it an instance runs new services against the controller it was
bootstrapped with, indefinitely.

It touches nothing else. It does not run the Helm upgrade, does not apply the
infrastructure stack, and does not generate, read or rotate any credential, so it
is safe on a live instance. (Re-running 'bootstrap' would rotate every generated
credential, which is why this is a separate verb rather than advice to do that.)

  # Upgrade the operator to a published release, after the helm upgrade
  dcctl upgrade local devicechain --version v1.3.0

  # Point at images you built yourself
  dcctl upgrade local devicechain --registry localhost:5000 --version my-build`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The provider is resolved rather than ignored: it is validated here, so a
		// typo fails against the argument instead of against a real cluster derived
		// from it, and the run reports which environment it believes it is acting on.
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		opts := bootstrap.UpgradeOptions{
			Options: bootstrap.Options{
				Instance:      args[1],
				KubeContext:   upgradeKubeContext,
				DryRun:        upgradeDryRun,
				ImageRegistry: upgradeRegistry,
				ImageVersion:  upgradeVersion,
			},
		}
		return bootstrap.Upgrade(cmd.Context(), provider, opts)
	},
	SilenceUsage: true,
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap)")
	upgradeCmd.Flags().StringVar(&upgradeRegistry, "registry", "", "image registry to pull the operator from (default: "+bootstrap.DefaultImageRegistry+")")
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "release version to upgrade the operator to (default: this dcctl's pinned version)")
	upgradeCmd.Flags().BoolVar(&upgradeDryRun, "dry-run", false, "print what would happen without changing anything")

	rootCmd.AddCommand(upgradeCmd)
}
