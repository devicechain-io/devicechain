// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestFlagUsageCarriesNoBackquotes bans backquotes from every flag usage string
// in dcctl's cobra tree.
//
// WHY THIS IS WORTH A TEST. pflag does not treat a backquoted word as emphasis —
// UnquoteUsage reads it as the flag's VALUE PLACEHOLDER and prints it in place of
// the type name. So prose punctuation silently rewrites the flag's signature in
// `--help`, and nothing fails: the flag still parses, the tests still pass, and
// the only symptom is help output describing an interface the command does not
// have.
//
// Two instances motivated it. One was live on main and had survived review:
//
//	--escrow-file dcctl destroy    (from "which `dcctl destroy` deletes")
//
// The other was caught in an unmerged draft, and is the worse shape — a BOOL flag
// rendered as `--ha preferred`, inviting an operator to pass a value that does not
// exist. It never reached main, so this test did not find it; it is recorded
// because it is the reason the ban is blanket rather than a heuristic.
//
// SCOPE, stated exactly, because the first version of this comment claimed more
// than the code delivers. This walks `rootCmd` and covers pflag usages in dcctl
// ONLY. It does not and cannot see:
//
//   - Go's stdlib `flag`, which has the IDENTICAL rule (flag.UnquoteUsage). Seven
//     usages in dc-simulator and migrationdiff were rendering `-handshake dcctl sim
//     create` and `-container docker exec` at the time this was written; they are
//     fixed, but nothing mechanical stops them coming back. Those are separate
//     modules with their own `main`s, so closing that class needs a repo-wide check
//     (hack/ + a CI step), not a test here.
//   - cobra's auto-generated `help`/`completion` commands and `-h`, which are added
//     during Execute() and so are absent from this pre-Execute snapshot. Cobra owns
//     those strings, so the risk is nil — but "every command" was the wrong words.
//
// A blanket ban rather than a smarter rule (single-token names, empty for bools)
// because nothing in this CLI has ever wanted the placeholder feature — every use
// so far has been a typographic accident, so the rule with no exceptions is the
// one that cannot rot. If a flag ever genuinely needs a custom placeholder, delete
// the assertion for it here deliberately rather than weakening this to a heuristic.
func TestFlagUsageCarriesNoBackquotes(t *testing.T) {
	checked := 0
	perCommand := map[string]int{}
	walkCommands(rootCmd, func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			checked++
			perCommand[c.CommandPath()]++
			if !strings.Contains(f.Usage, "`") {
				return
			}
			name, _ := pflag.UnquoteUsage(f)
			t.Errorf("%s: flag --%s has a backquote in its usage string, so pflag renders it as "+
				"the value placeholder: help shows %q instead of the type name. Use straight "+
				"quotes for prose emphasis.\n  usage: %s",
				c.CommandPath(), f.Name, "--"+f.Name+" "+name, f.Usage)
		}
		c.Flags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
	})

	// THE COVERAGE GUARD, and `checked == 0` is not it.
	//
	// A total wipeout is the easy case. The failure that actually costs something is
	// PARTIAL: move `rootCmd.AddCommand(bootstrapCmd)` behind a build tag or a
	// lazily-registered group and the walk still visits ~39 flags, every assertion
	// above still runs, and the test still passes green — while asserting nothing
	// about the one command that has ever carried this bug. Both motivating defects
	// were on `bootstrap`.
	//
	// So the guard names the commands that must be reachable and requires each to
	// carry flags. It is a deliberate tripwire, not a heuristic: deleting or renaming
	// one of these SHOULD fail here and be updated on purpose, because "the command
	// this test was written for silently left the tree" is precisely the event no
	// count threshold can distinguish from ordinary churn.
	//
	// These are LEAF paths, not parents. `dcctl secrets` and `dcctl ha` carry no
	// flags of their own — the first draft of this list named them and failed, which
	// is the guard demonstrating it can tell "not covered" from "covered".
	mustReach := []string{
		"dcctl bootstrap",
		"dcctl destroy",
		"dcctl ha verify",
		"dcctl ha verify-db",
		"dcctl secrets escrow verify",
		"dcctl sim create",
	}
	for _, path := range mustReach {
		if perCommand[path] == 0 {
			t.Errorf("the walk reached no flags for %q. Either that command left the "+
				"command tree, or its flags are now registered somewhere this walk does "+
				"not run — in both cases this test stopped covering it silently, which is "+
				"the failure mode it exists to prevent.\n  reached: %v", path, sortedKeys(perCommand))
		}
	}
	if checked == 0 {
		t.Fatal("walked every command and found no flags at all, so this test asserted nothing")
	}
	t.Logf("checked %d flag registrations across %d commands", checked, len(perCommand))
}

// sortedKeys returns the map's keys in a stable order, so a failure message lists
// what the walk DID reach rather than a differently-shuffled set each run.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func walkCommands(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walkCommands(sub, fn)
	}
}
