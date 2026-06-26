// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Destroy command flags.
var (
	destroyPurgeData bool
	destroyAssumeYes bool
)

// destroyCmd tears down a DeviceChain instance. SKELETON: it only prints the
// teardown plan; no destructive action is taken yet (ADR-032).
var destroyCmd = &cobra.Command{
	Use:   "destroy <provider> <instance>",
	Short: "Destroy a DeviceChain instance",
	Long:  `Tears down a DeviceChain instance. The data stack is preserved unless --purge-data is set.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, instance := args[0], args[1]

		fmt.Println(GreenUnderline(
			fmt.Sprintf("\nTeardown plan for instance %q on provider %q", instance, provider)))

		// TODO(ADR-032 phase: destroy): helm uninstall the application release.
		fmt.Println(color.WhiteString("  1. helm uninstall the application release"))
		// TODO(ADR-032 phase: destroy): tofu destroy the *platform* stack (ingress/cert-manager/operator infra).
		fmt.Println(color.WhiteString("  2. tofu destroy the platform stack (ingress, cert-manager, operator infra)"))
		// TODO(ADR-032 phase: destroy): namespace cleanup.
		fmt.Println(color.WhiteString("  3. namespace cleanup"))

		if destroyPurgeData {
			// TODO(ADR-032 phase: destroy): tofu destroy the data stack (Postgres/Timescale/NATS) and PVCs.
			fmt.Println(color.RedString("  4. --purge-data: tofu destroy the data stack (Postgres, Timescale, NATS) and PVCs"))
		} else {
			fmt.Println(color.GreenString("  Data stack preserved (pass --purge-data to drop it)."))
		}

		fmt.Println(color.YellowString(
			"\nNote: destroy is a skeleton (ADR-032); nothing was actually destroyed."))
		_ = destroyAssumeYes
		return nil
	},
	SilenceUsage: true,
}

func init() {
	destroyCmd.Flags().BoolVar(&destroyPurgeData, "purge-data", false, "also destroy the data stack (Postgres/Timescale/NATS); destructive")
	destroyCmd.Flags().BoolVarP(&destroyAssumeYes, "yes", "y", false, "assume yes for prompts")

	rootCmd.AddCommand(destroyCmd)
}
