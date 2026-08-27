// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command apiprobe writes one of every entity the platform's tenant API can
// create, and later proves that every one of them still reads back the way it
// was written.
//
// # WHY IT EXISTS
//
// Two gaps, and one tool closes both because they are the same measurement taken
// at two moments.
//
// The first is BREADTH. The load-test harness drives one path very hard —
// ingest, then detection, then a command round trip — with real oracles, and it
// has found real platform defects. What it does not do is touch most of the API:
// device types and their versioned profiles, the relationship graph, groups,
// customers, areas, assets, dashboards, connectors, notification policies. A
// release can break `createDeviceProfile` outright and every existing gate stays
// green, because nothing else asks for one.
//
// The second is UPGRADE. `hack/migration-diff.sh verify` compares
// `pg_dump --schema-only`, so it captures no ROWS — a migration that creates
// every column correctly and rewrites, truncates or drops data passes with every
// area green. The only way to see that is to write rows through the real API
// before an upgrade and read them back through the real API after it.
//
// # WHAT IT PROVES, AND WHAT IT DOES NOT
//
// It proves: every entity this tool knows how to create was created by the
// deployed platform, survived whatever happened between seed and verify, and is
// still returned by the same query with the same field values.
//
// It does NOT prove anything about an entity it does not know how to create. The
// table in entities.go is therefore the coverage claim, and it is written as
// DATA precisely so that a missing entity is an absent row somebody can count
// rather than absent code nobody can see. `apiprobe coverage` prints it.
//
// 🔴 AND UNTIL `readsweep` IT PROVED NOTHING ABOUT ANYTHING DERIVED FROM A WRITE.
// Every read-back in the table is "fetch the row I just wrote, by its token" — 24
// queries against a served surface of about 124. So the claim was always the
// narrower one: EVERY ROW YOU WROTE READS BACK UNCHANGED, not "the instance still
// works". #838 sat in that gap. The geometry archive shipped with no backfill, an
// upgraded instance stopped matching geofence rules, and the fence ROW was
// untouched — so the round trip was spotless and the drill passed with the defect
// live in its database. Seeding another entity would not have helped: a round trip
// of one's own writes cannot see a derived artifact go stale. readsweep.go is the
// other half, and it derives its list from the served schema rather than a table,
// because the hand-written half is the half that was complete and still missed it.
//
// It says nothing about the device plane. Telemetry, presence, command delivery
// and detection all belong to the load harness, which already has oracles for
// them; duplicating those here would produce a second, weaker set.
//
// # THE RULE THIS IS BUILT AROUND
//
// Borrowed from the A0 HA rig and the A5 restore drill, and it is why the exit
// codes below are an interface rather than an implementation detail: a check is
// worth nothing until it has been shown to FAIL. A verify that reports success
// against an instance where a seeded row was deleted is not a check, and the only
// way a rig can tell the difference is if "the row is gone" exits differently
// from "the API would not answer".
//
// That distinction is the whole reason exitMissing, exitMismatch, exitShape and
// exitUnreadable are four codes and not one. They correspond to four genuinely
// different upgrade defects — data dropped, data rewritten, a field removed from
// the schema, and a stored shape the new release can no longer make sense of —
// and an upgrade rig that cannot tell them apart would report the third as the
// first and send somebody hunting through migrations for a resolver change.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// Exit codes. These ARE the interface: hack/upgrade-rig.sh sources them from
// `apiprobe codes` rather than hard-coding the numbers, so that renumbering one
// cannot leave a negative control silently asserting the wrong outcome forever.
const (
	exitOK = 0

	// exitSetup is every INCONCLUSIVE outcome: bad flags, an unreachable
	// ingress, a login that failed, a tenant that does not exist. It is never a
	// verdict about the data, and a negative control that produces it has proved
	// nothing — it would exit 1 just as happily against a perfectly good
	// instance, or a broken one.
	exitSetup = 1

	// exitMissing means the entity the receipt records is NOT THERE. This is the
	// expected result of the rig's deletion control, and the signature of a
	// migration that dropped rows.
	exitMissing = 2

	// exitMismatch means the entity is there and a field differs from what was
	// written. The signature of a migration that rewrote data — the failure that
	// a schema-only diff cannot see at all.
	exitMismatch = 3

	// exitRefused means a CREATE was refused during seed. Distinct from the two
	// above because it is a verdict about the API rather than about the data: a
	// validation rule that tightened, a required field that appeared, an input
	// that was renamed.
	exitRefused = 4

	// exitShape means the read-back QUERY itself was rejected — a field this
	// tool selects no longer exists on the type. Distinct from exitMissing
	// because the row may be perfectly intact; it is the SCHEMA that moved, and
	// reporting that as missing data would send a reader into the migrations
	// looking for something that was never there.
	exitShape = 5

	// exitUnreadable means a door the platform SERVES returned an error after the
	// upgrade. Distinct from every code above: the row is present and unchanged (verify
	// says so), the query is valid against the served schema (the sweep built it FROM
	// that schema), and a client still cannot read it. That combination points at a
	// stored shape the new release can no longer make sense of — a snapshot naming rows
	// that were never migrated, a document decoded into a struct that moved — which is
	// the one class an upgrade introduces and a fresh install cannot.
	exitUnreadable = 6
)

const usage = `apiprobe — write one of every creatable entity, then prove it reads back unchanged.

  apiprobe seed     --instance <id> --receipt <path> [flags]
        Create one of every entity in the coverage table, with deterministic
        literal values, and write the receipt verify will read.
        --baseline-schemas <dir>  a backend/services tree from the release being
        seeded; entities that release cannot express are SKIPPED by name instead
        of being refused mid-run. This is what lets a drill seed an OLD version
        with a table built from the new one.

  apiprobe verify   --receipt <path> [flags]
        Read every entity on the receipt back through the same API and compare
        it field by field.

  apiprobe readsweep --receipt <path> --schemas <dir> [flags]
        Call every door the DEPLOYED release's schemas serve that this tool can
        supply arguments for, and require that none ERRORS. Wider and shallower
        than verify: verify proves the rows you wrote are unchanged, this proves
        nothing the platform serves about them has become unreadable. The list is
        derived from the served SDL, so a door a later release adds is swept
        without anyone remembering it; a door that is skipped is named, with the
        reason.

  apiprobe tamper   --receipt <path> --mode delete|modify [flags]
        THE NEGATIVE CONTROL. Break one seeded entity on purpose — delete it, or
        rewrite a field verify compares — and confirm through verify's own query
        that the damage is visible. A verify that has never been shown to fail is
        not a check, and this is what shows it.

  apiprobe coverage [--baseline-schemas <dir>]
        Print the coverage table: which entities this tool creates, which service
        each belongs to, and what the controls break. This is the tool's coverage
        CLAIM — read it before believing a green run means the API is covered.
        With a baseline, also print what a drill from that release would SKIP.

  apiprobe areas
        Print the functional areas the table writes to, one per line, so a rig can
        deploy them. Not every one is in the 'default' profile.

  apiprobe codes
        Print the exit codes as shell assignments, for a rig to source.

Common flags:
  --server        instance ingress host (and :port)          (default localhost)
  --scheme        http or https                              (default http)
  --tenant        tenant token to write under                (default apiprobe)
  --admin-email   superuser identity that creates the tenant
  --admin-password
`

func printExitCodes() {
	fmt.Printf("APIPROBE_EXIT_OK=%d\n", exitOK)
	fmt.Printf("APIPROBE_EXIT_SETUP=%d\n", exitSetup)
	fmt.Printf("APIPROBE_EXIT_MISSING=%d\n", exitMissing)
	fmt.Printf("APIPROBE_EXIT_MISMATCH=%d\n", exitMismatch)
	fmt.Printf("APIPROBE_EXIT_REFUSED=%d\n", exitRefused)
	fmt.Printf("APIPROBE_EXIT_SHAPE=%d\n", exitShape)
	fmt.Printf("APIPROBE_EXIT_UNREADABLE=%d\n", exitUnreadable)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(exitSetup)
	}

	var err error
	switch os.Args[1] {
	case "seed":
		err = runSeed(ctx, os.Args[2:])
	case "verify":
		err = runVerify(ctx, os.Args[2:])
	case "readsweep":
		err = runReadSweep(ctx, os.Args[2:])
	case "tamper":
		err = runTamper(ctx, os.Args[2:])
	case "coverage":
		err = runCoverage(os.Args[2:])
	case "areas":
		printAreas()
		return
	case "codes":
		printExitCodes()
		return
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(exitSetup)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\napiprobe %s: %v\n", os.Args[1], err)
		os.Exit(codeOf(err))
	}
}

func flagSetFor(name string) *flag.FlagSet {
	return flag.NewFlagSet("apiprobe "+name, flag.ContinueOnError)
}

func runCoverage(argv []string) error {
	fs := flagSetFor("coverage")
	var baselineDir string
	fs.StringVar(&baselineDir, "baseline-schemas", "",
		"also report which entities a seed against this `backend/services` tree would skip")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	var base *baseline
	if strings.TrimSpace(baselineDir) != "" {
		var err error
		if base, err = loadBaseline(baselineDir); err != nil {
			return err
		}
	}
	printCoverage(base)
	return nil
}
