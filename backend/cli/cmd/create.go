/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

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
