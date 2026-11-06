// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command decoderfuncs writes the payload-decoder function reference, in every locale, from
// the function library. Run it from backend/tools/docsgql after changing the library:
//
//	go run ./cmd/decoderfuncs --out ../../../docs
//
// TestCommittedReferenceIsCurrent fails until the pages it writes are committed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/devicechain-io/dc-docsgql/decoderfuncs"
)

func main() {
	out := flag.String("out", "../../../docs", "the docs root (the directory holding docusaurus.config.ts)")
	flag.Parse()
	for _, p := range decoderfuncs.Pages {
		body, err := decoderfuncs.Render(p.Locale)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		dst := filepath.Join(*out, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", dst)
	}
}
