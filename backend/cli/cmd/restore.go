// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"github.com/devicechain-io/dcctl/backup"
	"github.com/spf13/cobra"
)

func newRestoreCmd() *cobra.Command {
	var opts backupOptions
	var from string
	var plan, forceForeign bool
	cmd := &cobra.Command{
		Use:          "restore <instance> --from <backup-set> --plan",
		Short:        "Verify a backup and print a read-only restore plan",
		Long:         "Verify local backup bytes, identity and coverage before connecting to the installed instance.\nRead target stream state and blob bytes; refuse possible newer-data loss. No restore is applied.\nFreeze target writers while planning; this is an observation, not a reservation for a later apply.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !plan {
				return fmt.Errorf("--plan is required; restore apply is not implemented")
			}
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			verified, err := backup.PrepareRestore(from, args[0], forceForeign)
			if err != nil {
				return err
			}
			opts.InstanceID, opts.RestorePlan, opts.PlanOutput = args[0], verified, cmd.OutOrStdout()
			return backupInstance(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Local verified backup-set directory (required)")
	cmd.Flags().BoolVar(&plan, "plan", false, "Print a plan without applying it (required)")
	cmd.Flags().BoolVar(&forceForeign, "force-foreign", false, "Allow a foreign instance or tenant identity; never bypass loss or coverage checks")
	cmd.Flags().StringVar(&opts.KubeContext, "kube-context", "", "Kubernetes context of the installed instance")
	cmd.Flags().StringVar(&opts.NatsURL, "nats-url", "", "Reachable broker URL (default: port-forward)")
	cmd.Flags().StringVar(&opts.BlobDirectory, "blob-directory", "", "Read-only mounted/exported target filesystem blob PVC root")
	return cmd
}

func init() { rootCmd.AddCommand(newRestoreCmd()) }
