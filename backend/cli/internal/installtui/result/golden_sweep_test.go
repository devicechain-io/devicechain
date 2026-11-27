// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// sweptGoldens are the golden directories whose every file some test must compare.
var sweptGoldens = []string{"testdata"}

// comparedGoldens records each golden a test compared (slash paths under this package).
var comparedGoldens sync.Map

func recordGolden(path string) { comparedGoldens.Store(filepath.ToSlash(path), true) }

// TestMain sweeps the goldens after a run of the whole package: a golden file that no test
// compared is a screen state whose scenario was dropped from its table while its fixtures
// stayed behind, and the golden matrix (installtui's matrix_test.go) would still find it on
// disk. A run filtered with -run, -skip or -list compares only some goldens, so it is not swept.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 && !goldenRunFiltered() {
		if missed := uncomparedGoldens(); len(missed) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: %d golden file(s) were compared by no test (a scenario was dropped, or the file is stale):\n  %s\n",
				len(missed), strings.Join(missed, "\n  "))
			code = 1
		}
	}
	os.Exit(code)
}

func goldenRunFiltered() bool {
	for _, name := range []string{"test.run", "test.skip", "test.list"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return true
		}
	}
	return false
}

func uncomparedGoldens() []string {
	var missed []string
	for _, root := range sweptGoldens {
		_ = filepath.WalkDir(filepath.FromSlash(root), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == ".gitattributes" {
				return err
			}
			if _, ok := comparedGoldens.Load(filepath.ToSlash(path)); !ok {
				missed = append(missed, filepath.ToSlash(path))
			}
			return nil
		})
	}
	sort.Strings(missed)
	return missed
}
