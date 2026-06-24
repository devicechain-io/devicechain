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
var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall system components",
	Long:  `Uninstalls infrastructure and core components`,
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
