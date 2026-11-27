// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// requireGolden compares out with testdata/<name>. The -update flag (the one the
// teatest golden package registers) rewrites it; run that locally, never in CI.
func requireGolden(t *testing.T, name string, out []byte) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name))
	recordGolden(path)
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")) // a Windows checkout
	if !bytes.Equal(want, out) {
		t.Errorf("%s differs from its golden.\n--- want\n%s\n--- got\n%s", name, want, out)
	}
}

// quoteLines makes raw ANSI reviewable: each line Go-quoted, escapes visible.
func quoteLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(strings.TrimPrefix(strconv.Quote(l), `"`), `"`)
	}
	return strings.Join(lines, "\n")
}
