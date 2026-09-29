// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// `--backup-snapshot-class` reaches the install engine. A flag that parsed and was
// then dropped in the struct literal would install object-store base backups with no
// word that the request was ignored.
func TestTheSnapshotClassFlagReachesTheInstallEngine(t *testing.T) {
	f := installCmd.Flags().Lookup("backup-snapshot-class")
	if f == nil {
		t.Fatal("dcctl install has no --backup-snapshot-class flag")
	}
	saved := f.Value.String()
	t.Cleanup(func() {
		if err := installCmd.Flags().Set("backup-snapshot-class", saved); err != nil {
			t.Fatalf("restoring --backup-snapshot-class: %v", err)
		}
		f.Changed = false
	})
	if err := installCmd.Flags().Parse([]string{"--backup-snapshot-class=pd-snapshots"}); err != nil {
		t.Fatal(err)
	}

	opts := installOptions(nil, bootstrap.RestorePlan{}, bootstrap.ImageSource{})
	if opts.BackupSnapshotClass != "pd-snapshots" {
		t.Errorf("the install engine was handed class %q, not the flag's %q", opts.BackupSnapshotClass, "pd-snapshots")
	}
}
