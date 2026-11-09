// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// walkRunnable visits every runnable command in the tree.
func walkRunnable(c *cobra.Command, fn func(*cobra.Command)) {
	if c.Runnable() {
		fn(c)
	}
	for _, sub := range c.Commands() {
		walkRunnable(sub, fn)
	}
}

// allFlags is every flag the command accepts: its own, its persistent ones and every parent's.
func allFlags(c *cobra.Command) *pflag.FlagSet {
	c.InitDefaultHelpFlag()
	return c.Flags()
}

// 🔴 EVERY COMMAND THAT TARGETS AN INSTANCE'S PLATFORM API accepts the same selector flags.
// `sim create` took -i/--instance and -s/--server while `events failed` (and attr, deadletters,
// presence, product, tenant-deletion, test and device register) took only --server, so a
// script written for one failed on the next with "unknown shorthand flag: 'i'". A command that
// targets an instance is one that accepts --server; it must accept the whole set.
func TestEveryInstanceTargetingCommandAcceptsTheSameSelectorFlags(t *testing.T) {
	want := []struct{ name, shorthand string }{{"server", "s"}, {"instance", "i"}}
	seen := 0
	walkRunnable(rootCmd, func(c *cobra.Command) {
		fs := allFlags(c)
		if fs.Lookup("server") == nil {
			return
		}
		seen++
		for _, w := range want {
			f := fs.Lookup(w.name)
			switch {
			case f == nil:
				t.Errorf("`%s` accepts --server but not --%s", c.CommandPath(), w.name)
			case f.Shorthand != w.shorthand:
				t.Errorf("`%s`: --%s has shorthand %q, want %q", c.CommandPath(), w.name, f.Shorthand, w.shorthand)
			}
		}
	})
	// The walk must have found the commands it exists for, or it proves nothing.
	if seen < 8 {
		t.Fatalf("the walk found only %d instance-targeting commands; the tree changed shape or the walk is broken", seen)
	}
}

// The converse: a command that accepts --instance as a selector (every one except the ones
// where it means something else) must be able to say where the instance is.
func TestEverySelectorInstanceFlagComesWithServer(t *testing.T) {
	exempt := map[string]string{
		"dcctl ha verify":                       "names the instance the HA drill ran against, in the cluster",
		"dcctl instances break-glass mfa-reset": "names the instance whose pod runs the reset",
		"dcctl secrets escrow verify":           "names the instance an escrow artifact is checked against",
	}
	walkRunnable(rootCmd, func(c *cobra.Command) {
		fs := allFlags(c)
		if fs.Lookup("instance") == nil || fs.Lookup("server") != nil {
			return
		}
		if _, ok := exempt[c.CommandPath()]; !ok {
			t.Errorf("`%s` accepts --instance but not --server; add it, or exempt the command with the reason --instance means something else there", c.CommandPath())
		}
	})
}

func TestInstanceFlagSelectsTheRecordedHost(t *testing.T) {
	orig := readRecordedTarget
	t.Cleanup(func() { readRecordedTarget = orig })
	var asked string
	readRecordedTarget = func(_ context.Context, _, instance string) (string, bool, error) {
		asked = instance
		return "dc.example.com", true, nil
	}
	mk := func(args ...string) *cobra.Command {
		c := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		c.PersistentFlags().StringP("server", "s", "localhost", "")
		c.PersistentFlags().Bool("tls", false, "")
		addInstanceTargetFlags(c)
		if err := c.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		return c
	}

	server, tls, err := targetFlags(mk("-i", "prod"))
	if err != nil || server != "dc.example.com" || !tls || asked != "prod" {
		t.Fatalf("got %q %v %v (asked %q)", server, tls, err, asked)
	}
	// An explicit --server wins and the cluster is not read.
	asked = ""
	server, tls, err = targetFlags(mk("--instance", "prod", "--server", "10.0.0.9"))
	if err != nil || server != "10.0.0.9" || tls || asked != "" {
		t.Fatalf("explicit --server must win: %q %v %v (asked %q)", server, tls, err, asked)
	}
	// An explicit --tls=false wins over the declaration's.
	_, tls, _ = targetFlags(mk("-i", "prod", "--tls=false"))
	if tls {
		t.Error("an explicit --tls=false must win")
	}
	// Neither flag: today's behaviour, unchanged.
	server, tls, err = targetFlags(mk())
	if err != nil || server != "localhost" || tls {
		t.Fatalf("defaults changed: %q %v %v", server, tls, err)
	}
	// An instance with no readable host is an error that names the way out, never a quiet localhost.
	readRecordedTarget = func(context.Context, string, string) (string, bool, error) { return "", false, nil }
	if _, _, err := targetFlags(mk("-i", "ghost")); err == nil || !strings.Contains(err.Error(), "--server") {
		t.Errorf("want an error naming --server, got %v", err)
	}
	readRecordedTarget = func(context.Context, string, string) (string, bool, error) {
		return "", false, fmt.Errorf("boom")
	}
	if _, _, err := targetFlags(mk("-i", "ghost")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want the read error, got %v", err)
	}
}
