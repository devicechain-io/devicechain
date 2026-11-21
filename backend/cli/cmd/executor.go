// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

const executorPreviewHelp = "Preview: the executor is installed and verified but executes no operations in this release"

func newExecutorCommand() *cobra.Command {
	var kubeContext string
	group := &cobra.Command{
		Use: "executor", Short: "Manage the in-cluster executor (Preview)",
		Long: executorPreviewHelp, Args: cobra.NoArgs,
	}
	group.PersistentFlags().StringVar(&kubeContext, "kube-context", "", "kube-context to target (default: current context)")

	var owner, image, escrowDir string
	var dryRun, force bool
	enroll := &cobra.Command{
		Use: "enroll", Short: "Install and verify the executor (Preview)", Long: executorPreviewHelp,
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := bootstrap.ValidateExecutorEnrollment(owner, image); err != nil {
				return err
			}
			installCommand := "dcctl install <provider>"
			if kubeContext != "" {
				installCommand += fmt.Sprintf(" --kube-context %q", kubeContext)
			}
			return bootstrap.EnrollExecutor(cmd.Context(), bootstrap.ExecutorEnrollOptions{
				Owner: owner, Image: image, EscrowDir: escrowDir, KubeContext: kubeContext,
				DryRun: dryRun, InstallCommand: installCommand,
			}, cmd.OutOrStdout())
		},
	}
	enroll.Flags().StringVar(&owner, "owner", "", "executor owner (DNS label, at most 63 characters)")
	enroll.Flags().StringVar(&image, "image", "", "executor image pinned as ref@sha256:<64 lowercase hex digits>")
	enroll.Flags().StringVar(&escrowDir, "escrow-dir", "", "directory containing existing instance root-key escrow artifacts (default: the dcctl escrow directory)")
	enroll.Flags().BoolVar(&dryRun, "dry-run", false, "validate enrollment and run read-only checks, then print the objects that would be applied")
	_ = enroll.MarkFlagRequired("owner")
	_ = enroll.MarkFlagRequired("image")
	status := &cobra.Command{
		Use: "status", Short: "Show enrollment, capability and pending operations", Long: executorPreviewHelp,
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return bootstrap.ExecutorStatus(cmd.Context(), kubeContext, cmd.OutOrStdout())
		},
	}
	unenroll := &cobra.Command{
		Use: "unenroll", Short: "Remove the executor, leaving instances and resource definitions", Long: executorPreviewHelp,
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return bootstrap.UnenrollExecutor(cmd.Context(), kubeContext, force, cmd.OutOrStdout())
		},
	}
	unenroll.Flags().BoolVar(&force, "force", false, "first mark non-terminal operations Refused/Unenrolled, then remove the executor")
	group.AddCommand(enroll, status, unenroll)
	return group
}

func init() { rootCmd.AddCommand(newExecutorCommand()) }
