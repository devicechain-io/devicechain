// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// Build stamps, injected via ldflags by the two real build paths: the makefile
// (developer builds) and goreleaser (released binaries) — keep the two in sync.
// Every one of these is best-effort: a `go build ./...` or `go run .` that
// bypasses both leaves them empty, so `dcctl version` must render without them
// rather than print a confidently wrong answer.
var (
	// gitCommit is the full hash of HEAD at build time.
	gitCommit string

	// gitTreeState is "clean" or "dirty" — whether the working tree carried
	// uncommitted changes at build time. A dirty build's commit hash does not
	// identify its source, which is precisely when you most need to be told.
	gitTreeState string

	// buildDate is an RFC 3339 UTC timestamp of the build. `version` renders a
	// relative age from it: the stale-binary failure mode is invisible to a
	// version string alone, because a version can repeat across builds while an
	// age cannot.
	buildDate string
)

// Version is the build version, injected via ldflags at build time. For a
// release build this is the plain VERSION-file value (e.g. "0.0.1"); for any
// other build the makefile appends a "-dev.<utc-timestamp>" suffix, because the
// VERSION file is a slow-moving constant — pre-GA it has read 0.0.1 for the
// project's entire history, so without the suffix every dev build ever made
// reports an identical version and no two can be told apart.
//
// For dev builds this deliberately does NOT match bootstrap.DefaultImageVersion,
// which must stay a tag that resolves in a registry; see that value's docs.
// `version` prints both.
var Version = "dev"

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "dcctl",
	Short: "DeviceChain CLI",
	Long: color.HiGreenString(`
    ____            _           ________          _     
   / __ \___ _   __(_)_______  / ____/ /_  ____ _(_)___ 
  / / / / _ \ | / / / ___/ _ \/ /   / __ \/ __  / / __ \
 / /_/ /  __/ |/ / / /__/  __/ /___/ / / / /_/ / / / / /
/_____/\___/|___/_/\___/\___/\____/_/ /_/\__,_/_/_/ /_/ 
                                                        
Command line interface for interacting with DeviceChain components`),
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.dcctl.yaml)")

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
