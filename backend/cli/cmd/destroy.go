// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Destroy command flags.
var (
	destroyKubeContext   string
	destroyKeepCluster   bool
	destroyPurgeRegistry bool
	destroyDryRun        bool
	destroyAssumeYes     bool
)

// destroyCmd tears down a DeviceChain instance — the inverse of bootstrap.
var destroyCmd = &cobra.Command{
	Use:   "destroy <provider> <instance>",
	Short: "Destroy a DeviceChain instance",
	Long: `Tears down a DeviceChain instance — the inverse of bootstrap.

By default this is a full teardown: it deletes the whole cluster the instance
lives in (for the local provider, the kind cluster), which removes the operator,
infrastructure and all data in one shot, then clears the instance's local state.

Use --keep-cluster to uninstall only the instance (its Helm release + namespace),
leaving the cluster, infrastructure and operator in place for a quick re-bootstrap.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}
		opts := bootstrap.DestroyOptions{
			Options: bootstrap.Options{
				Instance:    args[1],
				KubeContext: destroyKubeContext,
				DryRun:      destroyDryRun,
				AssumeYes:   destroyAssumeYes,
			},
			KeepCluster:   destroyKeepCluster,
			PurgeRegistry: destroyPurgeRegistry,
		}
		return bootstrap.Destroy(cmd.Context(), provider, opts)
	},
	SilenceUsage: true,
}

func init() {
	destroyCmd.Flags().StringVar(&destroyKubeContext, "kube-context", "", "kube-context to target (default: auto-detect kind-<instance>)")
	destroyCmd.Flags().BoolVar(&destroyKeepCluster, "keep-cluster", false, "uninstall only the instance, leaving the cluster + infra + operator in place")
	destroyCmd.Flags().BoolVar(&destroyPurgeRegistry, "purge-registry", false, "also remove the shared local image registry container (full teardown only)")
	destroyCmd.Flags().BoolVar(&destroyDryRun, "dry-run", false, "print what would happen without destroying anything")
	destroyCmd.Flags().BoolVarP(&destroyAssumeYes, "yes", "y", false, "assume yes for prompts")

	rootCmd.AddCommand(destroyCmd)
}
