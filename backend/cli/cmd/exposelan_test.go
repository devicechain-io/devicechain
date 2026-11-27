// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/devicechain-io/dcctl/bootstrap"
)

func resetBoolFlag(t *testing.T, c *cobra.Command, name string) {
	t.Helper()
	t.Cleanup(func() {
		f := c.Flags().Lookup(name)
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
}

// --expose-lan is off unless asked for, and reaches the cluster step from install and from
// setup local alike.
func TestExposeLANFlagReachesTheClusterStep(t *testing.T) {
	for _, c := range []*cobra.Command{installCmd, setupLocalCmd} {
		if f := c.Flags().Lookup("expose-lan"); f == nil || f.DefValue != "false" {
			t.Fatalf("%s: --expose-lan missing or not off by default", c.CommandPath())
		}
	}
	resetBoolFlag(t, installCmd, "expose-lan")
	if err := installCmd.ParseFlags([]string{"--expose-lan"}); err != nil {
		t.Fatal(err)
	}
	opts, err := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.ExposeLAN {
		t.Fatal("install options dropped --expose-lan")
	}

	resetBoolFlag(t, setupLocalCmd, "expose-lan")
	if err := setupLocalCmd.ParseFlags([]string{"--expose-lan"}); err != nil {
		t.Fatal(err)
	}
	if c := setupInstallChoices(setupLocalCmd.Flags()); !c.ExposeLAN || !c.Given.Has("expose-lan") {
		t.Fatal("setup local dropped --expose-lan")
	}
}
