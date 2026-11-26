// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package brand

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/muesli/termenv"

	"github.com/devicechain-io/dcctl/internal/installtui/brand/gen"
)

var update = flag.Bool("update", false, "rewrite brand/testdata goldens from the current output")

// symbolSVG is the mark's source of truth, outside this module. hack/ci-select.py
// runs this package when it changes.
const symbolSVG = "../../../../../branding/logos/symbol.svg"

func TestCommittedCellsAreFresh(t *testing.T) {
	svg, err := os.ReadFile(symbolSVG)
	if err != nil {
		t.Fatal(err)
	}
	want, err := gen.Generate(svg)
	if err != nil {
		t.Fatal(err)
	}
	committed := map[string]bool{}
	err = fs.WalkDir(data, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		committed[filepath.Base(path)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range want {
		got, err := data.ReadFile("data/" + name)
		if err != nil {
			t.Errorf("%s is not committed: run go generate in internal/installtui/brand", name)
			continue
		}
		if !bytes.Equal(got, content) {
			t.Errorf("data/%s is stale against %s: run go generate in internal/installtui/brand", name, gen.SourcePath)
		}
		delete(committed, name)
	}
	for name := range committed {
		t.Errorf("data/%s is embedded but no longer generated", name)
	}
}

func TestEmbeddedDataWithinBudget(t *testing.T) {
	total := 0
	err := fs.WalkDir(data, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := data.ReadFile(path)
		total += len(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if total > 24*1024 {
		t.Fatalf("embedded logo data is %d bytes, over the 24 KiB budget", total)
	}
}

// The large mark is the true hexagon: 28 pixels wide and 32 tall in a square
// 32-pixel grid, so it keeps the SVG's proportions once two square pixels
// share each roughly 1:2 terminal cell.
func TestMarkShape(t *testing.T) {
	for _, tc := range []struct {
		m                      Mark
		cols, rows, wide, tall int
	}{
		{Large(), 32, 16, 28, 32},
		{Compact(), 24, 12, 22, 24},
	} {
		m := tc.m
		if m.Cols != tc.cols || m.Rows != tc.rows || len(m.Cells) != tc.rows || m.Pixels != tc.cols {
			t.Fatalf("%s: %dx%d cells", m.Name, m.Cols, m.Rows)
		}
		left, right, top, bottom := m.Pixels, -1, m.Pixels, -1
		for y := 0; y < m.Pixels; y++ {
			for x := 0; x < m.Pixels; x++ {
				if m.Pixel(x, y) != 0 {
					left, right = min(left, x), max(right, x)
					top, bottom = min(top, y), max(bottom, y)
				}
			}
		}
		if w, h := right-left+1, bottom-top+1; w != tc.wide || h != tc.tall {
			t.Errorf("%s: mark is %dx%d pixels, want %dx%d", m.Name, w, h, tc.wide, tc.tall)
		}
		// The one-pixel gap between the frame and the cube survives.
		var mid strings.Builder
		for x := 0; x < m.Pixels; x++ {
			switch k := m.Pixel(x, m.Pixels/2); {
			case k == 0:
				mid.WriteByte('.')
			case Facets()[k-1].Part == gen.Rim:
				mid.WriteByte('R')
			default:
				mid.WriteByte('C')
			}
		}
		if !regexp.MustCompile(`^\.*R+\.+C+\.+R+\.*$`).MatchString(mid.String()) {
			t.Errorf("%s: mid row %s has no gap between frame and cube", m.Name, mid.String())
		}
		// Rim and cube mirror about the axis, pixel for pixel.
		for y := 0; y < m.Pixels; y++ {
			for x := 0; x < m.Pixels; x++ {
				mx := int(2*m.Axis+0.5) - 1 - x
				if mx < 0 || mx >= m.Pixels {
					if m.Pixel(x, y) != 0 {
						t.Fatalf("%s: pixel (%d,%d) has no mirror", m.Name, x, y)
					}
					continue
				}
				if part(m.Pixel(x, y)) != part(m.Pixel(mx, y)) {
					t.Errorf("%s: pixel (%d,%d) %s mirrors (%d,%d) %s", m.Name, x, y, part(m.Pixel(x, y)), mx, y, part(m.Pixel(mx, y)))
				}
			}
		}
	}
}

func part(k uint8) string {
	if k == 0 {
		return "none"
	}
	return string(Facets()[k-1].Part)
}

func TestPalette(t *testing.T) {
	want := []string{"#1F425E", "#7AB7D9", "#208CB7", "#52A2C9", "#007BA6", "#006790", "#9ACEEC", "#006B97"}
	seen := map[string]bool{}
	for _, f := range Facets() {
		seen[f.Fill] = true
		if got := gen.Nearest256(f.RGB[0], f.RGB[1], f.RGB[2]); got != f.ANSI256 || got < 16 {
			t.Errorf("facet %d %s pins ANSI %d, nearest is %d", f.Index, f.Fill, f.ANSI256, got)
		}
	}
	for _, c := range want {
		if !seen[c] {
			t.Errorf("palette lacks %s", c)
		}
		delete(seen, c)
	}
	if len(seen) != 0 {
		t.Errorf("unexpected facet colours %v", seen)
	}
	if gen.Nearest256(0, 0, 0) != 16 || gen.Nearest256(255, 255, 255) != 231 || gen.Nearest256(128, 128, 128) != 244 {
		t.Error("xterm-256 mapping drifted")
	}
}

func TestShapeAndPaletteGoldens(t *testing.T) {
	for _, m := range []Mark{Large(), Compact()} {
		golden(t, m.Name+"-shape.txt", strings.Join(Static(m, termenv.Ascii), "\n")+"\n")
		golden(t, m.Name+"-ascii.txt", strings.Join(m.ASCII, "\n")+"\n")
		golden(t, m.Name+"-truecolor.ansi", strings.Join(Static(m, termenv.TrueColor), "\n")+"\n")
		golden(t, m.Name+"-256.ansi", strings.Join(Static(m, termenv.ANSI256), "\n")+"\n")
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update in this package)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden (go test -update rewrites it after review):\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}
