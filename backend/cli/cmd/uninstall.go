// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/spf13/cobra"
)

// Create common command for creating DeviceChain resources
var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall system components",
	Long:  `Uninstalls infrastructure and core components`,
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
