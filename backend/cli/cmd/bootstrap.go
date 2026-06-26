// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// Bootstrap command flags.
var (
	bootstrapKubeContext   string
	bootstrapProfile       string
	bootstrapDryRun        bool
	bootstrapAssumeYes     bool
	bootstrapSkipPreflight bool
	bootstrapRegistry      string
	bootstrapVersion       string
	bootstrapBuild         bool
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

		// Diagnose the local system up front so a run fails fast on a missing
		// tool / low limit / unreachable docker rather than midway through.
		if !bootstrapSkipPreflight {
			if d := runDoctor(args[0]); d.fails > 0 {
				return fmt.Errorf("%d preflight check(s) failed — fix the items above, or re-run with --skip-preflight", d.fails)
			}
		}

		opts := bootstrap.Options{
			Instance:      args[1],
			KubeContext:   bootstrapKubeContext,
			Profile:       bootstrapProfile,
			DryRun:        bootstrapDryRun,
			AssumeYes:     bootstrapAssumeYes,
			ImageRegistry: bootstrapRegistry,
			ImageVersion:  bootstrapVersion,
			BuildImages:   bootstrapBuild,
		}

		ctx := cmd.Context()
		// EnsureCluster resolves the kube-context we should target.
		kubeContext, err := provider.EnsureCluster(ctx, opts)
		if err != nil {
			return err
		}

		st := &bootstrap.State{
			Instance:      opts.Instance,
			KubeContext:   kubeContext,
			Profile:       opts.Profile,
			DryRun:        opts.DryRun,
			AssumeYes:     opts.AssumeYes,
			ImageRegistry: opts.ImageRegistry,
			ImageVersion:  opts.ImageVersion,
			BuildImages:   opts.BuildImages,
			Values:        map[string]string{},
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
	bootstrapCmd.Flags().BoolVar(&bootstrapSkipPreflight, "skip-preflight", false, "skip the local-system preflight checks")
	bootstrapCmd.Flags().StringVar(&bootstrapRegistry, "registry", "", "image registry to deploy from (default: published ghcr.io/devicechain-io, or localhost:5000 with --build)")
	bootstrapCmd.Flags().StringVar(&bootstrapVersion, "version", "", "image version/tag to deploy (default: the published release version, or 'dev' with --build)")
	bootstrapCmd.Flags().BoolVar(&bootstrapBuild, "build", false, "build images from source into a local registry (developer path; requires source + ko)")

	rootCmd.AddCommand(bootstrapCmd)
}
