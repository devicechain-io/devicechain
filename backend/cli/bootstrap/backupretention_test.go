// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"regexp"
	"slices"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
)

// Each store keeps its own recovery window: the relational store 30 days, the
// event store 7.
//
// The window a store gets is found the way the apply finds it: the variable its
// root actually wires into that store's backup block, resolved the way dcctl
// resolves it (an explicit -var wins, else the OpenTofu default). A test that
// read a variable by NAME would pass while the block pointed at another one.
//
// Why the two differ: the relational store is the one an instance cannot be
// rebuilt without, and the damage it is restored from -- a bad migration, a
// mistaken delete -- is often found days later. The event store's history is
// bulk, has its own lifecycle, and its archive is what fills the backup store.
func TestEachStoreKeepsItsOwnRecoveryWindow(t *testing.T) {
	st := compactState(false)

	for _, tc := range []struct {
		store string
		root  string
		local string
		want  string
	}{
		{"the relational store", assets.ClusterRootDir, "rdb_backup", "30d"},
		{"the event store", assets.InstanceRootDir, "tsdb_backup", "7d"},
	} {
		t.Run(tc.store, func(t *testing.T) {
			variable := retentionVariableWiredInto(t, tc.root, tc.local)
			if got := effectiveInfraVar(t, st, variable); got != tc.want {
				t.Errorf("%s: the %s root wires var.%s into local.%s, which resolves to %q; want %q",
					tc.store, tc.root, variable, tc.local, got, tc.want)
			}
		})
	}
}

// retentionVariableWiredInto returns the variable named on the
// `retention_policy = var.X` line inside `<local> = local.backups_on ? { ... } : null`
// in root's main.tf. It fails when the block or the line is missing, never
// returning "" to match nothing.
func retentionVariableWiredInto(t *testing.T, root, local string) string {
	t.Helper()
	line := regexp.MustCompile(`\n retention_policy = var\.([a-z0-9_]+)\n`).FindStringSubmatch(backupLocalBody(t, root, local))
	if line == nil {
		t.Fatalf("local.%s in the %s root does not set retention_policy from a variable",
			local, root)
	}
	return line[1]
}

// Each root declares the window of the store it owns and not the other's, and no
// root declares one window for both. A single name, `backup_retention`, declared
// once per root, made each root's setting read as governing both databases --
// its description said so -- while it reached only one.
func TestEachRootDeclaresOnlyItsOwnStoresWindow(t *testing.T) {
	for _, tc := range []struct {
		variable string
		want     []string
	}{
		{"backup_retention_rdb", []string{assets.ClusterRootDir}},
		{"backup_retention_tsdb", []string{assets.InstanceRootDir}},
		{"backup_retention", nil},
	} {
		got := rootNamesHolding(t, "variables.tf", "\nvariable \""+tc.variable+"\" {")
		if !slices.Equal(got, tc.want) {
			t.Errorf("variable %q is declared in %v; want %v", tc.variable, got, tc.want)
		}
	}
}

// backupLocalBody returns the body of `<local> = local.backups_on ? { ... } : null`
// in root's main.tf, with runs of blanks squashed to one space and a newline added
// at each end so every line can be matched as `\n <line>\n`. It fails when the
// block is missing, never returning "" to match nothing.
func backupLocalBody(t *testing.T, root, local string) string {
	t.Helper()

	src, ok := rootSources(t, "main.tf")[root]
	if !ok {
		t.Fatalf("the %s root has no main.tf", root)
	}
	// `tofu fmt` realigns `=` whenever a neighbouring argument's name changes
	// length, so the match is on the assignment, not on its column.
	src = regexp.MustCompile(`[ \t]+`).ReplaceAllString(src, " ")

	block := regexp.MustCompile(`(?s)\n ` + regexp.QuoteMeta(local) +
		` = local\.backups_on \? \{\n(.*?)\n \} : null`).FindStringSubmatch(src)
	if block == nil {
		t.Fatalf("the %s root's main.tf has no `%s = local.backups_on ? { ... } : null` block",
			root, local)
	}
	return "\n" + block[1] + "\n"
}
