// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os/exec"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// toolCheck describes an external tool to look for on PATH.
type toolCheck struct {
	// names are alternatives; the first found satisfies the check.
	names []string
	// guidance is shown when none of the names are found.
	guidance string
}

// preflightCmd verifies that external tooling and a kube-context are available
// before a bootstrap run.
var preflightCmd = &cobra.Command{
	Use:   "preflight [provider]",
	Short: "Check prerequisites for bootstrap",
	Long:  `Verifies that required external tools and a Kubernetes context are available`,
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(GreenUnderline("\nExternal tools"))
		checks := []toolCheck{
			{names: []string{"tofu", "terraform"}, guidance: "install OpenTofu (https://opentofu.org) or Terraform"},
			{names: []string{"kubectl"}, guidance: "install kubectl (https://kubernetes.io/docs/tasks/tools/)"},
			{names: []string{"minikube"}, guidance: "install minikube to run a local cluster"},
			{names: []string{"kind"}, guidance: "install kind to run a local cluster"},
			{names: []string{"k3d"}, guidance: "install k3d to run a local cluster"},
		}
		for _, c := range checks {
			checkTool(c)
		}

		fmt.Println(GreenUnderline("\nKubernetes contexts"))
		names, current, err := bootstrap.KubeContexts()
		if err != nil {
			fmt.Println(color.YellowString("  unable to load kube contexts: %v", err))
			return nil
		}
		if current != "" {
			fmt.Printf("  %s %s\n", color.WhiteString("current-context:"), color.GreenString(current))
		} else {
			fmt.Println(color.YellowString("  no current-context set"))
		}
		if len(names) == 0 {
			fmt.Println(color.YellowString("  no contexts found in kubeconfig"))
			return nil
		}
		fmt.Println(color.WhiteString("  available contexts:"))
		for _, name := range names {
			fmt.Printf("    %s %s\n", color.GreenString("-"), name)
		}
		return nil
	},
	SilenceUsage: true,
}

// checkTool prints a green check if any alternative is found, else a yellow miss
// with guidance.
func checkTool(c toolCheck) {
	for _, name := range c.names {
		if path, err := exec.LookPath(name); err == nil {
			fmt.Printf("  %s %s %s\n", color.GreenString("✓"), name, color.WhiteString("(%s)", path))
			return
		}
	}
	label := c.names[0]
	for _, n := range c.names[1:] {
		label += "/" + n
	}
	fmt.Printf("  %s %s %s\n", color.YellowString("✗"), label, color.YellowString("- "+c.guidance))
}

func init() {
	rootCmd.AddCommand(preflightCmd)
}
