// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"io/fs"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// Database backups (ADR-028, ADR-020 A2.5).
//
// 🔑 WHAT CHANGED, AND WHY THESE TESTS ARE NOT THE SAME AS cnpg_test.go's.
//
// Until A2.5, `enable_database_backups = true` installed the Barman Cloud PLUGIN
// and nothing else — no object store, no ObjectStore resources, no
// ScheduledBackup. Every test in cnpg_test.go was true and every one of them was
// satisfied by an instance that archived nothing anywhere. "Point-in-time
// recovery is possible in principle" and "this instance is being backed up" are
// different claims, and the flag only ever supported the first.
//
// So these tests pin the SECOND claim: that the flag reaches a destination. They
// are deliberately separate from the plugin tests rather than folded into them,
// because the failure they catch is the one that survived the whole first
// generation of those tests — a green flag with nothing behind it.

// A destination is not optional when backups are on. The variable is tri-state in
// spirit and two-valued by construction: there is no "backups on, destination
// none", because that state is precisely the one A2.5 removes.
func TestTheDefaultBootstrapGetsARealBackupDestination(t *testing.T) {
	got := effectiveInfraVar(t, compactState(false), "backup_destination")

	switch got {
	case "in-cluster", "external":
		t.Logf("a default bootstrap archives to the %q destination", got)
	default:
		t.Errorf("a default bootstrap resolves backup_destination to %q.\n"+
			"  Neither \"in-cluster\" nor \"external\" means nothing is provisioned to archive to,\n"+
			"  which puts the install back in the state A2.5 exists to remove: the plugin is\n"+
			"  present, database_backups_enabled reads true, and no WAL leaves the cluster.",
			got)
	}
}

// The wiring, pinned in the same shape as TestTheBackupFlagIsActuallyWiredToThePlugin
// and for the same reason: these are ordinary HCL lines nobody would think to
// look at, and deleting any of them leaves every flag-level assertion green while
// the instance stops being backed up.
//
// 🔴 BOTH stores, checked separately. One store backed up and the other not is a
// state nobody would choose on purpose and exactly what an edit to one of two
// near-identical blocks produces. The event store is the easier one to lose,
// because losing it breaks nothing an operator would notice.
func TestBothStoresAreActuallyWiredToABackupDestination(t *testing.T) {
	body, err := fs.ReadFile(assets.OpenTofu(), "main.tf")
	if err != nil {
		t.Fatalf("reading the embedded main.tf: %v", err)
	}
	main := string(body)

	for _, tc := range []struct {
		what  string
		line  string
		holds string
	}{
		{
			what:  "the relational store",
			line:  "backup = local.rdb_backup",
			holds: "tenants, users, devices, relationships and the audit journal",
		},
		{
			what:  "the event store",
			line:  "backup = local.tsdb_backup",
			holds: "all recorded device event history",
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if !strings.Contains(main, tc.line) {
				t.Errorf("the cnpg-cluster module call for %s no longer contains %q.\n"+
					"  That store holds %s, and without this line it has NO backups —\n"+
					"  it still runs, still replicates and still passes every health check.\n"+
					"  The difference only surfaces when someone tries to restore it.\n"+
					"  If the wiring was legitimately reformatted, update this string; do not delete the test.",
					tc.what, tc.line, tc.holds)
			}
		})
	}
}

// Backups need the plugin, the plugin needs the operator. Deriving that ONCE is
// what stops a configuration that provisions an object store and two ObjectStore
// resources for a plugin that was never installed — which does not fail, it just
// archives to a destination nothing writes to.
func TestBackupsAreDerivedFromTheOperatorToo(t *testing.T) {
	body, err := fs.ReadFile(assets.OpenTofu(), "main.tf")
	if err != nil {
		t.Fatalf("reading the embedded main.tf: %v", err)
	}

	const derivation = "backups_on = var.enable_database_backups && var.enable_cnpg"
	if !strings.Contains(string(body), derivation) {
		t.Errorf("main.tf no longer derives backups_on as %q.\n"+
			"  Both halves matter: --no-cnpg already sets enable_database_backups=false, so\n"+
			"  dropping the conjunction looks harmless today and stops being harmless the\n"+
			"  moment anything else turns the operator off. A destination provisioned for an\n"+
			"  absent plugin is a running MinIO, a 20Gi volume, and no backups.",
			derivation)
	}
}

// The compact preset sizes every volume it provisions, and the object store
// became one of those volumes when A2.5 gave the backup flag a destination.
//
// This is the same bug TimescaleStorage was added to fix, one slice later: shrink
// some of an install's volumes and leave one at its full-size default, and the
// preset's disk claim describes a fraction of the disk it actually uses. Here the
// gap is 20Gi against a preset that exists for small nodes.
func TestCompactSizesTheBackupDestination(t *testing.T) {
	vars := varsMap(t, compactState(true))

	got, ok := vars["backup_object_store_storage"]
	if !ok {
		t.Fatalf("--compact passes no backup_object_store_storage, so the in-cluster object " +
			"store takes its full-size default on a preset built for small nodes.\n" +
			"  Note --compact --no-tls does not provision one at all (it drops cert-manager, " +
			"and the plugin with it), so this is only visible on the TLS-keeping compact path.")
	}
	if got != compact.ObjectStoreStorage {
		t.Errorf("--compact passes backup_object_store_storage=%q but the preset says %q.\n"+
			"  Both must come from compactSizing, or the number the preset documents and the "+
			"volume it provisions are two different values.",
			got, compact.ObjectStoreStorage)
	}
}

// Turning backups off must leave nothing behind. The object store is gated on the
// same derived flag as the ObjectStore resources, so an install without backups
// does not carry a MinIO pod and a data volume for an archive nobody writes.
//
// 🔴 This is the assertion whose POSITIVE CONTROL matters: it is an implication,
// so a matrix in which backups are never off satisfies it while measuring
// nothing. That is the trap cnpg_test.go's own control was written for, and it
// applies here identically.
func TestTurningBackupsOffProvisionsNoDestination(t *testing.T) {
	reached := 0

	for name, st := range infraStates() {
		t.Run(name, func(t *testing.T) {
			vars := varsMap(t, st)
			if vars["enable_database_backups"] != "false" {
				return
			}
			reached++

			// A destination override on an install with no backups is not an error
			// — the object store is gated on backups_on, so nothing is provisioned
			// either way. What would be wrong is dcctl asking for one.
			if d, ok := vars["backup_destination"]; ok && d != "" {
				t.Errorf("this bootstrap turns database backups OFF but still asks for "+
					"backup_destination=%q.\n"+
					"  Nothing consumes it, which is the problem: it reads as a configured "+
					"destination on an instance that archives nothing.", d)
			}
		})
	}

	if reached == 0 {
		t.Error("no bootstrap in the matrix turned database backups off, so this assertion " +
			"was vacuously true and proved nothing. Either infraStates no longer covers the " +
			"path that drops them, or nothing drops them any more.")
	} else {
		t.Logf("the invariant was exercised on %d of the matrix's bootstraps", reached)
	}
}
