// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// parseBootstrapFlags drives argv through the REAL bootstrap flag set and
// restores every variable it touched afterwards.
//
// The flags bind to package-level variables, so a test that sets them directly
// would prove nothing about the flag NAMES — which are the operator's actual
// surface, and which a rename would change without any test noticing. Parsing
// real argv means `--restore-tsdb-from` has to exist and has to be spelled that
// way. Unknown flags are a hard failure here for the same reason: cobra would
// otherwise report the error and the test would go on asserting against a
// variable nobody set.
func parseBootstrapFlags(t *testing.T, argv ...string) {
	t.Helper()
	saved := map[string]string{}
	for _, n := range []string{
		"restore-tsdb-from", "restore-tsdb-at",
	} {
		f := bootstrapCmd.Flags().Lookup(n)
		if f == nil {
			t.Fatalf("dcctl bootstrap has no --%s flag: the restore surface was renamed "+
				"or removed, and every assertion below would now be checking a variable "+
				"no operator can reach", n)
		}
		saved[n] = f.Value.String()
	}
	// The booleans that decide whether backups (and therefore the restore path)
	// exist at all. Saved as values rather than through the flag set because
	// resolveCompactMode writes them directly.
	noCNPG, compact, noTLS := bootstrapNoCNPG, bootstrapCompact, bootstrapNoTLS
	t.Cleanup(func() {
		for n, v := range saved {
			if err := bootstrapCmd.Flags().Set(n, v); err != nil {
				t.Fatalf("restoring --%s: %v", n, err)
			}
			bootstrapCmd.Flags().Lookup(n).Changed = false
		}
		bootstrapNoCNPG, bootstrapCompact, bootstrapNoTLS = noCNPG, compact, noTLS
	})
	if err := bootstrapCmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parsing %v: %v", argv, err)
	}
}

// TestRestoreFlagsReachTheResolverUnshuffled pins the wiring between argv and
// bootstrap.RestoreFlags.
//
// The resolver's own tests cover its rules thoroughly; none of them can see a
// mistake in the struct literal that FEEDS it. Distinct values per field are what
// make a transposed or dropped field visible.
func TestRestoreFlagsReachTheResolverUnshuffled(t *testing.T) {
	parseBootstrapFlags(t,
		"--restore-tsdb-from=tsdb-source",
		"--restore-tsdb-at=2026-07-26T01:02:03Z",
	)

	got := restoreFlagsFromArgv()

	for _, c := range []struct{ field, got, want string }{
		{"TsdbFrom", got.TsdbFrom, "tsdb-source"},
		{"TsdbTargetTime", got.TsdbTargetTime, "2026-07-26T01:02:03Z"},
	} {
		if c.got != c.want {
			t.Errorf("RestoreFlags.%s = %q, want %q — the flag is landing in the wrong "+
				"field", c.field, c.got, c.want)
		}
	}
	if !got.BackupsEnabled {
		t.Error("BackupsEnabled = false on a default run, which has the plugin")
	}
}

// TestArgvThatCannotWorkIsRefusedFromArgv walks the refusals end to end, from
// the flags an operator types to the error they are handed.
//
// The resolver's unit tests construct RestoreFlags directly, so all of them
// stay green if the wiring stops feeding it — or if RunE stops checking its
// error. This is the test that fails in that case, and each row is a
// combination that is knowable before anything is built.
func TestArgvThatCannotWorkIsRefusedFromArgv(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		noCNPG  bool
		wantErr string
	}{
		{
			name:    "a recovery target with nothing to recover",
			argv:    []string{"--restore-tsdb-at=2026-07-27T13:59:00Z"},
			wantErr: "--restore-tsdb-from",
		},
		{
			// The one that succeeds and is wrong if it gets through: PostgreSQL reads
			// an offsetless timestamp in the RECOVERING server's timezone, stops hours
			// from the named moment, and reports success.
			name:    "a target time with no offset",
			argv:    []string{"--restore-tsdb-from=dc-tsdb", "--restore-tsdb-at=2026-07-27 13:59:00"},
			wantErr: "RFC3339",
		},
		{
			// --no-cnpg skips the operator the Barman plugin extends, so there is no
			// plugin to read the archive the restore names.
			name:    "restoring on a run with no database operator",
			argv:    []string{"--restore-tsdb-from=dc-tsdb"},
			noCNPG:  true,
			wantErr: "--no-cnpg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parseBootstrapFlags(t, tc.argv...)
			bootstrapNoCNPG = tc.noCNPG

			_, err := bootstrap.ResolveRestorePlan(restoreFlagsFromArgv())
			if err == nil {
				t.Fatalf("dcctl bootstrap %s was accepted", strings.Join(tc.argv, " "))
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("the refusal for %v does not mention %q, so it is not the refusal "+
					"this row is pinning: %v", tc.argv, tc.wantErr, err)
			}
		})
	}
}
