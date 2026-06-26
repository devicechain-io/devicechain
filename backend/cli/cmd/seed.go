// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

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

// seedCmd is the parent command for seeding example datasets into a running
// instance over GraphQL. (Formerly "bootstrap"; the bootstrap verb now drives
// cluster provisioning per ADR-032.)
var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed example datasets",
	Long:  `Seeds a running DeviceChain instance with example datasets over GraphQL`,
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
	seedCmd.PersistentFlags().StringP("server", "s", "localhost", "server hostname targeted for remote calls")
	seedCmd.PersistentFlags().StringP("instance", "i", "dc1", "instance id targeted for remote calls")
	seedCmd.PersistentFlags().StringP("tenant", "t", "tenant1", "tenant id targeted for remote calls")

	rootCmd.AddCommand(seedCmd)
}
