// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command redlines runs the declarative-only and constant-remediation guards.
//
//	redlines deps [-allow dir=reason]... root[=minfiles]...
//	redlines remediation root[=minfiles]...
//
// It exits 0 when clean, 1 when it found something or an allowance matched nothing, and 2
// when it cannot do its job: an unreadable root, fewer Go files than the floor, a file it
// cannot parse, a malformed allowance.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-redlines"
)

type specFlag []string

func (s *specFlag) String() string     { return strings.Join(*s, ",") }
func (s *specFlag) Set(v string) error { *s = append(*s, v); return nil }

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "redlines: "+format+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: redlines deps|remediation [flags] root[=minfiles]...")
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var allows specFlag
	if cmd == "deps" {
		fs.Var(&allows, "allow", "dir=reason: admit the files directly in dir; the reason must cite an ADR-nnn")
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() == 0 {
		die("no root given")
	}
	roots := make([]string, 0, fs.NArg())
	floor := 0
	for _, arg := range fs.Args() {
		root, minStr, has := strings.Cut(arg, "=")
		if has {
			n, err := strconv.Atoi(minStr)
			if err != nil || n < 1 {
				die("bad minimum in %q", arg)
			}
			floor += n
		}
		roots = append(roots, root)
	}

	switch cmd {
	case "deps":
		var parsed []redlines.Allow
		seen := map[string]bool{}
		for _, spec := range allows {
			a, err := redlines.ParseAllow(spec)
			if err != nil {
				die("%v", err)
			}
			if seen[a.Dir] {
				die("duplicate allowance for %q", a.Dir)
			}
			seen[a.Dir] = true
			parsed = append(parsed, a)
		}
		res, err := redlines.ScanDeps(parsed, roots...)
		if err != nil {
			die("%v", err)
		}
		if res.GoMods == 0 || res.GoFiles < max(floor, 1) {
			die("read %d go.mod and %d Go files, expected at least one go.mod and %d Go files — the root is not being read, so a clean result means nothing", res.GoMods, res.GoFiles, max(floor, 1))
		}
		for _, a := range parsed {
			fmt.Printf("allowed %s (%d reference(s)): %s\n", a.Dir, res.Allowed[a.Dir], a.Reason)
		}
		bad := false
		for _, f := range res.Findings {
			fmt.Println(f)
			bad = true
		}
		for _, a := range res.Stale {
			fmt.Printf("allowance %s matched no reference\n", a.Dir)
			bad = true
		}
		fmt.Printf("redlines deps: %d go.mod, %d Go files, %d finding(s)\n", res.GoMods, res.GoFiles, len(res.Findings))
		if bad {
			os.Exit(1)
		}
	case "remediation":
		res, err := redlines.ScanRemediation(roots...)
		if err != nil {
			die("%v", err)
		}
		if res.GoFiles < max(floor, 1) {
			die("parsed %d Go files, expected at least %d — the root is not being read, so a clean result means nothing", res.GoFiles, max(floor, 1))
		}
		for _, f := range res.Findings {
			fmt.Println(f)
		}
		fmt.Printf("redlines remediation: %d Go files, %d watched site(s), %d finding(s)\n", res.GoFiles, res.Sites, len(res.Findings))
		if len(res.Findings) > 0 {
			os.Exit(1)
		}
	default:
		die("unknown command %q", cmd)
	}
}
