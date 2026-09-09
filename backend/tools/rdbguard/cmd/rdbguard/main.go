// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command rdbguard runs one of two source checks about how code reaches the relational
// database, over the given roots.
//
//	rdbguard -check=index-helpers backend=400 deploy=1
//	rdbguard -check=bare-table    backend=400 deploy=1
//
// Each argument is a root, optionally with the minimum number of non-test Go files that
// root must yield.
//
// Exit codes are three-valued on purpose:
//
//	0  the tree is clean, and the scan proved it read enough to say so
//	1  findings — the tree breaks the rule
//	2  the INSTRUMENT is broken: a root that read too little, a parse error, an
//	   allow-list entry that matched nothing, bad arguments
//
// 🔴 2 IS NOT 1, AND IT IS NOT 0. A scan that read nothing has no opinion about the tree;
// reporting that as "clean" is an absence read as an answer, which is the failure mode
// these checks exist to prevent in the code they scan. Reporting it as a finding would be
// just as wrong in the other direction — it would send a reader looking for a violation
// that is not there.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	rdbguard "github.com/devicechain-io/dc-rdbguard"
)

type check struct {
	scan      func(roots ...string) (rdbguard.Result, error)
	allowKeys func() []string
	remedy    string
}

var checks = map[string]check{
	"index-helpers": {
		scan:      rdbguard.IndexHelperScan,
		allowKeys: rdbguard.IndexHelperAllowKeys,
		remedy: "core/rdb's partial-unique-index helpers are TEST FIXTURES. An index name and its\n" +
			"WHERE predicate are schema, so a migration that takes either from another module is\n" +
			"rewritten whenever that module changes: fresh installs build one index, every\n" +
			"existing database keeps another, and both report a clean migration.\n" +
			"Declare the index inside the migration itself, against the migration's own snapshot\n" +
			"struct and a literal index name. Six areas already do; copy one of those.",
	},
	"bare-table": {
		scan:      rdbguard.BareTableScan,
		allowKeys: rdbguard.BareTableAllowKeys,
		remedy: "A statement that names its table as a string is not tenant-scoped. Either it carries\n" +
			"no parseable schema, which the tenant-scope callback refuses outright, or it parses\n" +
			"into a shape with no TenantId — a projection struct, or a Model()/Table() mismatch —\n" +
			"which classifies as NOT tenant-scoped, injects no predicate, and returns every\n" +
			"tenant's rows with a nil error.\n" +
			"Pass the model instead: db.WithContext(ctx).Model(&Thing{}) or .Find(&[]Thing{}).",
	},
}

func main() {
	name := flag.String("check", "", "which check to run: "+strings.Join(checkNames(), ", "))
	// Off only for the self-tests, which scan fixture directories that cannot contain
	// the real allow-listed files. It is NEVER off for a run over the repository — see
	// the liveness note below.
	strictAllowList := flag.Bool("strict-allowlist", true,
		"fail when an allow-list entry matched nothing (off only for fixture scans)")
	flag.Parse()

	c, found := checks[*name]
	if !found {
		fmt.Fprintf(os.Stderr, "usage: rdbguard -check={%s} <root>[=minfiles] [root[=minfiles]...]\n",
			strings.Join(checkNames(), "|"))
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "rdbguard: no roots given, so there is nothing to check")
		os.Exit(2)
	}

	// 🔴 THE MINIMUM IS PER ROOT, NOT A TOTAL. A single total is satisfied by whichever
	// root happens to be large: against ~900 files a floor of 500 is cleared by
	// backend/core alone, so every service directory could vanish and the total would
	// still pass. The failure a floor exists to catch — a root that stopped being read —
	// is exactly the one a total hides.
	roots := make([]string, 0, flag.NArg())
	minFor := map[string]int{}
	for _, arg := range flag.Args() {
		root, minStr, hasMin := strings.Cut(arg, "=")
		min := 1
		if hasMin {
			n, err := strconv.Atoi(minStr)
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "rdbguard: bad minimum in %q\n", arg)
				os.Exit(2)
			}
			min = n
		}
		roots = append(roots, root)
		minFor[root] = min
	}

	res, err := c.scan(roots...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rdbguard: %v\n", err)
		os.Exit(2)
	}

	broken := false
	for _, root := range roots {
		if got := res.PerRoot[root]; got < minFor[root] {
			fmt.Fprintf(os.Stderr,
				"rdbguard: parsed %d non-test Go files under %q, expected at least %d — that root "+
					"is not being read, so a clean result from it means nothing\n",
				got, root, minFor[root])
			broken = true
		}
	}

	// 🔴 THE LIVENESS CHECK, and it is the half of this instrument that a green tick
	// cannot otherwise distinguish from a broken one. Zero findings is what a clean tree
	// reports AND what a scan that matches nothing reports. A working scan also lands on
	// every allow-list entry at least once, because each entry describes source that is
	// known to be present. An entry that absorbed nothing therefore means one of two
	// things, both of which invalidate the run: the matcher has gone blind, or the code
	// the rule was derived from has moved and the rule has not been re-derived.
	if *strictAllowList {
		for _, key := range c.allowKeys() {
			if res.Allowed[key] == 0 {
				fmt.Fprintf(os.Stderr,
					"rdbguard: allow-list entry %q matched nothing. Either the scan is no longer "+
						"seeing what it thinks it sees, or that source has moved — in both cases a "+
						"clean result from this run is not evidence of anything.\n", key)
				broken = true
			}
		}
	}
	if broken {
		os.Exit(2)
	}

	for _, f := range res.Findings {
		fmt.Println(f)
	}
	if len(res.Findings) > 0 {
		fmt.Fprintf(os.Stderr, "\nrdbguard(%s): %d finding(s).\n%s\n", *name, len(res.Findings), c.remedy)
		os.Exit(1)
	}

	names := make([]string, 0, len(res.PerRoot))
	for r := range res.PerRoot {
		names = append(names, r)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, r := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", r, res.PerRoot[r]))
	}
	fmt.Printf("rdbguard(%s): %d non-test files parsed (%s), %d allow-list entr(ies) live, no findings.\n",
		*name, res.Files(), strings.Join(parts, " "), len(c.allowKeys()))
}

func checkNames() []string {
	out := make([]string, 0, len(checks))
	for n := range checks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
