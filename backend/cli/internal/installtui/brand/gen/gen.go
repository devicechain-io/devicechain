// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package gen rasterises the DeviceChain mark into terminal cells. It is the
// maintainer-time half of the brand package: cmd/genbrand writes its output
// into brand/data, and the brand tests regenerate it from the SVG to prove the
// committed cells are fresh. Standard library only.
//
// The algorithm is fixed by the installer specification:
//
//  1. read only the SVG subset the mark uses (path with M/m L/l H/h V/v Z/z,
//     filled even-odd; polygon; solid #RRGGBB fills); anything else fails;
//  2. sample each logical pixel at 4x4 sub-pixel centres, later shapes
//     painting over earlier ones;
//  3. a pixel takes its most frequent facet (ties to the later shape) when at
//     least half its samples are covered, so colours are the canonical facet
//     fills and nothing is blended into an assumed background;
//  4. pack two vertical pixels into one terminal cell.
//
// Logical pixels are square. A terminal cell is about twice as tall as it is
// wide, so a cell holding two stacked pixels is square too, and the mark keeps
// the proportions of the SVG: the hexagon is 28 pixels wide in a 32-pixel grid.
package gen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Supersample is the number of samples per logical pixel along each axis.
const Supersample = 4

// Version changes whenever the output format or algorithm changes, so a stale
// manifest is distinguishable from a stale SVG.
const Version = 1

// Sizes are the logical pixel grids generated: the large mark (32 px, 32x16
// cells) and the compact mark (24 px, 24x12 cells).
var Sizes = []Size{{Name: "large", Pixels: 32}, {Name: "compact", Pixels: 24}}

// Size names one generated mark.
type Size struct {
	Name   string
	Pixels int
}

// Part classifies a facet for the animation: the hexagonal rim or the cube.
type Part string

const (
	Rim  Part = "rim"
	Cube Part = "cube"
)

// cubeGlyphs are the ASCII silhouette characters for the cube's shapes in
// document order: the backdrop, the two top triangles, the left and the right
// face. The rim is always '#'.
var cubeGlyphs = []string{"+", "/", "\\", ":", "."}

// Facet is one filled shape of the SVG, indexed from 1 in document order.
type Facet struct {
	Index   int    `json:"index"`
	Fill    string `json:"fill"`
	ANSI256 int    `json:"ansi256"`
	Part    Part   `json:"part"`
	ASCII   string `json:"ascii"`
}

// Mark is the manifest entry for one generated size.
type Mark struct {
	Name   string `json:"name"`
	Pixels int    `json:"pixels"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	// Axis is the mark's vertical axis of symmetry in cell columns, measured
	// from the left edge of column 0 (so 16 means between columns 15 and 16).
	Axis  float64 `json:"axis"`
	Cells string  `json:"cells"`
	ASCII string  `json:"ascii"`
}

// Manifest describes the generated files and the source they came from.
type Manifest struct {
	Generator    string    `json:"generator"`
	Version      int       `json:"version"`
	Source       string    `json:"source"`
	SourceSHA256 string    `json:"sourceSHA256"`
	ViewBox      []float64 `json:"viewBox"`
	Supersample  int       `json:"supersample"`
	Facets       []Facet   `json:"facets"`
	Marks        []Mark    `json:"marks"`
}

// SourcePath is the repository-relative path of the mark, recorded in the
// manifest.
const SourcePath = "branding/logos/symbol.svg"

type point struct{ x, y float64 }

type shape struct {
	fill    string
	subs    [][]point
	evenOdd bool // a path: all subpaths together, even-odd
}

// Generate returns the generated files keyed by their name within brand/data.
// The output is a pure function of the SVG bytes.
func Generate(svg []byte) (map[string][]byte, error) {
	vb, shapes, err := parse(svg)
	if err != nil {
		return nil, err
	}
	parts, err := classify(shapes)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(svg)
	m := Manifest{
		Generator: "genbrand", Version: Version, Source: SourcePath,
		SourceSHA256: hex.EncodeToString(sum[:]), ViewBox: vb[:], Supersample: Supersample,
	}
	cube := 0
	for i, s := range shapes {
		r, g, b, err := ParseHex(s.fill)
		if err != nil {
			return nil, err
		}
		f := Facet{Index: i + 1, Fill: s.fill, ANSI256: Nearest256(r, g, b), Part: parts[i], ASCII: "#"}
		if parts[i] == Cube {
			f.ASCII = cubeGlyphs[cube]
			cube++
		}
		m.Facets = append(m.Facets, f)
	}
	out := map[string][]byte{}
	for _, size := range Sizes {
		grid := raster(vb, shapes, size.Pixels)
		cellsName, asciiName := size.Name+".cells", size.Name+".ascii"
		out[cellsName] = encodeCells(grid)
		out[asciiName] = encodeASCII(grid, m.Facets)
		minX, maxX := extentX(shapes)
		axis := ((minX+maxX)/2 - vb[0]) / vb[2] * float64(size.Pixels)
		m.Marks = append(m.Marks, Mark{
			Name: size.Name, Pixels: size.Pixels, Cols: size.Pixels, Rows: size.Pixels / 2,
			Axis: math.Round(axis*1000) / 1000, Cells: cellsName, ASCII: asciiName,
		})
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	out["manifest.json"] = append(manifest, '\n')
	return out, nil
}

func parse(svg []byte) ([4]float64, []shape, error) {
	var vb [4]float64
	dec := xml.NewDecoder(bytes.NewReader(svg))
	depth := 0
	var shapes []shape
	sawRoot := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return vb, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if t.Name.Local != "svg" {
					return vb, nil, fmt.Errorf("root element %q, want svg", t.Name.Local)
				}
				sawRoot = true
				for _, a := range t.Attr {
					switch {
					case a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns"):
					case a.Name.Local == "viewBox":
						f := strings.Fields(strings.ReplaceAll(a.Value, ",", " "))
						if len(f) != 4 {
							return vb, nil, fmt.Errorf("viewBox %q", a.Value)
						}
						for i := range f {
							if vb[i], err = strconv.ParseFloat(f[i], 64); err != nil {
								return vb, nil, fmt.Errorf("viewBox %q: %w", a.Value, err)
							}
						}
					default:
						return vb, nil, fmt.Errorf("unsupported svg attribute %q", a.Name.Local)
					}
				}
				if vb[2] <= 0 || vb[2] != vb[3] {
					return vb, nil, fmt.Errorf("viewBox %v must be a non-empty square", vb)
				}
				continue
			}
			if depth != 2 {
				return vb, nil, fmt.Errorf("unsupported nested element %q", t.Name.Local)
			}
			s, err := parseShape(t)
			if err != nil {
				return vb, nil, err
			}
			if s != nil {
				shapes = append(shapes, *s)
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 2 {
				// Text inside title or desc is fine; text in a shape is not.
				continue
			}
			if len(bytes.TrimSpace(t)) != 0 {
				return vb, nil, fmt.Errorf("unexpected text %q", string(t))
			}
		}
	}
	if !sawRoot || len(shapes) == 0 {
		return vb, nil, errors.New("no shapes")
	}
	return vb, shapes, nil
}

var fillPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func parseShape(t xml.StartElement) (*shape, error) {
	name := t.Name.Local
	if name == "title" || name == "desc" {
		return nil, nil
	}
	if name != "path" && name != "polygon" {
		return nil, fmt.Errorf("unsupported element %q", name)
	}
	var fill, geometry string
	for _, a := range t.Attr {
		switch {
		case a.Name.Local == "fill":
			fill = a.Value
		case name == "path" && a.Name.Local == "d", name == "polygon" && a.Name.Local == "points":
			geometry = a.Value
		default:
			return nil, fmt.Errorf("unsupported %s attribute %q", name, a.Name.Local)
		}
	}
	if !fillPattern.MatchString(fill) {
		return nil, fmt.Errorf("%s fill %q is not a solid #RRGGBB colour", name, fill)
	}
	s := shape{fill: strings.ToUpper(fill)}
	if name == "path" {
		subs, err := pathSubpaths(geometry)
		if err != nil {
			return nil, err
		}
		s.subs, s.evenOdd = subs, true
		return &s, nil
	}
	nums, err := numbers(geometry)
	if err != nil {
		return nil, err
	}
	if len(nums) < 6 || len(nums)%2 != 0 {
		return nil, fmt.Errorf("polygon points %q", geometry)
	}
	var poly []point
	for i := 0; i < len(nums); i += 2 {
		poly = append(poly, point{nums[i], nums[i+1]})
	}
	s.subs = [][]point{poly}
	return &s, nil
}

var (
	pathToken   = regexp.MustCompile(`[A-Za-z]|-?\d*\.?\d+(?:[eE][-+]?\d+)?|[\s,]+|.`)
	numberToken = regexp.MustCompile(`^-?\d*\.?\d+(?:[eE][-+]?\d+)?$`)
)

func numbers(s string) ([]float64, error) {
	var out []float64
	for _, tok := range pathToken.FindAllString(s, -1) {
		if strings.TrimSpace(strings.ReplaceAll(tok, ",", "")) == "" {
			continue
		}
		if !numberToken.MatchString(tok) {
			return nil, fmt.Errorf("unexpected %q in %q", tok, s)
		}
		v, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func pathSubpaths(d string) ([][]point, error) {
	var toks []string
	for _, tok := range pathToken.FindAllString(d, -1) {
		if strings.TrimSpace(strings.ReplaceAll(tok, ",", "")) != "" {
			toks = append(toks, tok)
		}
	}
	var subs [][]point
	var cur []point
	var x, y float64
	var start point
	cmd := byte(0)
	for i := 0; i < len(toks); {
		t := toks[i]
		if len(t) == 1 && (t[0] >= 'A' && t[0] <= 'Z' || t[0] >= 'a' && t[0] <= 'z') {
			cmd = t[0]
			i++
			if cmd == 'Z' || cmd == 'z' {
				if len(cur) != 0 {
					subs = append(subs, cur)
					cur = nil
				}
				x, y = start.x, start.y
				continue
			}
			if !strings.ContainsRune("MmLlHhVv", rune(cmd)) {
				return nil, fmt.Errorf("unsupported path command %q", t)
			}
			continue
		}
		num := func() (float64, error) {
			if i >= len(toks) || !numberToken.MatchString(toks[i]) {
				return 0, fmt.Errorf("path %q: expected a number at token %d", d, i)
			}
			v, err := strconv.ParseFloat(toks[i], 64)
			i++
			return v, err
		}
		var a, b float64
		var err error
		switch cmd {
		case 'M', 'm', 'L', 'l':
			if a, err = num(); err == nil {
				b, err = num()
			}
		case 'H', 'h', 'V', 'v':
			a, err = num()
		default:
			return nil, fmt.Errorf("path %q: number %q without a command", d, t)
		}
		if err != nil {
			return nil, err
		}
		switch cmd {
		case 'M', 'm':
			if cmd == 'm' {
				a, b = x+a, y+b
			}
			if len(cur) != 0 {
				subs = append(subs, cur)
			}
			x, y = a, b
			cur, start = []point{{x, y}}, point{x, y}
			// Further pairs after a moveto are linetos.
			cmd = map[byte]byte{'M': 'L', 'm': 'l'}[cmd]
			continue
		case 'L':
			x, y = a, b
		case 'l':
			x, y = x+a, y+b
		case 'H':
			x = a
		case 'h':
			x += a
		case 'V':
			y = a
		case 'v':
			y += a
		}
		if len(cur) == 0 {
			return nil, fmt.Errorf("path %q: drawing before a moveto", d)
		}
		cur = append(cur, point{x, y})
	}
	if len(cur) != 0 {
		subs = append(subs, cur)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("empty path %q", d)
	}
	return subs, nil
}

// classify finds the rim and the cube. The first shape must be the frame: a
// path whose second subpath is the hexagonal opening. A shape is part of the
// cube when every vertex lies strictly inside that opening.
func classify(shapes []shape) ([]Part, error) {
	if !shapes[0].evenOdd || len(shapes[0].subs) != 2 {
		return nil, errors.New("the first shape must be the frame: a path with an outer outline and one opening")
	}
	hole := shapes[0].subs[1]
	parts := make([]Part, len(shapes))
	cubes := 0
	for i, s := range shapes {
		parts[i] = Rim
		if i == 0 {
			continue
		}
		inside := true
		for _, sub := range s.subs {
			for _, p := range sub {
				if !insidePolygon(p, hole) {
					inside = false
				}
			}
		}
		if inside {
			parts[i] = Cube
			cubes++
		}
	}
	if cubes != len(cubeGlyphs) {
		return nil, fmt.Errorf("found %d cube shapes, want %d", cubes, len(cubeGlyphs))
	}
	return parts, nil
}

func extentX(shapes []shape) (float64, float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, s := range shapes {
		for _, sub := range s.subs {
			for _, p := range sub {
				lo, hi = math.Min(lo, p.x), math.Max(hi, p.x)
			}
		}
	}
	return lo, hi
}

// insidePolygon is the even-odd crossing test.
func insidePolygon(p point, poly []point) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

func (s shape) contains(p point) bool {
	if s.evenOdd {
		in := false
		for _, sub := range s.subs {
			if insidePolygon(p, sub) {
				in = !in
			}
		}
		return in
	}
	for _, sub := range s.subs {
		if insidePolygon(p, sub) {
			return true
		}
	}
	return false
}

// raster returns a px x px grid of facet indices (0 = transparent).
func raster(vb [4]float64, shapes []shape, px int) [][]uint8 {
	scale := float64(px*Supersample) / vb[2]
	grid := make([][]uint8, px)
	counts := make([]int, len(shapes)+1)
	for y := 0; y < px; y++ {
		grid[y] = make([]uint8, px)
		for x := 0; x < px; x++ {
			for i := range counts {
				counts[i] = 0
			}
			for j := 0; j < Supersample; j++ {
				for i := 0; i < Supersample; i++ {
					p := point{
						vb[0] + (float64(x*Supersample+i)+0.5)/scale,
						vb[1] + (float64(y*Supersample+j)+0.5)/scale,
					}
					k := 0
					for n, s := range shapes {
						if s.contains(p) {
							k = n + 1
						}
					}
					counts[k]++
				}
			}
			opaque := Supersample*Supersample - counts[0]
			if opaque*2 < Supersample*Supersample {
				continue
			}
			best := 0
			for k := 1; k < len(counts); k++ {
				if counts[k] > 0 && (best == 0 || counts[k] >= counts[best]) {
					best = k // the later shape wins a tie
				}
			}
			grid[y][x] = uint8(best)
		}
	}
	return grid
}

const facetDigits = "0123456789abcdefghijklmnopqrstuvwxyz"

// encodeCells writes one line per cell row; each cell is two characters, the
// top and bottom pixel's facet (base 36, 0 = transparent).
func encodeCells(grid [][]uint8) []byte {
	var b bytes.Buffer
	for r := 0; r+1 < len(grid); r += 2 {
		for x := range grid[r] {
			b.WriteByte(facetDigits[grid[r][x]])
			b.WriteByte(facetDigits[grid[r+1][x]])
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// encodeASCII writes the silhouette: one character per cell, the top pixel's
// glyph, else the bottom pixel's, else a space; trailing spaces trimmed.
func encodeASCII(grid [][]uint8, facets []Facet) []byte {
	var b bytes.Buffer
	for r := 0; r+1 < len(grid); r += 2 {
		var line strings.Builder
		for x := range grid[r] {
			k := grid[r][x]
			if k == 0 {
				k = grid[r+1][x]
			}
			if k == 0 {
				line.WriteByte(' ')
			} else {
				line.WriteString(facets[k-1].ASCII)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// ParseHex parses a "#RRGGBB" colour.
func ParseHex(s string) (uint8, uint8, uint8, error) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, fmt.Errorf("colour %q", s)
	}
	var rgb [3]uint8
	for i := range rgb {
		// Each channel is parsed at 8 bits, so it is range-checked, never truncated.
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("colour %q", s)
		}
		rgb[i] = uint8(v)
	}
	return rgb[0], rgb[1], rgb[2], nil
}

// Nearest256 maps a colour to the nearest xterm-256 colour in 16-255 by
// squared RGB distance, ties to the lower index. 0-15 are skipped because
// terminal themes redefine them.
func Nearest256(r, g, b uint8) int {
	best, bestD := 16, math.MaxInt
	for i := 16; i < 256; i++ {
		cr, cg, cb := XtermRGB(i)
		dr, dg, db := int(r)-int(cr), int(g)-int(cg), int(b)-int(cb)
		if d := dr*dr + dg*dg + db*db; d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// XtermRGB is the standard xterm palette for 16-255.
func XtermRGB(i int) (uint8, uint8, uint8) {
	if i >= 232 {
		v := uint8(8 + 10*(i-232))
		return v, v, v
	}
	levels := [6]uint8{0, 95, 135, 175, 215, 255}
	i -= 16
	return levels[i/36], levels[i/6%6], levels[i%6]
}
