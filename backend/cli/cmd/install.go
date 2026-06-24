// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/spf13/cobra"
)

// Create common command for creating DeviceChain resources
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install system components",
	Long:  `Installs infrastructure and core components needed to use DeviceChain`,
}

func init() {
	rootCmd.AddCommand(installCmd)
}
