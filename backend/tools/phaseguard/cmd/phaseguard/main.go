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
	minStop := flag.Int("min-stop-entries", 1, "minimum stop-phase entry points")
	only := flag.String("rules", "", "comma-separated rule and symmetry names to run (default: all)")
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
	syms := phaseguard.Symmetries
	if *only != "" {
		wanted := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(n)] = true
		}
		var selR []phaseguard.Rule
		for _, r := range rules {
			if wanted[r.Name] {
				selR = append(selR, r)
				delete(wanted, r.Name)
			}
		}
		var selS []phaseguard.Symmetry
		for _, y := range syms {
			if wanted[y.Name] {
				selS = append(selS, y)
				delete(wanted, y.Name)
			}
		}
		if len(wanted) > 0 {
			for n := range wanted {
				fmt.Fprintf(os.Stderr, "phaseguard: no rule or symmetry named %q\n", n)
			}
			os.Exit(2)
		}
		rules, syms = selR, selS
	}

	res, err := phaseguard.Scan(phaseguard.Options{Dir: *dir, Core: *corePkg}, rules, syms, patterns...)
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
		if got := res.EntryPoints[phaseguard.Stop]; got < *minStop {
			broken = append(broken, fmt.Sprintf(
				"found %d stop-phase entry points, expected at least %d — the phase "+
					"discovery is not reading this tree", got, *minStop))
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

	if *liveness {
		for _, y := range syms {
			sr := res.Symmetry[y.Name]
			// 🔴 THE SERVICE COUNT IS THE FLOOR THAT MATTERS, because this constraint
			// is a SET DIFFERENCE and a service that dropped out of the set contributes
			// an empty difference — which is indistinguishable from a service that
			// stops everything it starts.
			if sr.Services < y.MinServices {
				broken = append(broken, fmt.Sprintf(
					"symmetry %q compared %d services, expected at least %d — services are "+
						"dropping out of the comparison, and a service that is not compared "+
						"reports no findings exactly like one that is clean",
					y.Name, sr.Services, y.MinServices))
			}
			if sr.Pairs < y.MinPairs {
				broken = append(broken, fmt.Sprintf(
					"symmetry %q matched %d components on the %s side, expected at least %d — "+
						"an empty difference against a side the walk could not read is not a pass",
					y.Name, sr.Pairs, y.To, y.MinPairs))
			}
		}
	}

	// 🔴 OUTSIDE THE LIVENESS SWITCH, LIKE A RENAMED SYMBOL AND FOR THE SAME REASON.
	// Both of these are the analysis failing on a SPECIFIC SITE rather than a floor
	// about how much of the tree was loaded, and -liveness=false exists to let the
	// self-test's miniature fixtures fall short of the FLOORS, not to let the instrument
	// answer questions it cannot answer.
	for _, y := range syms {
		sr := res.Symmetry[y.Name]
		// 🔴 A ONE-SIDED SERVICE FAILS ON ITS OWN AND IS NOT LEFT TO THE FLOOR. The set
		// difference cannot be taken when only one callback was found, so the service
		// is not compared — and a service that is not compared contributes an empty
		// difference, which is what a service that stops everything it starts also
		// contributes. Leaving that to MinServices meant a third of the services could
		// quietly drop out before anything failed, and the way they drop out is
		// mundane: assigning the callback from a function call instead of writing the
		// literal inline is enough.
		for _, pa := range sr.Partial {
			broken = append(broken, fmt.Sprintf(
				"symmetry %q could not compare %s — only one side of the pair was found, "+
					"so every component it wires is unchecked", y.Name, pa))
		}
		for _, a := range sr.Anonymous {
			broken = append(broken, fmt.Sprintf(
				"symmetry %q cannot identify a component: %s — it is not counted on "+
					"either side, so it can neither be reported nor vouched for", y.Name, a))
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
		for _, y := range syms {
			sr := res.Symmetry[y.Name]
			fmt.Printf("# %s: %d service(s), %d component(s) matched on the %s side\n",
				y.Name, sr.Services, sr.Pairs, y.To)
			for _, m := range sr.Matched {
				fmt.Println("   " + m)
			}
			for _, pa := range sr.Partial {
				fmt.Println("   (not compared) " + pa)
			}
		}
	}

	var symFindings []phaseguard.SymFinding
	for _, y := range syms {
		symFindings = append(symFindings, res.Symmetry[y.Name].Findings...)
	}
	if len(symFindings) > 0 {
		for _, f := range symFindings {
			fmt.Println(f)
		}
		fmt.Fprintf(os.Stderr, "\nphaseguard: %d component(s) handled in one phase and not its pair.\n",
			len(symFindings))
		seen := map[string]bool{}
		for _, y := range syms {
			if len(res.Symmetry[y.Name].Findings) == 0 || seen[y.Name] {
				continue
			}
			seen[y.Name] = true
			fmt.Fprintf(os.Stderr, "\n%s: %s.\n  Fix: %s.\n", y.Name, y.Why, y.Remedy)
		}
		if len(res.Findings) == 0 {
			os.Exit(1)
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

	fmt.Printf("phaseguard: %d packages / %d files, %d initialize, %d start and %d stop entry points",
		res.Packages, res.Files, res.EntryPoints[phaseguard.Initialize],
		res.EntryPoints[phaseguard.Start], res.EntryPoints[phaseguard.Stop])
	for _, r := range rules {
		fmt.Printf("; %s ok (%d on the %s path)", r.Name, res.Reached[r.Name][r.Expect], r.Expect)
	}
	for _, y := range syms {
		sr := res.Symmetry[y.Name]
		fmt.Printf("; %s ok (%d services, %d matched", y.Name, sr.Services, sr.Pairs)
		// Printed on the clean path, not only under -show-expected: it is the size of a
		// declared blind spot, and a blind spot only visible behind a flag is one nobody
		// reads.
		if sr.ViaInterface > 0 {
			fmt.Printf(", %d of them identified only by interface", sr.ViaInterface)
		}
		fmt.Print(")")
	}
	fmt.Println(".")
}
