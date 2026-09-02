// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Destroy command flags.
var (
	destroyKubeContext   string
	destroyKeepCluster   bool
	destroyPurgeRegistry bool
	destroyDryRun        bool
	destroyAssumeYes     bool
	destroyAll           bool
)

// destroyCmd tears down a DeviceChain instance — the inverse of bootstrap.
var destroyCmd = &cobra.Command{
	Use:   "destroy <provider> <instance>",
	Short: "Destroy a DeviceChain instance",
	Long: `Tears down a DeviceChain instance — the inverse of bootstrap.

By default this is a full teardown: it deletes the cluster the instance lives in
(for the local provider, the kind cluster), which removes the operator,
infrastructure and all data in one shot, then clears the instance's local state.

Which cluster that is comes from a record written at bootstrap, not from the
instance's name. An instance bootstrapped into a cluster somebody else created
(with --kube-context) has that cluster LEFT RUNNING: the instance is uninstalled
from it and its local state cleared, and the cluster is named on the way out.

An instance bootstrapped before dcctl recorded this has no record, and destroy
falls back to guessing the cluster from the instance name — saying so as it goes.
Run "dcctl instances list" to see which instances are in that state.

Use --keep-cluster to uninstall only the instance (its Helm release + namespace),
leaving the cluster, infrastructure and operator in place for a quick re-bootstrap.
Use --all to destroy every instance on this machine.`,
	// Not ExactArgs(2): `--all` takes no instance, because the whole point of it is that
	// the operator does not have to know what is there. Validated below so the error says
	// which form was wrong rather than "accepts 2 arg(s)".
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if destroyAll {
			if len(args) > 0 {
				return fmt.Errorf("--all destroys every instance and takes no arguments (got %q)", strings.Join(args, " "))
			}
			return destroyEveryInstance(cmd.Context())
		}
		if len(args) != 2 {
			return fmt.Errorf("accepts 2 args (provider and instance), or --all with none; received %d", len(args))
		}
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
	destroyCmd.Flags().StringVar(&destroyKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap)")
	destroyCmd.Flags().BoolVar(&destroyKeepCluster, "keep-cluster", false, "uninstall only the instance, leaving the cluster + infra + operator in place")
	destroyCmd.Flags().BoolVar(&destroyPurgeRegistry, "purge-registry", false, "also remove the shared local image registry container (full teardown only)")
	destroyCmd.Flags().BoolVar(&destroyDryRun, "dry-run", false, "print what would happen without destroying anything")
	destroyCmd.Flags().BoolVarP(&destroyAssumeYes, "yes", "y", false, "assume yes for prompts")
	destroyCmd.Flags().BoolVar(&destroyAll, "all", false, "destroy EVERY instance on this machine (takes no arguments)")

	rootCmd.AddCommand(destroyCmd)
}

// destroyEveryInstance is the single command Derek asked for: shut it all down without
// having to know what "it all" is, or which provider anything is on.
//
// 🔴 IT PRINTS THE PLAN BEFORE IT ASKS. A bulk teardown whose confirmation prompt says
// only "destroy everything?" gives the operator nothing to check the answer against —
// and the failure this whole change exists to fix is precisely a destroy that acted on a
// cluster nobody had named out loud. So the table below IS the prompt.
//
// 🔴 AND IT DOES NOT STOP AT THE FIRST FAILURE. Halting halfway through leaves an operator
// who asked to tear everything down with an unknown subset still running and no statement
// of which — the same "reported one thing, did another" shape, wearing a different hat.
// Every instance is attempted, every outcome is reported, and the exit status reflects
// whether any failed.
func destroyEveryInstance(ctx context.Context) error {
	known, err := bootstrap.ListInstances()
	if err != nil {
		return err
	}
	if len(known) == 0 {
		fmt.Println(color.WhiteString("No DeviceChain instances on this machine (nothing under ~/.devicechain)."))
		return nil
	}

	fmt.Println(bootstrap.GreenUnderline("\nThese instances will be destroyed"))
	if err := runInstancesList(ctx, os.Stdout); err != nil {
		return err
	}
	if destroyKeepCluster {
		fmt.Println(color.YellowString(
			"\n--keep-cluster: every cluster above is LEFT RUNNING. Only the instances are\n" +
				"uninstalled, and their local state is kept for a re-bootstrap."))
	} else {
		fmt.Println(color.YellowString(
			"\nAn ADOPTED cluster is left running — only the instance is uninstalled from it.\n" +
				"An instance with no record has its cluster GUESSED from its name; if that guess is\n" +
				"wrong the cluster is left running and its local state is still cleared."))
	}

	if destroyDryRun {
		fmt.Println(color.YellowString("\n[dry-run] nothing was destroyed."))
		return nil
	}
	prompt := fmt.Sprintf("Permanently destroy ALL %d instance(s) above? This deletes ALL of their data", len(known))
	if destroyKeepCluster {
		prompt = fmt.Sprintf("Uninstall ALL %d instance(s) above, keeping their clusters?", len(known))
	}
	if !destroyAssumeYes && !bootstrap.Confirm(prompt) {
		fmt.Println(color.YellowString("Aborted."))
		return nil
	}

	var failed []string
	for _, k := range known {
		providerName := "local"
		if k.HasRecord && k.Record.Provider != "" {
			providerName = k.Record.Provider
		}
		provider, err := bootstrap.Get(providerName)
		if err != nil {
			fmt.Println(color.RedString("  %s: %v", k.Instance, err))
			failed = append(failed, k.Instance)
			continue
		}
		opts := bootstrap.DestroyOptions{
			Options: bootstrap.Options{
				Instance:  k.Instance,
				DryRun:    false,
				AssumeYes: true, // the one confirmation above covers the whole run
			},
			// 🔴 CARRIED THROUGH, not dropped. An earlier version built these options
			// without KeepCluster, so `--all --keep-cluster` deleted every cluster the
			// operator had just asked to keep — and the confirmation they answered never
			// mentioned clusters at all. A flag silently ignored on the one command that
			// acts on everything is the worst place for it.
			KeepCluster:   destroyKeepCluster,
			PurgeRegistry: destroyPurgeRegistry,
		}
		if err := bootstrap.Destroy(ctx, provider, opts); err != nil {
			fmt.Println(color.RedString("  %s: %v", k.Instance, err))
			failed = append(failed, k.Instance)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d of %d instance(s) could not be destroyed: %s",
			len(failed), len(known), strings.Join(failed, ", "))
	}
	fmt.Println(color.HiGreenString("\nAll %d instance(s) destroyed.", len(known)))
	return nil
}
