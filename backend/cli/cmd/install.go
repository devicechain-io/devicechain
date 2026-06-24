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
var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install system components",
	Long:  `Installs infrastructure and core components needed to use DeviceChain`,
}

func init() {
	rootCmd.AddCommand(installCmd)
}
