// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/backup"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "backup",
		Short: "Inspect local backup sets",
	}
	var verify bool
	inspect := &cobra.Command{
		Use:   "inspect <backup-set-dir>",
		Short: "Verify a local backup manifest and all listed snapshot files",
		Long: "Read manifest.json and verify each listed snapshot's size and SHA-256.\n" +
			"Requires --verify. Uses no cluster access. Verification covers listed files only;\n" +
			"it does not establish inventory completeness or checkpoint consistency.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !verify {
				return fmt.Errorf("--verify is required")
			}
			m, err := backup.Inspect(args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Verified manifest v%d: %d snapshots\n", m.Version, len(m.Entries))
			return err
		},
	}
	inspect.Flags().BoolVar(&verify, "verify", false, "Verify every listed snapshot's size and SHA-256 (required)")
	parent.AddCommand(inspect)
	return parent
}

func init() {
	rootCmd.AddCommand(newBackupCmd())
}
