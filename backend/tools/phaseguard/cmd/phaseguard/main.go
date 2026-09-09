// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command phaseguard reports work that runs in the wrong lifecycle phase: a
// build-per-start thing constructed on the initialize path, or a build-once thing
// constructed on the start path.
//
// Exit codes are three-valued and the third one is the point:
//
//	0  the tree is clean
//	1  findings
//	2  THE INSTRUMENT IS BROKEN — nothing was loaded, the program does not type-check,
//	   a watched symbol no longer exists, too few entry points were discovered, or the
//	   rule's expected sites came back short. An empty tree exits 2, never 0.
//
// 🔴 BUILD THE BINARY AND RUN IT. `go run` collapses every non-zero exit to 1, which
// silently destroys the distinction above and turns a broken instrument into a finding.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	phaseguard "github.com/devicechain-io/dc-phaseguard"
)

func main() {
	dir := flag.String("dir", ".", "directory the patterns are resolved from")
	minPkgs := flag.Int("min-packages", 1, "minimum packages that must load")
	minFiles := flag.Int("min-files", 1, "minimum Go files that must load")
	minInit := flag.Int("min-initialize-entries", 1, "minimum initialize-phase entry points")
	minStart := flag.Int("min-start-entries", 1, "minimum start-phase entry points")
	only := flag.String("rules", "", "comma-separated rule names to run (default: all)")
	corePkg := flag.String("core-pkg", phaseguard.CorePkg,
		"import path of the lifecycle framework (self-test fixtures only)")
	// 🔴 OFF ONLY FOR THE SELF-TEST'S FIXTURES, which are single files with no core
	// package in them and therefore cannot satisfy any liveness floor. It is never
	// passed by the CI invocation, and the self-test proves the armed run refuses a
	// tree the floors do not fit.
	liveness := flag.Bool("liveness", true, "refuse a tree the rules' expected sites are missing from")
	showExpected := flag.Bool("show-expected", false, "print where each rule's symbols were found on the path they are allowed to be on")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		fmt.Fprintln(os.Stderr, "usage: phaseguard [flags] <pattern> [pattern...]")
		os.Exit(2)
	}

	rules := phaseguard.RulesFor(*corePkg)
	all := rules
	if *only != "" {
		wanted := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(n)] = true
		}
		var sel []phaseguard.Rule
		for _, r := range all {
			if wanted[r.Name] {
				sel = append(sel, r)
				delete(wanted, r.Name)
			}
		}
		if len(wanted) > 0 {
			for n := range wanted {
				fmt.Fprintf(os.Stderr, "phaseguard: no rule named %q\n", n)
			}
			os.Exit(2)
		}
		rules = sel
	}

	res, err := phaseguard.Scan(phaseguard.Options{Dir: *dir, Core: *corePkg}, rules, patterns...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "phaseguard: %v\n", err)
		os.Exit(2)
	}

	// ---------------------------------------------------------------------
	// The instrument checks, all of which exit 2 rather than 1.
	//
	// 🔴 EXIT 2 IS A DIFFERENT CLAIM FROM EXIT 1, AND CONFLATING THEM IS THE FAILURE
	// THIS REPOSITORY KEEPS HITTING. "I looked and the tree is clean" and "I could not
	// look" are opposite statements that a two-valued exit renders identically. A
	// scan that read nothing has no opinion about anything.
	// ---------------------------------------------------------------------
	var broken []string

	if res.Packages < *minPkgs {
		broken = append(broken, fmt.Sprintf(
			"loaded %d packages, expected at least %d — nothing here was analysed, so a clean result means nothing",
			res.Packages, *minPkgs))
	}
	if res.Files < *minFiles {
		broken = append(broken, fmt.Sprintf(
			"loaded %d Go files, expected at least %d", res.Files, *minFiles))
	}
	for _, sym := range res.Unresolved {
		broken = append(broken, fmt.Sprintf(
			"watched symbol %s does not exist in the loaded program — it was renamed, moved or "+
				"deleted, and every reference to it silently stopped matching", sym))
	}

	if *liveness {
		// Entry points first. Every rule's answer is downstream of these, so a
		// discovery step that stopped recognising the framework's contract makes
		// every rule report clean at once.
		if got := res.EntryPoints[phaseguard.Initialize]; got < *minInit {
			broken = append(broken, fmt.Sprintf(
				"found %d initialize-phase entry points, expected at least %d — the phase "+
					"discovery is not reading this tree", got, *minInit))
		}
		if got := res.EntryPoints[phaseguard.Start]; got < *minStart {
			broken = append(broken, fmt.Sprintf(
				"found %d start-phase entry points, expected at least %d — the phase "+
					"discovery is not reading this tree", got, *minStart))
		}
		// Then each rule's positive control: the direction that MUST be non-empty.
		// A call graph that stopped resolving drives this to zero at the same moment
		// it drives the findings to zero.
		for _, r := range rules {
			got := res.Reached[r.Name][r.Expect]
			if got < r.MinExpected {
				broken = append(broken, fmt.Sprintf(
					"rule %q found %d of its symbols on the %s path, expected at least %d — "+
						"zero findings from a scan that cannot see the sites it is supposed to "+
						"see is not a pass",
					r.Name, got, r.Expect, r.MinExpected))
				for _, f := range res.Expected[r.Name] {
					broken = append(broken, "    it did see "+f.String())
				}
			}
		}
	}

	if len(broken) > 0 {
		fmt.Fprintln(os.Stderr, "phaseguard: the check could not be performed:")
		for _, b := range broken {
			fmt.Fprintf(os.Stderr, "  - %s\n", b)
		}
		os.Exit(2)
	}

	if *showExpected {
		for _, r := range rules {
			fmt.Printf("# %s: %d site(s) on the %s path\n", r.Name, len(res.Expected[r.Name]), r.Expect)
			for _, f := range res.Expected[r.Name] {
				fmt.Println(f)
			}
		}
	}

	if len(res.Findings) > 0 {
		byRule := map[string]phaseguard.Rule{}
		for _, r := range rules {
			byRule[r.Name] = r
		}
		for _, f := range res.Findings {
			fmt.Println(f)
		}
		fmt.Fprintf(os.Stderr, "\nphaseguard: %d finding(s) across %d files.\n",
			len(res.Findings), res.Files)
		seen := map[string]bool{}
		for _, f := range res.Findings {
			if seen[f.Rule] {
				continue
			}
			seen[f.Rule] = true
			r := byRule[f.Rule]
			fmt.Fprintf(os.Stderr, "\n%s: %s.\n  Fix: %s.\n", r.Name, r.Why, r.Remedy)
		}
		os.Exit(1)
	}

	fmt.Printf("phaseguard: %d packages / %d files, %d initialize and %d start entry points",
		res.Packages, res.Files, res.EntryPoints[phaseguard.Initialize], res.EntryPoints[phaseguard.Start])
	for _, r := range rules {
		fmt.Printf("; %s ok (%d on the %s path)", r.Name, res.Reached[r.Name][r.Expect], r.Expect)
	}
	fmt.Println(".")
}
