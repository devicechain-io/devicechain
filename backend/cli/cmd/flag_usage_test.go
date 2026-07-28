// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestFlagUsageCarriesNoBackquotes bans backquotes from every flag's usage
// string, across every command.
//
// WHY THIS IS WORTH A TEST. pflag does not treat a backquoted word as emphasis —
// UnquoteUsage reads it as the flag's VALUE PLACEHOLDER and prints it in place of
// the type name. So prose punctuation silently rewrites the flag's signature in
// `--help`, and nothing fails: the flag still parses, the tests still pass, and
// the only symptom is help output describing an interface the command does not
// have.
//
// Both instances this caught were exactly that, and neither was noticed by
// review:
//
//	--escrow-file dcctl destroy    (from "which `dcctl destroy` deletes")
//	--ha preferred                 (a BOOL flag, rendered as taking a value)
//
// The second is the worse one, because a bool flag has no value to give and the
// help was inviting an operator to pass one.
//
// A blanket ban rather than a smarter rule (single-token names, empty for bools)
// because nothing in this CLI has ever wanted the placeholder feature — every use
// so far has been a typographic accident, so the rule with no exceptions is the
// one that cannot rot. If a flag ever genuinely needs a custom placeholder, delete
// the assertion for it here deliberately rather than weakening this to a heuristic.
func TestFlagUsageCarriesNoBackquotes(t *testing.T) {
	checked := 0
	walkCommands(rootCmd, func(c *cobra.Command) {
		check := func(f *pflag.Flag) {
			checked++
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

	// The vacuity guard. A walk that visits no flags — a refactor that moves
	// registration behind an init this test does not trigger, a renamed root —
	// passes every assertion above by examining nothing, which is the failure this
	// whole file exists to prevent one instance of.
	if checked == 0 {
		t.Fatal("walked every command and found no flags at all, so this test asserted nothing")
	}
	t.Logf("checked %d flag registrations", checked)
}

func walkCommands(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walkCommands(sub, fn)
	}
}
