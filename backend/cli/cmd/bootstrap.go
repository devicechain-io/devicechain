/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	DUP_SPACE = regexp.MustCompile(`\s+`)
)

// Create common command for creating DeviceChain resources
var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap system data",
	Long:  `Bootstraps system microservices with example datasets`,
}

// Remove leading space and any cases of multiple spaces internally.
func unspace(val string) *string {
	cleaned := DUP_SPACE.ReplaceAllString(strings.TrimSpace(val), " ")
	return &cleaned
}

// Create a string pointer from a string.
func s(val string) *string {
	return &val
}

// Section header for bootstrap operation.
func title(dataset string) {
	fmt.Println(GreenUnderline(fmt.Sprintf("\nBootstrap Data for %s Dataset", dataset)))
}

// Section header for bootstrap operation.
func header(model string, dataset string) {
	fmt.Println(WhiteUnderline(fmt.Sprintf("\nCreate %s for %s Dataset", model, dataset)))
}

// Footer for bootstrap operation.
func footer(dataset string) {
	fmt.Println(color.HiGreenString(fmt.Sprintf("\nBootstrap Completed for %s Dataset.", dataset)))
}

func init() {
	bootstrapCmd.PersistentFlags().StringP("server", "s", "localhost", "server hostname targeted for remote calls")
	bootstrapCmd.PersistentFlags().StringP("instance", "i", "dc1", "instance id targeted for remote calls")
	bootstrapCmd.PersistentFlags().StringP("tenant", "t", "tenant1", "tenant id targeted for remote calls")

	rootCmd.AddCommand(bootstrapCmd)
}
