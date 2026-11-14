// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/devicechain-io/dcctl/backup"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	var opts backupOptions
	parent := &cobra.Command{
		Use:   "backup <instance> --destination <dir>",
		Short: "Export instance streams and blobs, or inspect a local backup set",
		Long:  "Export checkpoint-preserved JetStream streams with consumers and instance blobs.\nFreeze writers before export. This does not coordinate database recovery cutoffs or maintenance mode.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && cmd.Flags().NFlag() == 0 {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if opts.Destination == "" {
				return fmt.Errorf("--destination is required")
			}
			opts.InstanceID = args[0]
			return backupInstance(cmd.Context(), opts)
		},
	}
	parent.Flags().StringVar(&opts.Destination, "destination", "", "New local backup directory (must not exist)")
	parent.Flags().StringVar(&opts.KubeContext, "kube-context", "", "Kubernetes context of the installed instance")
	parent.Flags().StringVar(&opts.NatsURL, "nats-url", "", "Reachable broker URL (default: port-forward)")
	parent.Flags().StringVar(&opts.BlobDirectory, "blob-directory", "", "Read-only mounted/exported filesystem blob PVC root")
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
