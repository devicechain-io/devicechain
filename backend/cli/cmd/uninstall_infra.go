/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

package cmd

import (
	"fmt"
	"io/fs"

	"github.com/fatih/color"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/cli"
)

// Create instance of uninstall infra command
var uninstallInfraCmd = NewUninstallInfraCommand()

// Create command for uninstalling DeviceChain infrastructure
func NewUninstallInfraCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "infra",
		Short:        "Uninstall infrastructure components",
		Long:         `Uninstalls DeviceChain infrastructure dependencies`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return uninstallInfraComponents()
		},
	}
}

// Uninstall infrastructure components.
func uninstallInfraComponents() error {
	fmt.Println("Preparing to uninstall DeviceChain infrastructure components...")

	settings := cli.New()
	err := uninstallHelmReleases(settings)
	if err != nil {
		return err
	}

	fmt.Println(color.HiGreenString("\nUninstall completed successfully."))
	return nil
}

// Uninstall Helm releases for each chart embedded in the binary.
func uninstallHelmReleases(settings *cli.EnvSettings) error {
	fmt.Println(GreenUnderline("\nUnnstall Helm Charts"))
	return fs.WalkDir(ChartFS, "install_infra/charts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			cinfo, err := parseChartInfo(d)
			if err != nil {
				return err
			}
			fmt.Printf("Uninstalling Helm Chart: Repository: %s Chart: %s Version: %s Release: %s\n",
				color.GreenString(cinfo.Repository),
				color.GreenString(cinfo.Chart),
				color.GreenString(cinfo.Version),
				color.GreenString(cinfo.Release),
			)

			_, err = uninstallHelmRelease(settings, cinfo)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func init() {
	uninstallCmd.AddCommand(uninstallInfraCmd)
}
