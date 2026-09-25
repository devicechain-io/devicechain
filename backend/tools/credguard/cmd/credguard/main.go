// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command credguard reports production code that names a watched secret compare
// (credguard.Watched) outside the credential primitive without an exemption for that
// function and that member. It exits 1 when it finds any, or when an exemption matched
// nothing, and 2 when it cannot do its job — including a malformed or duplicate
// exemption.
//
// Every run, success included, prints the exempt set with each entry's site count and
// reason, so the exemptions are visible in every CI log rather than being the one claim
// nothing reports.
//
//	credguard [-exempt dir.func@member=reason]... root[=minfiles]...
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-credguard"
)

type specFlag []string

func (s *specFlag) String() string     { return strings.Join(*s, ",") }
func (s *specFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var specs specFlag
	flag.Var(&specs, "exempt", "dir.func@member=reason: allow one watched member inside one function, for a stated reason")
	flag.CommandLine.Init("credguard", flag.ContinueOnError)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: credguard [-exempt dir.func@member=reason]... <root>[=minfiles]...")
		os.Exit(2)
	}
	exemptions, err := credguard.ParseExemptions(specs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "credguard: %v\n", err)
		os.Exit(2)
	}

	roots := make([]string, 0, flag.NArg())
	minFor := map[string]int{}
	for _, arg := range flag.Args() {
		root, minStr, hasMin := strings.Cut(arg, "=")
		min := 1
		if hasMin {
			n, err := strconv.Atoi(minStr)
			if err != nil || n < 1 {
				fmt.Fprintf(os.Stderr, "credguard: bad minimum in %q\n", arg)
				os.Exit(2)
			}
			min = n
		}
		roots = append(roots, root)
		minFor[root] = min
	}

	res, err := credguard.Scan(exemptions, roots...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "credguard: %v\n", err)
		os.Exit(2)
	}

	// A scan that read nothing has no opinion about the tree. Exit 2, not 0: a wrong
	// root or a renamed directory must not read as clean.
	short := false
	for _, root := range roots {
		if got := res.PerRoot[root]; got < minFor[root] {
			fmt.Fprintf(os.Stderr, "credguard: parsed %d Go files under %q, expected at least %d — "+
				"that root is not being read, so a clean result from it means nothing\n",
				got, root, minFor[root])
			short = true
		}
	}
	if short {
		os.Exit(2)
	}

	// 🔴 THE EXEMPT SET IS PRINTED ON EVERY RUN. An exemption is the one claim a clean
	// scan never reports: it is what the guard was told not to look at. Listing each
	// one with its site count and reason puts that claim in every CI log.
	for _, e := range exemptions {
		fmt.Printf("exempt %s (%d site(s)): %s\n", e, res.Used[e.String()], e.Reason)
	}

	failed := false
	for _, f := range res.Findings {
		fmt.Println(f)
		failed = true
	}

	// 🔴 A STALE EXEMPTION IS A FINDING. An exemption that matches nothing is permission
	// nobody is using — and it stays valid for whatever code later lands in that function
	// under that name, which is exactly how an allow-list outlives its reason.
	stale := make([]string, 0)
	for k, n := range res.Used {
		if n == 0 {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		fmt.Printf("exemption %s matched no call site; delete it\n", k)
		failed = true
	}

	labels := make([]string, 0, len(credguard.Watched))
	for _, m := range credguard.Watched {
		labels = append(labels, m.Label)
	}
	if failed {
		fmt.Fprintf(os.Stderr, "\ncredguard: %d finding(s), %d stale exemption(s), across %d files.\n"+
			"A secret compare (%s) outside backend/core/credential is a check the\n"+
			"per-principal backoff does not count. Authenticate through credential.Checker.Check,\n"+
			"or exempt the function for that member with the reason it is not a guessable check.\n",
			len(res.Findings), len(stale), res.Files(), strings.Join(labels, ", "))
		os.Exit(1)
	}
	fmt.Printf("credguard: %d files parsed, no watched compare outside %s (%d exemption(s), listed above).\n",
		res.Files(), credguard.OwnerDir, len(exemptions))
}
