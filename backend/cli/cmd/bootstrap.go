// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Bootstrap command flags.
var (
	bootstrapKubeContext string
	bootstrapProfile     string
	bootstrapDryRun      bool
	bootstrapAssumeYes   bool
)

// bootstrapCmd provisions a usable DeviceChain instance on a target provider.
// It is a thin wrapper over the bootstrap engine package (ADR-032).
var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <provider> <instance>",
	Short: "Bootstrap a DeviceChain instance",
	Long:  `Provisions a usable DeviceChain instance on the given provider (e.g. "local")`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, err := bootstrap.Get(args[0])
		if err != nil {
			return err
		}

		opts := bootstrap.Options{
			Instance:    args[1],
			KubeContext: bootstrapKubeContext,
			Profile:     bootstrapProfile,
			DryRun:      bootstrapDryRun,
			AssumeYes:   bootstrapAssumeYes,
		}

		ctx := cmd.Context()
		// EnsureCluster resolves the kube-context we should target.
		kubeContext, err := provider.EnsureCluster(ctx, opts)
		if err != nil {
			return err
		}

		st := &bootstrap.State{
			Instance:    opts.Instance,
			KubeContext: kubeContext,
			Profile:     opts.Profile,
			DryRun:      opts.DryRun,
			AssumeYes:   opts.AssumeYes,
			Values:      map[string]string{},
		}
		return bootstrap.NewDefaultPipeline().Run(ctx, st)
	},
	SilenceUsage: true,
}

func init() {
	bootstrapCmd.Flags().StringVar(&bootstrapKubeContext, "kube-context", "", "kube-context to target (default: auto-detect)")
	bootstrapCmd.Flags().StringVar(&bootstrapProfile, "profile", "", "configuration profile to apply")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "print what would happen without applying changes")
	bootstrapCmd.Flags().BoolVarP(&bootstrapAssumeYes, "yes", "y", false, "assume yes for prompts")

	rootCmd.AddCommand(bootstrapCmd)
}
