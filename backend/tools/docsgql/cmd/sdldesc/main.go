// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command sdldesc checks that every element of every served GraphQL schema has a
// description, against the shrink-only allowlist in sdldesc/undescribed/. See package
// sdldesc for the rules.
//
//	go run ./cmd/sdldesc [flags] [repo-root]   (run from backend/tools/docsgql; root defaults to ../../..)
//
//	-coverage        print described/total per schema and kind, then check
//	-list <schema>   print the coordinates of one schema that still lack a description
//	-prune           delete allowlist entries that are described or gone (never adds one)
//	-seed            create the allowlist of a schema that has none (never edits one)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devicechain-io/dc-docsgql/sdldesc"
)

func main() {
	coverage := flag.Bool("coverage", false, "print coverage per schema and kind")
	list := flag.String("list", "", "print the undescribed coordinates of one schema (published name, e.g. device-management or user-management-admin)")
	prune := flag.Bool("prune", false, "remove allowlist entries that are now described or no longer exist")
	seed := flag.Bool("seed", false, "create the allowlist for any served schema that has none")
	flag.Parse()

	root := "../../.."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	servicesDir := filepath.Join(root, "backend", "services")
	allowDir := filepath.Join(root, "backend", "tools", "docsgql", "sdldesc", "undescribed")

	served, err := sdldesc.LoadServed(servicesDir)
	if err != nil {
		fail(err)
	}

	if *list != "" {
		for _, s := range served {
			if s.Name == *list {
				for _, c := range sdldesc.Undescribed(s) {
					fmt.Println(c)
				}
				return
			}
		}
		fail(fmt.Errorf("no served schema named %q", *list))
	}

	if *seed {
		for _, s := range served {
			p := filepath.Join(allowDir, s.Name+".txt")
			if _, err := os.Stat(p); err == nil {
				continue
			}
			if err := writeList(p, s.Name, sdldesc.Undescribed(s)); err != nil {
				fail(err)
			}
			fmt.Printf("seeded %s\n", p)
		}
	}

	allow, err := sdldesc.ReadAllowlists(allowDir)
	if err != nil {
		fail(err)
	}

	if *prune {
		for _, s := range served {
			old, ok := allow[s.Name]
			if !ok {
				continue
			}
			undescribed := map[string]bool{}
			for _, c := range sdldesc.Undescribed(s) {
				undescribed[c] = true
			}
			var kept []string
			for _, c := range old {
				if undescribed[c] {
					kept = append(kept, c)
				}
			}
			if len(kept) == len(old) {
				continue
			}
			if err := writeList(filepath.Join(allowDir, s.Name+".txt"), s.Name, kept); err != nil {
				fail(err)
			}
			allow[s.Name] = kept
			fmt.Printf("pruned %d from %s\n", len(old)-len(kept), s.Name)
		}
	}

	if *coverage {
		sdldesc.PrintCoverage(os.Stdout, sdldesc.Measure(served))
	}

	findings := sdldesc.Check(served, allow)
	for _, f := range findings {
		fmt.Printf("FAIL %s\n", f)
	}
	listed := 0
	for _, s := range served {
		listed += len(allow[s.Name])
	}
	fmt.Printf("sdldesc: %d schemas, %d elements, %d allowlisted as undescribed, %d failed\n",
		len(served), countElements(served), listed, len(findings))
	if len(findings) > 0 {
		os.Exit(1)
	}
}

func countElements(served []sdldesc.Served) int {
	n := 0
	for _, s := range served {
		n += len(s.Elements)
	}
	return n
}

const header = `# Elements of the %s schema that have no description yet.
#
# This list may only SHRINK. Describe an element with a """string""" directly above it in
# the schema, then run: go run ./cmd/sdldesc -prune   (from backend/tools/docsgql)
# New schema elements must arrive described; do not add lines here.
`

func writeList(path, name string, coords []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, header, name)
	for _, c := range coords {
		b.WriteString(c)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdldesc:", err)
	os.Exit(2)
}
