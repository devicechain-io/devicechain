// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"regexp"
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
	// Collapse runs of spaces before matching. `terraform fmt` ALIGNS the `=` of
	// adjacent arguments, so adding a longer argument name to one of these module
	// blocks silently rewrites its neighbours' whitespace — which is exactly what
	// happened when `restore` joined the event store's block and turned
	// `backup = ` into `backup  = `. The wiring was intact; only the literal moved.
	// Matching on the assignment rather than on its column keeps the test pointed
	// at the thing that matters.
	// 🔑 ACROSS EVERY ROOT, because the two stores no longer live in the same one:
	// the relational store is a cluster prerequisite and the event store is
	// per-instance. What this test asserts is unchanged — both stores are wired to a
	// destination — and it deliberately does not care which root does the wiring.
	main := ""
	for _, src := range rootSources(t, "main.tf") {
		main += regexp.MustCompile(`[ \t]+`).ReplaceAllString(src, " ") + "\n"
	}

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
//
// 🔴 AND AFTER THE ROOT SPLIT THE SAME CONJUNCTION IS RIGHT IN ONE ROOT AND WRONG
// IN THE OTHER, which is why this is two assertions rather than one.
//
// The CLUSTER root both installs the operator and creates the relational store, so
// asking "did I install the plugin?" about its own variable is exactly right there.
//
// The INSTANCE root does not install anything of the sort: it runs permanently in
// the mode enable_cnpg = false describes, "the cluster already runs CNPG". MEASURED
// on the pre-split tree: enable_database_backups = true with enable_cnpg = false
// evaluates backups_on to FALSE, which drops the ObjectStore, the ScheduledBackup
// and every archive setting on the Cluster. Carrying the conjunction across the
// split would therefore have turned every instance's event-store backups off, with
// the flag still reading true and nothing failing.
func TestTheOperatorConjunctionLivesOnlyWhereItIsTrue(t *testing.T) {
	const derivation = "backups_on = var.enable_database_backups && var.enable_cnpg"

	sources := rootSources(t, "main.tf")
	if !strings.Contains(sources[assets.ClusterRootDir], derivation) {
		t.Errorf("the cluster root no longer derives backups_on as %q.\n"+
			"  Both halves matter there: it installs the operator AND creates the relational\n"+
			"  store, so a destination provisioned for an absent plugin is a running MinIO, a\n"+
			"  20Gi volume, and no backups.", derivation)
	}
	if strings.Contains(sources[assets.InstanceRootDir], derivation) {
		t.Errorf("the instance root derives backups_on as %q, and it must not.\n"+
			"  That root never installs the operator, so it runs with enable_cnpg = false by\n"+
			"  construction and this conjunction evaluates FALSE on every install — dropping\n"+
			"  the event store's archiving silently while the flag still reads true.\n"+
			"  Whether the plugin exists is a question about the CLUSTER, and\n"+
			"  backup_prerequisite_guard is what asks it.", derivation)
	}
}

// 🔴 AND THE QUESTION THE CONJUNCTION USED TO ANSWER MUST STILL BE ASKED. Removing
// it from the instance root is only safe because something else checks that the
// plugin is actually installed — otherwise the fix for a silent no-backups bug is a
// different silent no-backups bug, one where the Cluster is created with archive
// settings nothing acts on.
//
// This is the assertion that fails if that guard is ever deleted as redundant.
func TestTheInstanceRootAsksTheClusterWhetherThePluginIsThere(t *testing.T) {
	src := rootSources(t, "main.tf")[assets.InstanceRootDir]

	for _, want := range []string{
		// The CRD the Barman Cloud plugin serves — its presence IS the plugin's.
		"objectstores.barmancloud.cnpg.io",
		// ...read at plan time, and refused as a precondition rather than reported.
		"backup_prerequisite_guard",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the instance root no longer contains %q.\n"+
				"  Without it nothing checks that the plugin performing the archiving exists:\n"+
				"  the event store would be created with archive settings nothing acts on —\n"+
				"  no WAL shipped, no base backup taken, no error — until a restore finds an\n"+
				"  empty archive.", want)
		}
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
