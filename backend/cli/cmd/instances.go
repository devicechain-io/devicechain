// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// `dcctl instances list` — what is on this machine, and where.
//
// 🔴 WHY "INSTANCES" AND NOT "CLUSTERS". An instance is what an operator NAMES; a cluster
// is a detail of whichever provider they happen to be on. The whole reason this command
// exists is that finding out what was running meant `kind get clusters` — reaching past
// dcctl for provider knowledge the CLI is supposed to hide. Naming the command after the
// provider's noun would have reproduced that in the CLI's own vocabulary. The cluster is
// shown as a COLUMN, which is where a detail belongs.
//
// 🔴 SECURITY: THIS COMMAND READS ONLY instance.json. Nothing else in the instance
// directory is opened, and that is a property to preserve rather than an accident of the
// current implementation. Its neighbour, terraform.tfstate, holds the database superuser
// password and the broker's TLS private key in cleartext — and THIS output is the kind of
// thing an operator pastes into an issue or has on screen while sharing. A display field
// sourced from the state file would be one path away from printing a private key.
// TestInstancesListReadsOnlyTheRecord pins it.

var instancesCmd = &cobra.Command{
	Use:   "instances",
	Short: "Inspect the DeviceChain instances on this machine",
}

var instancesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the instances bootstrapped on this machine and the clusters they live in",
	Long: `Lists every instance dcctl has state for, the cluster it was bootstrapped into,
and whether that cluster is still there.

The cluster column is read from a record written at bootstrap. An instance created
before dcctl recorded that shows as "no record" — destroy still works on it, but it
falls back to guessing the cluster from the instance name, which is wrong for any
instance bootstrapped with --kube-context.

A row reading "cluster gone" is an instance whose cluster has been deleted out from
under it, leaving only local state. That is the normal end state of a validation rig
run, and clearing it is what ` + "`dcctl destroy`" + ` does.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstancesList(cmd.Context(), os.Stdout)
	},
	SilenceUsage: true,
}

// instanceStatus is the derived half of a row: the record says where the instance should
// be, and the provider is asked whether that is true NOW. Deliberately not stored — a
// cached "running" is a claim that rots the moment somebody runs `kind delete`.
func instanceStatus(ctx context.Context, known bootstrap.KnownInstance) string {
	if known.Err != nil {
		return "record unreadable: " + known.Err.Error()
	}
	if !known.HasRecord {
		return "no record — destroy will guess the cluster"
	}
	provider, err := bootstrap.Get(known.Record.Provider)
	if err != nil {
		return "unknown provider " + known.Record.Provider
	}
	exists, err := provider.ClusterExists(ctx, known.Record.Binding())
	switch {
	case err != nil:
		return "could not check: " + err.Error()
	case !exists:
		return "cluster gone — stale local state"
	case !known.Record.Managed:
		return "running (adopted cluster — destroy will not delete it)"
	default:
		return "running"
	}
}

func runInstancesList(ctx context.Context, out *os.File) error {
	known, err := bootstrap.ListInstances()
	if err != nil {
		return err
	}
	// 🔴 The empty case gets a SENTENCE, not an empty table. A header with no rows under
	// it reads identically to a command that failed to look, which is the exact ambiguity
	// this whole change exists to remove.
	if len(known) == 0 {
		fmt.Fprintln(out, color.WhiteString("No DeviceChain instances on this machine (nothing under ~/.devicechain)."))
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INSTANCE\tPROVIDER\tCLUSTER\tCONTEXT\tSTATUS")
	for _, k := range known {
		provider, cluster, kubeContext := "?", "?", "?"
		if k.HasRecord {
			provider = k.Record.Provider
			cluster = k.Record.Cluster
			if cluster == "" {
				cluster = "-"
			}
			kubeContext = k.Record.KubeContext
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.Instance, provider, cluster, kubeContext, instanceStatus(ctx, k))
	}
	return w.Flush()
}

func init() {
	instancesCmd.AddCommand(instancesListCmd)
	rootCmd.AddCommand(instancesCmd)
}
