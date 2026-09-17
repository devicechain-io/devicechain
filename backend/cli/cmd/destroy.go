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
	destroyKubeContext  string
	destroyDryRun       bool
	destroyAssumeYes    bool
	destroyAll          bool
	destroyWithoutState bool
)

// destroyCmd removes a DeviceChain instance — the inverse of bootstrap.
var destroyCmd = &cobra.Command{
	Use:   "destroy <provider> <instance>",
	Short: "Destroy a DeviceChain instance",
	Long: `Removes a DeviceChain instance — the inverse of bootstrap.

This deletes the instance and ALL ITS DATA, in this order: its Helm release; its
own infrastructure — the broker and event store — by "tofu destroy" over the
instance's infrastructure state; its database and login on the shared relational
store; its namespace, waiting until it is gone; and, last, its local state under
~/.devicechain/instances/<instance>. The root-key escrow is kept. A destroy that
fails part-way keeps the local state, and running it again resumes.

If the instance's infrastructure state is missing or empty but its broker or event
store is running, or the state cannot be read, or it still holds the cluster's shared
prerequisites (an instance built by an older dcctl), destroy refuses before changing
anything. --without-state removes such an instance anyway: it skips "tofu destroy"
and removes the instance by its Helm release, database and login, and namespace,
saying that tofu destroy was skipped and what it left on the cluster.

The cluster, and the shared prerequisites "dcctl install" put there, are never
touched — other instances may be using them, and destroy leaves the cluster
running whether or not dcctl created it. To delete a local cluster, do it by
hand: kind delete cluster --name <name>

Which cluster the instance is in comes from a record written at bootstrap, not
from the instance's name; --kube-context overrides it. An instance bootstrapped
before dcctl recorded this has no record, and destroy falls back to guessing the
cluster from the instance name — saying so as it goes. Run "dcctl instances list"
to see which instances are in that state. If the cluster is already gone, only
the local state is cleared.

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
		return bootstrap.Destroy(cmd.Context(), provider, destroyOptionsFor(args[1], destroyKubeContext, destroyDryRun, destroyAssumeYes))
	},
	SilenceUsage: true,
}

func init() {
	destroyCmd.Flags().StringVar(&destroyKubeContext, "kube-context", "", "kube-context to target (default: the cluster recorded at bootstrap)")
	destroyCmd.Flags().BoolVar(&destroyDryRun, "dry-run", false, "print what would happen without destroying anything")
	destroyCmd.Flags().BoolVarP(&destroyAssumeYes, "yes", "y", false, "assume yes for prompts")
	destroyCmd.Flags().BoolVar(&destroyAll, "all", false, "destroy EVERY instance on this machine (takes no arguments)")
	destroyCmd.Flags().BoolVar(&destroyWithoutState, "without-state", false,
		"skip tofu destroy and remove the instance by its release, database, login and namespace (for an instance whose infrastructure state is lost)")

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
		fmt.Println(color.WhiteString("No DeviceChain instances on this machine (nothing under ~/.devicechain/instances)."))
		return nil
	}

	fmt.Println(bootstrap.GreenUnderline("\nThese instances will be destroyed"))
	if err := runInstancesList(ctx, os.Stdout); err != nil {
		return err
	}
	fmt.Println(color.YellowString(
		"\nEvery cluster above is LEFT RUNNING — only the instances are removed from them.\n" +
			"An instance with no record has its cluster GUESSED from its name; if that guess is\n" +
			"wrong, the instance is looked for in the wrong cluster."))

	if destroyWithoutState {
		fmt.Println(color.YellowString("\n--without-state: tofu destroy is SKIPPED for every instance above."))
	}
	if destroyDryRun {
		fmt.Println(color.YellowString("\n[dry-run] nothing was destroyed."))
		return nil
	}
	prompt := fmt.Sprintf("Permanently destroy ALL %d instance(s) above? This deletes ALL of their data; every cluster stays", len(known))
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
		// DryRun false and AssumeYes true: the one confirmation above covers the whole run.
		if err := bootstrap.Destroy(ctx, provider, destroyOptionsFor(k.Instance, "", false, true)); err != nil {
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

// destroyOptionsFor builds one instance's destroy options, for both the single form and
// every instance of --all.
//
// 🔴 ONE CONSTRUCTOR, BECAUSE --all BUILT ITS OWN AND A FLAG ADDED TO ONE LITERAL IS NOT
// ADDED TO THE OTHER. --without-state dropped on the --all path would run tofu destroy —
// or the empty-state refusal — on every instance the operator had said to skip it for.
func destroyOptionsFor(instance, kubeContext string, dryRun, assumeYes bool) bootstrap.DestroyOptions {
	return bootstrap.DestroyOptions{
		Options: bootstrap.Options{
			Instance:    instance,
			KubeContext: kubeContext,
			DryRun:      dryRun,
			AssumeYes:   assumeYes,
		},
		WithoutState: destroyWithoutState,
	}
}
