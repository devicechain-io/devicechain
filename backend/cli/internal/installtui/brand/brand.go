// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package brand holds the installer's generated logo cells, their renderer and
// the splash model. The cells in data/ are generated from
// branding/logos/symbol.svg by cmd/genbrand and embedded; target machines need
// no graphics stack and no network. The splash model is pure: it is driven by
// explicit times, key presses and sizes, and never touches the terminal.
package brand

//go:generate go run ./cmd/genbrand -svg ../../../../../branding/logos/symbol.svg -out data

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installtui/brand/gen"
)

//go:embed data/manifest.json data/*.cells data/*.ascii
var data embed.FS

// Cell is one terminal cell: two stacked square pixels, each a facet index
// (0 is transparent).
type Cell struct{ Top, Bottom uint8 }

// Facet is a canonical colour of the mark.
type Facet struct {
	gen.Facet
	RGB [3]uint8
}

// Mark is one generated size of the logo.
type Mark struct {
	Name       string
	Pixels     int
	Cols, Rows int
	// Axis is the mark's axis of symmetry in cell columns from the block's
	// left edge.
	Axis  float64
	Cells [][]Cell
	// ASCII is the silhouette, one string per cell row, trailing spaces
	// trimmed.
	ASCII []string
}

var (
	manifest gen.Manifest
	facets   []Facet
	large    Mark
	compact  Mark
)

func init() {
	if err := load(); err != nil {
		panic("brand: embedded logo data is invalid: " + err.Error())
	}
}

func load() error {
	b, err := data.ReadFile("data/manifest.json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return err
	}
	facets = nil
	for i, f := range manifest.Facets {
		if f.Index != i+1 {
			return fmt.Errorf("facet %d has index %d", i+1, f.Index)
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(f.Fill, "#"), 16, 32)
		if err != nil {
			return err
		}
		facets = append(facets, Facet{Facet: f, RGB: [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}})
	}
	for _, m := range manifest.Marks {
		mark, err := loadMark(m)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
		switch m.Name {
		case "large":
			large = mark
		case "compact":
			compact = mark
		default:
			return fmt.Errorf("unknown mark %q", m.Name)
		}
	}
	if large.Cells == nil || compact.Cells == nil {
		return fmt.Errorf("missing a mark")
	}
	return nil
}

func loadMark(m gen.Mark) (Mark, error) {
	mark := Mark{Name: m.Name, Pixels: m.Pixels, Cols: m.Cols, Rows: m.Rows, Axis: m.Axis}
	b, err := data.ReadFile("data/" + m.Cells)
	if err != nil {
		return mark, err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != m.Rows {
		return mark, fmt.Errorf("%d cell rows, want %d", len(lines), m.Rows)
	}
	for _, line := range lines {
		if len(line) != 2*m.Cols {
			return mark, fmt.Errorf("cell row %q is not %d cells", line, m.Cols)
		}
		row := make([]Cell, m.Cols)
		for x := range row {
			top, err1 := strconv.ParseUint(line[2*x:2*x+1], 36, 8)
			bottom, err2 := strconv.ParseUint(line[2*x+1:2*x+2], 36, 8)
			if err1 != nil || err2 != nil || int(top) > len(facets) || int(bottom) > len(facets) {
				return mark, fmt.Errorf("bad cell %q", line[2*x:2*x+2])
			}
			row[x] = Cell{uint8(top), uint8(bottom)}
		}
		mark.Cells = append(mark.Cells, row)
	}
	b, err = data.ReadFile("data/" + m.ASCII)
	if err != nil {
		return mark, err
	}
	mark.ASCII = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(mark.ASCII) != m.Rows {
		return mark, fmt.Errorf("%d ASCII rows, want %d", len(mark.ASCII), m.Rows)
	}
	return mark, nil
}

// Large is the 32x16-cell mark.
func Large() Mark { return large }

// Compact is the 24x12-cell mark.
func Compact() Mark { return compact }

// Facets are the mark's canonical colours, indexed from 1 by Cell values.
func Facets() []Facet { return facets }

// Pixel returns the facet at pixel (x, y) of the mark.
func (m Mark) Pixel(x, y int) uint8 {
	c := m.Cells[y/2][x]
	if y%2 == 0 {
		return c.Top
	}
	return c.Bottom
}
