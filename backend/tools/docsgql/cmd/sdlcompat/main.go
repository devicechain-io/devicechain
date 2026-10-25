// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command sdlcompat compares every served GraphQL schema with the same schema at a
// release and fails on a breaking change that is not declared in
// sdlcompat/breaks.txt. It also fails on a `@deprecated` with no usable reason. See
// package sdlcompat for the rules.
//
//	go run ./cmd/sdlcompat -base <baseline-tree> -baseline <tag> [repo-root]
//
// Run from backend/tools/docsgql; repo-root defaults to ../../.. . <baseline-tree> is
// a directory holding the release's backend/services (hack/check-graphql-compat.sh
// extracts it with git archive and passes it here).
//
//	-list-deprecated <tree>  print `schema coordinate` for every @deprecated element served by the
//	          tree (a directory holding backend/services) and exit; used by the deprecation-window check
//	-v        also list the safe changes
//	-breaks   read declarations from this file instead of sdlcompat/breaks.txt
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/devicechain-io/dc-docsgql/sdlcompat"
)

// breaksFile is where intended breaks are declared, relative to the repo root.
const breaksFile = "backend/tools/docsgql/sdlcompat/breaks.txt"

func main() {
	base := flag.String("base", "", "directory holding the baseline release's tree (it must contain backend/services)")
	baseline := flag.String("baseline", "", "the baseline release tag, e.g. v0.24.0")
	verbose := flag.Bool("v", false, "also list safe changes")
	breaks := flag.String("breaks", "", "the declarations file (default <repo-root>/"+breaksFile+"); the wrapper's self-test points it at a scratch file")
	listDeprecated := flag.String("list-deprecated", "", "print every @deprecated element of this tree and exit")
	flag.Parse()
	if *listDeprecated != "" {
		schemas, err := sdlcompat.Load(filepath.Join(*listDeprecated, "backend", "services"))
		if err != nil {
			fail(err)
		}
		for _, l := range sdlcompat.Deprecated(schemas) {
			fmt.Println(l)
		}
		return
	}
	if *base == "" || *baseline == "" {
		fail(fmt.Errorf("-base and -baseline are both required"))
	}
	root := "../../.."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	baseSchemas, err := sdlcompat.Load(filepath.Join(*base, "backend", "services"))
	if err != nil {
		fail(fmt.Errorf("baseline %s: %w", *baseline, err))
	}
	headSchemas, err := sdlcompat.Load(filepath.Join(root, "backend", "services"))
	if err != nil {
		fail(fmt.Errorf("working tree: %w", err))
	}

	declPath := *breaks
	if declPath == "" {
		declPath = filepath.Join(root, breaksFile)
	}
	f, err := os.Open(declPath)
	if err != nil {
		// A missing declarations file is an error, not an empty list: deleting it must
		// not be a way to make every declared break fail in a confusing way, nor (if a
		// later change made absence mean "none") a way to drop declarations unseen.
		fail(fmt.Errorf("read the declarations: %w", err))
	}
	decls, bad := sdlcompat.ParseDeclarations(f, breaksFile)
	f.Close()

	changes := sdlcompat.Compare(baseSchemas, headSchemas)
	res := sdlcompat.Evaluate(*baseline, changes, decls)
	lint := sdlcompat.LintDeprecations(headSchemas)

	failed := 0
	for _, b := range bad {
		failed++
		fmt.Printf("FAIL invalid declaration %s\n", b)
	}
	for _, c := range res.Undeclared {
		failed++
		fmt.Printf("FAIL breaking change against %s: %s\n", *baseline, c)
		fmt.Printf("     A client written against %s breaks. Make the change additively (add the new\n", *baseline)
		fmt.Printf("     element, mark the old one @deprecated(reason: ...)), or, if the break is intended,\n")
		fmt.Printf("     declare it in %s:\n", breaksFile)
		fmt.Printf("       %s\n", sdlcompat.DeclarationFor(*baseline, c))
	}
	for _, d := range res.Stale {
		failed++
		fmt.Printf("FAIL %s:%d declares %s: %s %s against %s, but that change is not in the schema; delete the line\n",
			breaksFile, d.Line, d.Schema, d.Coord, d.Kind, d.Baseline)
	}
	for _, l := range lint {
		failed++
		fmt.Printf("FAIL %s\n", l)
	}
	for _, dc := range res.Declared {
		fmt.Printf("declared  %s\n          reason: %s\n", dc.Change, dc.Declaration.Reason)
	}
	for _, c := range res.Dangerous {
		fmt.Printf("dangerous %s\n", c)
	}
	for _, d := range res.Expired {
		fmt.Printf("expired   %s:%d (%s %s %s) was declared against %s; the baseline is now %s, so it covers nothing — delete it\n",
			breaksFile, d.Line, d.Schema, d.Coord, d.Kind, d.Baseline, *baseline)
	}
	if *verbose {
		for _, c := range changes {
			if c.Severity == sdlcompat.Safe {
				fmt.Printf("safe      %s\n", c)
			}
		}
	}
	fmt.Printf("sdlcompat: %d schemas at %s, %d now; %d breaking (%d declared), %d dangerous, %d safe; %d failed\n",
		len(baseSchemas), *baseline, len(headSchemas), len(res.Undeclared)+len(res.Declared), len(res.Declared),
		len(res.Dangerous), res.Safe, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdlcompat:", err)
	os.Exit(2)
}
