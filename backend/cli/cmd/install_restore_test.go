// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// parseInstallRestoreFlags drives argv through the REAL install flag set and restores
// every variable it touched afterwards.
//
// The flags bind to package-level variables, so a test that set them directly would
// prove nothing about the flag NAMES — which are the operator's actual surface, and
// which a rename would change without any test noticing. Parsing real argv means
// `--restore-rdb-from` has to exist and has to be spelled that way. Unknown flags are
// a hard failure here for the same reason: cobra would otherwise report the error and
// the test would go on asserting against a variable nobody set.
func parseInstallRestoreFlags(t *testing.T, argv ...string) {
	t.Helper()
	saved := map[string]string{}
	for _, n := range []string{"restore-rdb-from", "restore-rdb-at"} {
		f := installCmd.Flags().Lookup(n)
		if f == nil {
			t.Fatalf("dcctl install has no --%s flag: the relational restore surface was "+
				"renamed or removed, and every assertion below would now be checking a "+
				"variable no operator can reach", n)
		}
		saved[n] = f.Value.String()
	}
	t.Cleanup(func() {
		for n, v := range saved {
			if err := installCmd.Flags().Set(n, v); err != nil {
				t.Fatalf("restoring --%s: %v", n, err)
			}
			installCmd.Flags().Lookup(n).Changed = false
		}
	})
	if err := installCmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parsing %v: %v", argv, err)
	}
}

// TestInstallRestoreFlagsReachTheResolverUnshuffled pins the wiring between argv and
// bootstrap.RestoreFlags.
//
// The resolver's own tests cover its rules thoroughly; none of them can see a mistake
// in the struct literal that FEEDS it. Distinct values per field are what make a
// transposed or dropped field visible.
func TestInstallRestoreFlagsReachTheResolverUnshuffled(t *testing.T) {
	parseInstallRestoreFlags(t,
		"--restore-rdb-from=rdb-source",
		"--restore-rdb-at=2026-07-26T01:02:03Z",
	)

	got := installRestoreFlagsFromArgv(true)

	for _, c := range []struct{ field, got, want string }{
		{"RdbFrom", got.RdbFrom, "rdb-source"},
		{"RdbTargetTime", got.RdbTargetTime, "2026-07-26T01:02:03Z"},
	} {
		if c.got != c.want {
			t.Errorf("RestoreFlags.%s = %q, want %q — the flag is landing in the wrong field",
				c.field, c.got, c.want)
		}
	}
	if !got.BackupsEnabled {
		t.Error("BackupsEnabled = false when the cluster archives")
	}
	if installRestoreFlagsFromArgv(false).BackupsEnabled {
		t.Error("BackupsEnabled = true on an install that leaves the cluster without backups")
	}
}

// 🔴 `dcctl install` MUST NOT FILL THE EVENT STORE'S HALF. RestoreFlags carries both
// stores, and the two halves are applied by two different OpenTofu roots: a value
// leaking into TsdbFrom here would be emitted as restore_tsdb_from, routed by
// splitVars to the INSTANCE root, and aimed at a store this command does not own.
//
// The two-store struct is what makes that reachable at all, so it is pinned rather
// than assumed.
func TestInstallNeverCarriesTheEventStoresRestore(t *testing.T) {
	parseInstallRestoreFlags(t, "--restore-rdb-from=rdb-source", "--restore-rdb-at=2026-07-26T01:02:03Z")

	got := installRestoreFlagsFromArgv(true)
	if got.TsdbFrom != "" || got.TsdbTargetTime != "" {
		t.Fatalf("dcctl install filled the EVENT store's restore fields (%+v). That store "+
			"belongs to an instance, and the value would be applied by the instance root.", got)
	}
	// ...and the flag itself is not offered here, so an operator cannot reach it either.
	for _, n := range []string{"restore-tsdb-from", "restore-tsdb-at"} {
		if installCmd.Flags().Lookup(n) != nil {
			t.Errorf("dcctl install offers --%s. The event store is an instance's; "+
				"`dcctl bootstrap` owns that flag", n)
		}
	}
}

// TestInstallArgvThatCannotWorkIsRefusedFromArgv walks the refusals end to end, from
// the flags an operator types to the error they are handed.
//
// The resolver's unit tests construct RestoreFlags directly, so all of them stay green
// if the wiring stops feeding it — or if RunE stops checking its error. This is the
// test that fails in that case, and each row is a combination knowable before a single
// object is created.
func TestInstallArgvThatCannotWorkIsRefusedFromArgv(t *testing.T) {
	for _, tc := range []struct {
		name      string
		argv      []string
		noBackups bool
		wantErr   string
	}{
		{
			name:    "a recovery target with nothing to recover",
			argv:    []string{"--restore-rdb-at=2026-07-27T13:59:00Z"},
			wantErr: "--restore-rdb-from",
		},
		{
			// The one that succeeds and is wrong if it gets through: PostgreSQL reads
			// an offsetless timestamp in the RECOVERING server's timezone, stops hours
			// from the named moment, and reports success.
			name:    "a target time with no offset",
			argv:    []string{"--restore-rdb-from=dc-rdb", "--restore-rdb-at=2026-07-27 13:59:00"},
			wantErr: "RFC3339",
		},
		{
			// The plugin that WRITES the archive is the plugin that READS it, so the
			// flags that switch backups off switch restore off with them.
			name:      "restoring on a cluster installed without backups",
			argv:      []string{"--restore-rdb-from=dc-rdb"},
			noBackups: true,
			wantErr:   "installed without it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parseInstallRestoreFlags(t, tc.argv...)
			_, err := bootstrap.ResolveRestorePlan(installRestoreFlagsFromArgv(!tc.noBackups))
			if err == nil {
				t.Fatalf("dcctl install %s was accepted", strings.Join(tc.argv, " "))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal for %v does not mention %q, so it is not the refusal "+
					"this row is pinning: %v", tc.argv, tc.wantErr, err)
			}
		})
	}
}

// 🔴 THE FLAG COMBINATION THAT REACHES THE REFUSAL BY ACCIDENT. Backups are not a
// flag an operator sets: they are a consequence of --no-cnpg, and of --compact, which
// turns --no-tls on by itself. So a perfectly reasonable-looking
// `dcctl install --compact --restore-rdb-from dc-rdb` asks for a recovery from an
// archive the cluster will have no plugin to read.
//
// This pins the derivation the command actually performs, not a hand-written truth
// table: the same three flags decide it in RunE.
func TestTheBackupsDerivationInstallPassesToTheResolver(t *testing.T) {
	for _, tc := range []struct {
		what                   string
		noCNPG, compact, noTLS bool
		wantRestorePossible    bool
	}{
		{"an ordinary install", false, false, false, true},
		{"--compact, which turns TLS off and takes cert-manager with it", false, true, true, false},
		{"--compact --no-tls=false, which keeps both", false, true, false, true},
		{"--no-cnpg, which skips the operator the plugin extends", true, false, false, false},
	} {
		t.Run(tc.what, func(t *testing.T) {
			parseInstallRestoreFlags(t, "--restore-rdb-from=dc-rdb")
			enabled := bootstrap.DatabaseBackupsEnabled(tc.noCNPG, tc.compact, tc.noTLS)
			_, err := bootstrap.ResolveRestorePlan(installRestoreFlagsFromArgv(enabled))
			if (err == nil) != tc.wantRestorePossible {
				t.Fatalf("restore possible = %v, want %v (err: %v)", err == nil, tc.wantRestorePossible, err)
			}
		})
	}
}

// 🔴 THE HAND-OFF NOTHING ELSE CAN SEE. bootstrap.Install needs a provider and a
// cluster, so no test reaches the options literal through the command itself — and a
// literal is exactly where a settled plan is dropped or lands in the wrong field.
//
// Dropped, the failure is silent in the worst direction: `dcctl install
// --restore-rdb-from` reports an ordinary, green install of an EMPTY relational store,
// during the recovery it was run for. The flags validate, the plan is built, the error
// is checked — and nothing restores.
func TestTheSettledRestorePlanReachesTheInstallEngine(t *testing.T) {
	parseInstallRestoreFlags(t, "--restore-rdb-from=dc-rdb", "--restore-rdb-at=2026-07-26T01:02:03Z")

	plan, err := bootstrap.ResolveRestorePlan(installRestoreFlagsFromArgv(true))
	if err != nil {
		t.Fatal(err)
	}
	opts := installOptions(nil, plan, bootstrap.ImageSource{})

	if opts.Restore != plan {
		t.Fatalf("the install engine was handed %+v, not the settled plan %+v", opts.Restore, plan)
	}
	if !opts.Restore.RestoresRelationalStore() {
		t.Fatal("the engine was handed a plan that restores nothing, so the apply would " +
			"initialise an EMPTY relational store and report success")
	}
}

// parseInstallImageFlags is parseInstallRestoreFlags for the operator's image source.
//
// Separate rather than folded in, because the flag set each one saves is the flag set
// it restores: a test that parsed --build through the restore helper would leave the
// package-level installBuild set for whatever ran next, and the failure would land in
// a test that never mentioned it.
func parseInstallImageFlags(t *testing.T, argv ...string) {
	t.Helper()
	saved := map[string]string{}
	for _, n := range []string{"registry", "version", "build"} {
		f := installCmd.Flags().Lookup(n)
		if f == nil {
			t.Fatalf("dcctl install has no --%s flag: the operator's image source was renamed "+
				"or removed, and a cluster could no longer be prepared at a chosen release", n)
		}
		saved[n] = f.Value.String()
	}
	t.Cleanup(func() {
		for n, v := range saved {
			if err := installCmd.Flags().Set(n, v); err != nil {
				t.Fatalf("restoring --%s: %v", n, err)
			}
			installCmd.Flags().Lookup(n).Changed = false
		}
	})
	if err := installCmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parsing %v: %v", argv, err)
	}
}

// 🔴 THE SAME HOLE, FOR THE OPERATOR'S IMAGE SOURCE. `dcctl install` deploys a
// workload now — the controller Deployment names an image — and installOptions is
// the struct literal that carries the settled source across into the engine.
//
// Dropped, --registry and --version are accepted, validated, printed in the plan, and
// then ignored: the cluster is prepared with whatever dcctl was compiled to default
// to. The install is green and the cluster is running a release nobody chose, which
// is the shape this arc exists to remove rather than relocate.
func TestTheSettledImageSourceReachesTheInstallEngine(t *testing.T) {
	img := bootstrap.ImageSource{Registry: "ghcr.io/example", Version: "v0.17.0"}
	opts := installOptions(nil, bootstrap.RestorePlan{}, img)

	if opts.ImageRegistry != img.Registry || opts.ImageVersion != img.Version {
		t.Fatalf("the install engine was told to deploy the operator from %q at %q, "+
			"not the settled source %q at %q", opts.ImageRegistry, opts.ImageVersion,
			img.Registry, img.Version)
	}
}

// And the developer path's own half of it. --build is read straight off the flag
// rather than out of the ImageSource — ResolveImageSource consumes it and does not
// carry it — so it is the one field of the three that a refactor can lose without
// the other two noticing.
func TestTheBuildFlagReachesTheInstallEngine(t *testing.T) {
	parseInstallImageFlags(t, "--build")

	img, err := bootstrap.ResolveImageSource(installRegistry, installVersion, installBuild)
	if err != nil {
		t.Fatal(err)
	}
	opts := installOptions(nil, bootstrap.RestorePlan{}, img)

	if !opts.BuildImages {
		t.Fatal("--build was dropped on the way to the install engine, so the operator " +
			"image would be pulled from a local registry nothing ever pushed to")
	}
}
