// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The golden matrix of the cluster journey (dcctl install local --ui=tui): every screen,
// in the order the flow reaches it, at every rung of the capability ladder and every
// size, as escape-stripped text and as raw ANSI. The goldens themselves are drawn and
// compared by each screen's own test (TestSplashGoldens, TestPrerunGoldens,
// TestProgressGoldens, the result package's TestResultGoldens); this test owns only the
// question those cannot answer each on their own: is any screen, state, level or size
// MISSING from the matrix? A screen added without goldens, a scenario dropped from its
// table, or a size removed from one test's list fails here, by name.
//
// That the files on disk are the ones those tests DRAW is each package's golden sweep
// (golden_sweep_test.go here, in brand and in result): after an unfiltered run, a golden
// that no test compared fails the package, so a scenario dropped from its table while its
// fixtures stay behind is caught there, not passed here.
//
// Paths are relative to this package; the splash's goldens live in brand/testdata, the
// result screens' in result/testdata.
var clusterFlow = []struct {
	screen string
	// states are golden directories, one per state of the screen the matrix draws.
	states []string
}{
	{"splash", []string{"brand/testdata/splash"}},
	{"welcome", []string{"testdata/prerun/welcome-kind", "testdata/prerun/welcome-context"}},
	{"preflight", []string{"testdata/prerun/preflight-running", "testdata/prerun/preflight-pass",
		"testdata/prerun/preflight-warn", "testdata/prerun/preflight-fail", "testdata/prerun/preflight-verdicts",
		"testdata/prerun/preflight-skipped", "testdata/prerun/preflight-switch", "testdata/prerun/preflight-advisory"}},
	{"choices", []string{"testdata/prerun/choices-kind", "testdata/prerun/choices-context-compact",
		"testdata/prerun/choices-failed", "testdata/prerun/choices-rechecking", "testdata/prerun/choices-renaming",
		"testdata/prerun/picker"}},
	{"advanced drawer", []string{"testdata/prerun/drawer", "testdata/prerun/drawer-compact"}},
	{"review", []string{"testdata/prerun/review-kind", "testdata/prerun/review-context-template",
		"testdata/prerun/review-settling"}},
	{"progress", []string{"testdata/progress/applying", "testdata/progress/kind-prompt",
		"testdata/progress/quiet-retry-indeterminate", "testdata/progress/readiness-logs-paused",
		"testdata/progress/finalizing"}},
	{"stopping", []string{"testdata/progress/stopping", "result/testdata/stopped"}},
	{"failure", []string{"result/testdata/failed", "result/testdata/readiness-timeout"}},
	{"success", []string{"result/testdata/succeeded"}},
	{"help", []string{"result/testdata/help", "testdata/prerun/help-welcome", "testdata/prerun/help-preflight",
		"testdata/prerun/help-switch", "testdata/prerun/help-choices", "testdata/prerun/help-renaming",
		"testdata/prerun/help-drawer", "testdata/prerun/help-picker", "testdata/prerun/help-review",
		"testdata/progress/help-applying", "testdata/progress/help-kind-prompt", "testdata/progress/help-verdict"}},
}

// The matrix: the four rungs a terminal UI runs at (L0 is plain output, pinned byte for
// byte by the cmd package's characterisation goldens) and the four sizes of the layout
// ladder.
var (
	matrixLevels = []string{"L4", "L3", "L2", "L1"}
	matrixSizes  = []string{"120x40", "100x30", "80x24", "60x20"}
)

// cellPaths are the two goldens of one cell. The splash's are flat files named
// splash-<level>-<size>; every other state is a directory of <level>-<size> files.
func cellPaths(state, level, size string) []string {
	if base := filepath.Base(state); base == "splash" {
		p := filepath.Join(filepath.Dir(state), fmt.Sprintf("splash-%s-%s", strings.ToLower(level), size))
		return []string{p + ".txt", p + ".ansi"}
	}
	p := filepath.Join(state, level+"-"+size)
	return []string{p + ".txt", p + ".ansi"}
}

func TestTheGoldenMatrixCoversTheClusterFlow(t *testing.T) {
	cells := 0
	for _, s := range clusterFlow {
		if len(s.states) == 0 {
			t.Errorf("%s: no golden state", s.screen)
		}
		for _, st := range s.states {
			for _, lv := range matrixLevels {
				for _, sz := range matrixSizes {
					for _, p := range cellPaths(filepath.FromSlash(st), lv, sz) {
						fi, err := os.Stat(p)
						switch {
						case err != nil:
							t.Errorf("%s: %s at %s has no golden %s", s.screen, st, lv+" "+sz, filepath.ToSlash(p))
						case fi.Size() == 0:
							t.Errorf("%s: the golden %s is empty", s.screen, filepath.ToSlash(p))
						}
					}
					cells++
				}
			}
		}
	}
	if want := 4 * 4 * countStates(); cells != want {
		t.Fatalf("checked %d cells, want %d", cells, want)
	}
}

func countStates() int {
	n := 0
	for _, s := range clusterFlow {
		n += len(s.states)
	}
	return n
}

// A full-matrix golden directory that the flow above does not list is a screen state
// the matrix does not account for: list it under its screen, or it was left behind by
// a scenario that no longer exists.
func TestEveryFullMatrixStateIsInTheFlow(t *testing.T) {
	listed := map[string]bool{}
	for _, s := range clusterFlow {
		for _, st := range s.states {
			listed[st] = true
		}
	}
	cell := regexp.MustCompile(`^L[1-4]-[0-9]+x[0-9]+\.(txt|ansi)$`)
	var unlisted []string
	for _, root := range []string{"testdata/prerun", "testdata/progress", "result/testdata"} {
		entries, err := os.ReadDir(filepath.FromSlash(root))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := root + "/" + e.Name()
			files, err := os.ReadDir(filepath.FromSlash(dir))
			if err != nil {
				t.Fatal(err)
			}
			levels := map[string]bool{}
			for _, f := range files {
				if cell.MatchString(f.Name()) {
					levels[f.Name()[:2]] = true
				}
			}
			// Only a state drawn at every level is a screen state of the matrix; a
			// directory of one level is a focused golden (the command quoting's, at L1).
			if len(levels) == len(matrixLevels) && !listed[dir] {
				unlisted = append(unlisted, dir)
			}
		}
	}
	sort.Strings(unlisted)
	for _, d := range unlisted {
		t.Errorf("%s is drawn at every level but is not a state of the cluster flow's matrix", d)
	}
}
