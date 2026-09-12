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

// upgradeCmd moves a live instance onto a release's version. It began as the half of
// an upgrade `helm upgrade` could not reach — the operator is not in the chart — and
// it is now the whole of it, because once dcctl owns the configuration document the
// chart no longer renders one for `helm upgrade` to move.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade <provider> <instance>",
	Short: "Move a live instance onto a released version",
	Long: `Upgrades an existing instance: the cluster-scoped operator install (namespace,
CRDs, RBAC and the controller), the configuration document its services read, and
the Helm release that runs them.

A DeviceChain release is one version across the service images, the Helm chart,
the operator and dcctl. This command moves all of them together, in the order
they have to move in — the operator and its CRDs first, then the services.

It mints no credentials. Every credential the instance is running on is read back
and kept: the database passwords, the broker's authority and logins, the
cross-service secret and the secret-store root key. A version change here cannot
become a credential change.

It does not change an instance's shape. The profile, topology and functional
areas come from the instance's own declaration, which is what 'dcctl bootstrap'
recorded. Use it to move versions, not to reconfigure.

  # Move an instance onto a published release
  dcctl upgrade local devicechain --version v1.3.0

  # Point at images you built yourself
  dcctl upgrade local devicechain --registry localhost:5000 --version my-build

  # See what it would do first
  dcctl upgrade local devicechain --version v1.3.0 --dry-run`,
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
	upgradeCmd.Flags().StringVar(&upgradeRegistry, "registry", "", "image registry to pull from (default: "+bootstrap.DefaultImageRegistry+")")
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "release version to upgrade to (default: this dcctl's pinned version)")
	upgradeCmd.Flags().BoolVar(&upgradeDryRun, "dry-run", false, "print what would happen without changing anything")

	rootCmd.AddCommand(upgradeCmd)
}
