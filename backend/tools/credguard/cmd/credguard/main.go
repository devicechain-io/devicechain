// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command credguard reports production code that names bcrypt.CompareHashAndPassword
// outside the credential primitive. It exits 1 when it finds any, or when an exemption
// matched nothing, and 2 when it cannot do its job.
//
//	credguard [-exempt dir.func=reason]... root[=minfiles]...
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

type exemptFlag []credguard.Exemption

func (e *exemptFlag) String() string { return fmt.Sprint(*e) }
func (e *exemptFlag) Set(s string) error {
	ex, err := credguard.ParseExemption(s)
	if err != nil {
		return err
	}
	*e = append(*e, ex)
	return nil
}

func main() {
	var exemptions exemptFlag
	flag.Var(&exemptions, "exempt", "dir.func=reason: allow the compare inside one function, for a stated reason")
	flag.CommandLine.Init("credguard", flag.ContinueOnError)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: credguard [-exempt dir.func=reason]... <root>[=minfiles]...")
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

	if failed {
		fmt.Fprintf(os.Stderr, "\ncredguard: %d finding(s), %d stale exemption(s), across %d files.\n"+
			"A bcrypt compare outside backend/core/credential is a password check the sign-in\n"+
			"backoff does not count. Authenticate through credential.Checker.Check instead.\n",
			len(res.Findings), len(stale), res.Files())
		os.Exit(1)
	}
	fmt.Printf("credguard: %d files parsed, no bcrypt compare outside %s (%d exemption(s) in use).\n",
		res.Files(), credguard.OwnerDir, len(res.Used))
}
