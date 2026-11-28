// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dcctl/backup"
	"github.com/spf13/cobra"
)

// Keep --only explicit so adding resource types never broadens an existing
// command line's destructive scope.
const restoreOnlyObjectStore = "object-store"
const restoreOnlyStream = "stream"

func newRestoreCmd() *cobra.Command {
	return newRestoreCmdWithRunners(backupInstance, restoreObjects)
}

// Inject only the installed-instance boundary; command tests still verify real
// backup sets and exercise the real JetStream restore against a local broker.
func newRestoreCmdWithRunners(instance func(context.Context, backupOptions) error, objects func(context.Context, backupOptions, bool) error) *cobra.Command {
	var opts backupOptions
	var from, only string
	var plan, apply, forceForeign, overwriteTarget bool
	cmd := &cobra.Command{
		Use:   "restore <instance> --from <backup-set> (--plan | --apply --only object-store|stream)",
		Short: "Verify a backup and print a restore plan, or restore its objects or streams",
		Long: "Verify local backup bytes, identity and coverage before connecting to the installed instance.\n" +
			"--plan reads target stream state and blob bytes, refuses possible newer-data loss and prints the plan; it changes nothing.\n" +
			"Freeze target writers while planning; a plan is an observation, not a reservation for a later apply.\n\n" +
			"--apply --only object-store writes the backup's objects into the target instance's blob store. It refuses a\n" +
			"target holding any object that differs from the backup, or is not in it, unless --overwrite-target is given;\n" +
			"objects already identical are left alone, so an interrupted restore is finished by running it again.\n" +
			"Every object is checked against the verified backup before it is committed, and the target is read back at\n" +
			"the end.\n\n" +
			"--apply --only stream restores captured JetStream streams, messages and consumers. Existing streams\n" +
			"are refused unless --overwrite-target explicitly authorizes deleting and replacing them. Replacement\n" +
			"is destructive and not atomic. Freeze writers and stream/consumer creation throughout the restore.\n" +
			"Snapshot size and SHA-256 are rechecked before replacement; each chunk is acknowledged and success\n" +
			"requires the server's final restore response. No objects are written and --blob-directory is not needed.\n" +
			"Uses backup's broker credentials; the current broker-admin grant lacks restore/upload permissions.\n" +
			"--apply requires --only object-store or --only stream; it never defaults to everything.",
		Example: `  dcctl restore acme --from ./acme-backup --plan
  dcctl restore acme --from ./acme-backup --plan --kube-context prod
  dcctl restore acme-copy --from ./acme-backup --plan --force-foreign
  dcctl restore acme --from ./acme-backup --apply --only object-store --blob-directory /mnt/acme-blob
  dcctl restore acme --from ./acme-backup --apply --only stream
  dcctl restore acme --from ./acme-backup --apply --only stream --overwrite-target`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if plan && apply {
				return fmt.Errorf("--plan and --apply are mutually exclusive")
			}
			if !plan && !apply {
				return fmt.Errorf("--plan or --apply is required")
			}
			if plan && (only != "" || overwriteTarget) {
				return fmt.Errorf("--only and --overwrite-target apply only to --apply")
			}
			if apply && only != restoreOnlyObjectStore && only != restoreOnlyStream {
				return fmt.Errorf("--apply requires --only object-store or --only stream")
			}
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			verified, err := backup.PrepareRestore(from, args[0], forceForeign)
			if err != nil {
				return err
			}
			opts.InstanceID, opts.RestorePlan, opts.PlanOutput = args[0], verified, cmd.OutOrStdout()
			if apply && only == restoreOnlyObjectStore {
				return objects(cmd.Context(), opts, overwriteTarget)
			}
			opts.RestoreStreams, opts.OverwriteTarget = apply, overwriteTarget
			return instance(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Local verified backup-set directory (required)")
	cmd.Flags().BoolVar(&plan, "plan", false, "Print a plan without applying it")
	cmd.Flags().BoolVar(&apply, "apply", false, "Restore the resources named by --only into the target instance")
	cmd.Flags().StringVar(&only, "only", "", "With --apply, the resource to restore: object-store or stream (required)")
	cmd.Flags().BoolVar(&overwriteTarget, "overwrite-target", false, "With --apply, replace differing objects or delete and replace existing captured streams; resources not in the backup are kept")
	cmd.Flags().BoolVar(&forceForeign, "force-foreign", false, "Allow a foreign instance or tenant identity; never bypass loss or coverage checks")
	cmd.Flags().StringVar(&opts.KubeContext, "kube-context", "", "Kubernetes context of the installed instance")
	cmd.Flags().StringVar(&opts.NatsURL, "nats-url", "", "Reachable broker URL (default: port-forward)")
	cmd.Flags().StringVar(&opts.BlobDirectory, "blob-directory", "", "Mounted/exported target filesystem blob PVC root (read-only for --plan, writable for --apply)")
	return cmd
}

func init() { rootCmd.AddCommand(newRestoreCmd()) }
