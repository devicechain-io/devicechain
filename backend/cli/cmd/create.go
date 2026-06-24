// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/spf13/cobra"
)

// Create common command for creating DeviceChain resources
var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create system resources",
	Long:  `Commands that create DeviceChain system resources`,
}

func init() {
	rootCmd.AddCommand(createCmd)
}
