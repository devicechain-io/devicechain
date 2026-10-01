// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// `--database-node-selector` and `--database-toleration` reach the install engine. A
// flag that parsed and was then dropped in the struct literal would install the
// databases wherever the scheduler put them, with no word that the request was ignored.
func TestThePlacementFlagsReachTheInstallEngine(t *testing.T) {
	for _, name := range []string{"database-node-selector", "database-toleration"} {
		f := installCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("dcctl install has no --%s flag", name)
		}
	}
	t.Cleanup(func() {
		installDBNodeSelector, installDBTolerations = nil, nil
		for _, name := range []string{"database-node-selector", "database-toleration"} {
			installCmd.Flags().Lookup(name).Changed = false
		}
	})
	if err := installCmd.Flags().Parse([]string{
		"--database-node-selector=devicechain.io/pool=database",
		"--database-node-selector", "disk=ssd",
		"--database-toleration=dedicated=database:NoSchedule",
	}); err != nil {
		t.Fatal(err)
	}

	placement, err := installPlacementFromArgv()
	if err != nil {
		t.Fatal(err)
	}
	opts := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{}, placement)
	want := bootstrap.DatabasePlacement{
		NodeSelector: "devicechain.io/pool=database,disk=ssd",
		Tolerations:  "dedicated=database:NoSchedule",
	}
	if opts.DatabasePlacement != want {
		t.Errorf("the install engine was handed %+v, not the flags' %+v", opts.DatabasePlacement, want)
	}

	// A toleration with nothing to place is refused from argv.
	installDBNodeSelector = nil
	if _, err := installPlacementFromArgv(); err == nil || !strings.Contains(err.Error(), "--database-node-selector") {
		t.Errorf("a toleration with no selector: %v, want a refusal naming the missing flag", err)
	}
}
