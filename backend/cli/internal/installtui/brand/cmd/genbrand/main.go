// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command genbrand regenerates the installer's logo cells from the DeviceChain
// mark. It is a maintainer tool; dcctl embeds its committed output and target
// machines never run it. From backend/cli/internal/installtui/brand:
//
//	go generate .
//
// which runs
//
//	go run ./cmd/genbrand -svg <repo>/branding/logos/symbol.svg -out data
//
// TestCommittedCellsAreFresh fails until the output is regenerated after the
// SVG or the generator changes.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/devicechain-io/dcctl/internal/installtui/brand/gen"
)

func main() {
	svgPath := flag.String("svg", "", "path to branding/logos/symbol.svg")
	outDir := flag.String("out", "", "directory to write the generated files into")
	flag.Parse()
	if *svgPath == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, "usage: genbrand -svg <symbol.svg> -out <dir>")
		os.Exit(2)
	}
	if err := run(*svgPath, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "genbrand:", err)
		os.Exit(1)
	}
}

func run(svgPath, outDir string) error {
	svg, err := os.ReadFile(svgPath)
	if err != nil {
		return err
	}
	files, err := gen.Generate(svg)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(outDir, name), files[name], 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", filepath.Join(outDir, name))
	}
	return nil
}
