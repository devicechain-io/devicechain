// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/spf13/cobra"
)

// staleAfter is how old a build gets before `version` flags it. It is a nudge,
// not a policy: dcctl is built from a fast-moving pre-GA workspace, so a binary
// more than a week old is likely to disagree with the checkout it is run from —
// the failure this command exists to surface, where a months-stale dcctl on PATH
// deploys against a chart that has moved underneath it.
const staleAfter = 7 * 24 * time.Hour

var versionShort bool

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Get version info",
	Long: `Gets build information for the CLI: version, source commit, build date,
and the container image tag this binary deploys by default.

A dcctl built outside the makefile carries no build stamps; the fields it cannot
know are reported as unknown rather than guessed.`,
	Run: func(cmd *cobra.Command, args []string) {
		out := cmd.OutOrStdout()

		if versionShort {
			fmt.Fprintln(out, Version)
			return
		}

		fmt.Fprintf(out, "dcctl %s\n", Version)
		for _, f := range versionFields(time.Now()) {
			fmt.Fprintf(out, "  %-10s %s\n", f.label+":", f.value)
		}
	},
}

type versionField struct {
	label string
	value string
}

// versionFields renders the build stamps for display, tolerating any of them
// being absent.
func versionFields(now time.Time) []versionField {
	return []versionField{
		{"commit", describeCommit(gitCommit, gitTreeState)},
		{"built", describeBuildDate(buildDate, now)},
		{"images", bootstrap.DefaultImageVersion},
		{"go", runtime.Version()},
		{"platform", runtime.GOOS + "/" + runtime.GOARCH},
	}
}

// describeCommit renders the source commit and whether the tree was clean. The
// hash is abbreviated for reading; a dirty tree is called out because the hash
// then describes the build's ancestor rather than the build itself.
func describeCommit(commit, treeState string) string {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return "unknown (not built via the makefile)"
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	if strings.TrimSpace(treeState) == "dirty" {
		return short + " (dirty: built with uncommitted changes)"
	}
	return short
}

// describeBuildDate renders the build timestamp with a relative age. The age is
// the point of the command: an absolute UTC timestamp makes the reader do the
// subtraction, and the reader who most needs the answer is the one who has not
// thought to ask the question.
func describeBuildDate(stamp string, now time.Time) string {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return "unknown (not built via the makefile)"
	}
	built, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		// Show what we were given rather than swallow it: a malformed stamp is a
		// build-tooling bug, and hiding it would make that bug invisible.
		return stamp + " (unparsable timestamp)"
	}

	age := now.Sub(built)
	if age < 0 {
		// Clock skew between the build host and this one. The age is meaningless
		// but the timestamp still is not.
		return built.Format(time.RFC3339)
	}

	desc := fmt.Sprintf("%s (%s)", built.Format(time.RFC3339), humanizeAge(age))
	if age >= staleAfter {
		desc += " — stale; consider rebuilding"
	}
	return desc
}

// humanizeAge renders a duration at the coarsest unit that still says something
// useful.
func humanizeAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return plural(int(age.Minutes()), "minute")
	case age < 24*time.Hour:
		return plural(int(age.Hours()), "hour")
	default:
		return plural(int(age.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

func init() {
	rootCmd.AddCommand(versionCmd)

	versionCmd.Flags().BoolVar(&versionShort, "short", false,
		"print just the version string (for scripts)")
}
