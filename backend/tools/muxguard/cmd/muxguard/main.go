// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command muxguard reports registrations on net/http's package-global
// http.DefaultServeMux under the given roots. It exits 1 when it finds any, and 2 when
// it cannot do its job.
//
// Each argument is a root, optionally with the minimum number of Go files that root must
// yield: `backend=400 deploy=1`.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-muxguard"
)

func main() {
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: muxguard <root>[=minfiles] [root[=minfiles]...]")
		os.Exit(2)
	}

	// 🔴 THE MINIMUM IS PER ROOT, NOT A TOTAL. A single total is satisfied by whichever
	// root happens to be large: against ~900 files a floor of 500 would still pass with
	// every service directory deleted, because core alone clears it. The failure the
	// floor exists to catch — a root that stopped being read — is exactly the one a
	// total hides.
	roots := make([]string, 0, flag.NArg())
	minFor := map[string]int{}
	for _, arg := range flag.Args() {
		root, minStr, hasMin := strings.Cut(arg, "=")
		min := 1
		if hasMin {
			n, err := strconv.Atoi(minStr)
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "muxguard: bad minimum in %q\n", arg)
				os.Exit(2)
			}
			min = n
		}
		roots = append(roots, root)
		minFor[root] = min
	}

	res, err := muxguard.Scan(roots...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muxguard: %v\n", err)
		os.Exit(2)
	}

	// 🔴 THE VACUITY GUARD, AND IT EXITS 2 RATHER THAN 1 ON PURPOSE. A scan that read
	// nothing has no opinion about the tree, and reporting that as "clean" is the exact
	// failure this repository keeps hitting: an absence read as an answer. A wrong root,
	// a renamed directory or a filter that stopped matching all land here. Exit 2 says
	// the instrument is broken, which is a different thing from the tree being dirty.
	short := false
	for _, root := range roots {
		if got := res.PerRoot[root]; got < minFor[root] {
			fmt.Fprintf(os.Stderr,
				"muxguard: parsed %d Go files under %q, expected at least %d — that root is not "+
					"being read, so a clean result from it means nothing\n",
				got, root, minFor[root])
			short = true
		}
	}
	if short {
		os.Exit(2)
	}

	for _, f := range res.Findings {
		fmt.Println(f)
	}
	if len(res.Findings) > 0 {
		fmt.Fprintf(os.Stderr,
			"\nmuxguard: %d finding(s) across %d files.\n"+
				"Every server here serves an explicit Handler, so http.DefaultServeMux is served by\n"+
				"NOTHING: anything on it compiles, runs, and answers 404. Register on the\n"+
				"microservice's own mux instead — Microservice.Mux() — and give every http.Server a\n"+
				"non-nil Handler.\n",
			len(res.Findings), res.Files())
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
	fmt.Printf("muxguard: %d files parsed (%s), no registration on http.DefaultServeMux.\n",
		res.Files(), strings.Join(parts, " "))
}
