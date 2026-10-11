// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sdldesc checks that every element of every served GraphQL schema carries a
// description, against a per-schema allowlist of the elements that do not yet — a list
// that can only shrink.
//
// WHY. The served SDL is the API reference: it is published verbatim at /schema/ on the
// docs site, an agent integrating against DeviceChain reads it instead of the Go, and
// every GraphQL tool (codegen, GraphiQL's docs pane, a generated reference page) reads
// the descriptions in it. A field with no description is a field whose contract exists
// only in the resolver.
//
// WHICH TEXT COUNTS. Only a spec description — a "string" or """block string""" directly
// above the element — counts. A `#` comment does NOT, and that is the distinction this
// check exists to make, not a technicality:
//
//   - `"""` is the PUBLIC contract. The services parse with UseStringDescriptions (core
//     graphql.MustParseSchema), so this is what introspection serves and what every
//     standard tool reads. It must stand on its own for a reader outside the project, so
//     it may not cite an internal decision record.
//   - `#` is a MAINTAINER note. It may carry rationale, history and ADR citations, and no
//     tool presents it as documentation.
//
// Before this check, every schema documented itself in `#` comments, which graphql-go's
// legacy mode served as descriptions and every other tool dropped. One text was doing
// both jobs, so public readers got maintainer rationale and tools got nothing.
//
// THE ALLOWLIST. undescribed/<published-name>.txt lists, one schema coordinate per line,
// the elements of that schema that have no description yet. It is the ratchet:
//
//   - an element with no description that is NOT listed fails (new API must arrive
//     described);
//   - a listed element that HAS a description, or no longer exists, fails as a stale
//     entry (so the list cannot hide progress, and only shrinks: run -prune);
//   - a list for a schema that is not served fails (a renamed schema must move its list).
//
// One file per schema, so builders filling different schemas never touch the same file.
//
// Coordinates follow the GraphQL schema-coordinate syntax: Type, Type.field,
// Type.field(arg:), Enum.VALUE, Input.field, @directive, @directive(arg:).
package sdldesc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/ast"
)

// Kind classifies a coordinate, for the coverage report.
type Kind string

const (
	KindType       Kind = "type"
	KindField      Kind = "field"
	KindInputField Kind = "input field"
	KindArgument   Kind = "argument"
	KindEnumValue  Kind = "enum value"
	KindDirective  Kind = "directive"
)

// Kinds is the report order.
var Kinds = []Kind{KindType, KindField, KindInputField, KindArgument, KindEnumValue, KindDirective}

// Element is one describable element of a schema.
type Element struct {
	Coord string
	Kind  Kind
	Desc  string
}

// Served is one served schema, parsed.
type Served struct {
	// Name is the published name: "<area>", "<area>-admin" or "<area>-settings" — the
	// stem of the file the docs site serves at /schema/, and of its allowlist.
	Name     string
	Path     string
	Elements []Element
}

// Finding is one failure.
type Finding struct {
	Schema string
	Coord  string
	Detail string
}

func (f Finding) String() string {
	if f.Coord == "" {
		return fmt.Sprintf("%s: %s", f.Schema, f.Detail)
	}
	return fmt.Sprintf("%s: %s: %s", f.Schema, f.Coord, f.Detail)
}

var mountSuffix = map[string]string{
	schemaplane.MountTenant:   "",
	schemaplane.MountAdmin:    "-admin",
	schemaplane.MountSettings: "-settings",
}

// Parse parses one SDL document the way the services do (string descriptions) and
// lists its describable elements in a stable order.
func Parse(sdl string) ([]Element, error) {
	s, err := graphql.ParseSchema(sdl, nil, graphql.UseStringDescriptions(), graphql.UseFieldResolvers())
	if err != nil {
		return nil, err
	}
	return elements(s.ASTSchema()), nil
}

// builtins are the names graphql-go adds to every schema: scalars, introspection types
// and the spec directives. They are learned from an empty schema rather than written
// down, so a library upgrade that adds one cannot make every schema fail.
var builtinTypes, builtinDirectives = func() (map[string]bool, map[string]bool) {
	s := graphql.MustParseSchema(`type Query { x: Int }`, nil, graphql.UseFieldResolvers()).ASTSchema()
	ts, ds := map[string]bool{}, map[string]bool{}
	for n := range s.Types {
		if n != "Query" {
			ts[n] = true
		}
	}
	for n := range s.Directives {
		ds[n] = true
	}
	return ts, ds
}()

func elements(s *ast.Schema) []Element {
	roots := map[string]bool{}
	for _, t := range s.RootOperationTypes {
		if t != nil {
			roots[t.TypeName()] = true
		}
	}
	var out []Element
	add := func(coord string, k Kind, desc string) {
		out = append(out, Element{Coord: coord, Kind: k, Desc: desc})
	}
	args := func(prefix string, list ast.ArgumentsDefinition) {
		for _, a := range list {
			add(prefix+"("+a.Name.Name+":)", KindArgument, a.Desc)
		}
	}
	fields := func(typ string, list ast.FieldsDefinition) {
		for _, f := range list {
			add(typ+"."+f.Name, KindField, f.Desc)
			args(typ+"."+f.Name, f.Arguments)
		}
	}
	for name, t := range s.Types {
		if builtinTypes[name] {
			continue
		}
		switch t := t.(type) {
		case *ast.ObjectTypeDefinition:
			// A root operation type is a container, not a thing a reader looks up: its
			// FIELDS are the API, and each of them must be described.
			if !roots[name] {
				add(name, KindType, t.Desc)
			}
			fields(name, t.Fields)
		case *ast.InterfaceTypeDefinition:
			add(name, KindType, t.Desc)
			fields(name, t.Fields)
		case *ast.InputObject:
			add(name, KindType, t.Desc)
			for _, v := range t.Values {
				add(name+"."+v.Name.Name, KindInputField, v.Desc)
			}
		case *ast.EnumTypeDefinition:
			add(name, KindType, t.Desc)
			for _, v := range t.EnumValuesDefinition {
				add(name+"."+v.EnumValue, KindEnumValue, v.Desc)
			}
		case *ast.Union:
			add(name, KindType, t.Desc)
		case *ast.ScalarTypeDefinition:
			add(name, KindType, t.Desc)
		default:
			// A kind this check does not know how to enumerate must not be passed over
			// as described: it is reported as an undescribed element whose coordinate
			// names the unhandled kind, so the run fails until the enumeration learns it.
			// (That coordinate could be allowlisted by hand; a reviewer would see a line
			// reading "unhandled kind" go into the list.)
			add(name+" (unhandled kind "+t.Kind()+")", KindType, "")
		}
	}
	for name, d := range s.Directives {
		if builtinDirectives[name] {
			continue
		}
		add("@"+name, KindDirective, d.Desc)
		args("@"+name, d.Arguments)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Coord < out[j].Coord })
	return out
}

// LoadServed parses every schema served under servicesDir/<area>/graphql.
func LoadServed(servicesDir string) ([]Served, error) {
	areas, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil, err
	}
	var out []Served
	for _, a := range areas {
		dir := filepath.Join(servicesDir, a.Name(), "graphql")
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		found, err := schemaplane.Dir(dir)
		if err != nil {
			return nil, err
		}
		for _, sc := range found {
			sdl, err := os.ReadFile(sc.Path)
			if err != nil {
				return nil, err
			}
			els, err := Parse(string(sdl))
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", sc.Path, err)
			}
			suffix, ok := mountSuffix[sc.Mount]
			if !ok {
				return nil, fmt.Errorf("%s: mount %s has no published name", sc.Path, sc.Mount)
			}
			out = append(out, Served{Name: a.Name() + suffix, Path: sc.Path, Elements: els})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no served schema under %s; a check that parsed nothing passes everything", servicesDir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadAllowlists reads every <name>.txt under dir. Blank lines and lines starting with
// '#' are ignored.
func ReadAllowlists(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var coords []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			coords = append(coords, l)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
		out[strings.TrimSuffix(e.Name(), ".txt")] = coords
	}
	return out, nil
}

var (
	adrRe         = regexp.MustCompile(`(?i)\bADR[- ]?\d`)
	placeholderRe = regexp.MustCompile(`(?i)^\s*(todo|tbd|fixme|xxx)\b`)
	nonWord       = regexp.MustCompile(`[^a-z0-9]+`)
)

// quality returns why a non-empty description is not acceptable, or "".
func quality(e Element) string {
	if adrRe.MatchString(e.Desc) {
		return "description cites a decision record; a description is public text — say the thing " +
			"itself, and keep the citation in a # comment"
	}
	if placeholderRe.MatchString(e.Desc) {
		return "description is a placeholder"
	}
	norm := func(s string) string { return nonWord.ReplaceAllString(strings.ToLower(s), "") }
	if norm(e.Desc) == norm(leafName(e.Coord)) {
		return "description only restates the name"
	}
	return ""
}

// leafName is the name a coordinate ends in: the argument, field, value or type.
func leafName(coord string) string {
	if i := strings.Index(coord, "("); i >= 0 {
		return strings.TrimSuffix(coord[i+1:], ":)")
	}
	if i := strings.LastIndex(coord, "."); i >= 0 {
		return coord[i+1:]
	}
	return strings.TrimPrefix(coord, "@")
}

// Check compares every served schema with its allowlist.
func Check(served []Served, allow map[string][]string) []Finding {
	var out []Finding
	names := map[string]bool{}
	for _, s := range served {
		names[s.Name] = true
		listed := map[string]bool{}
		for _, c := range allow[s.Name] {
			if listed[c] {
				out = append(out, Finding{s.Name, c, "listed twice in the allowlist"})
			}
			listed[c] = true
		}
		present := map[string]bool{}
		for _, e := range s.Elements {
			present[e.Coord] = true
			described := strings.TrimSpace(e.Desc) != ""
			switch {
			case described && listed[e.Coord]:
				out = append(out, Finding{s.Name, e.Coord,
					"is described now; remove it from the allowlist (go run ./cmd/sdldesc -prune)"})
			case !described && !listed[e.Coord]:
				out = append(out, Finding{s.Name, e.Coord,
					"has no description. Add a \"\"\"description\"\"\" above it in " + filepath.ToSlash(s.Path) +
						" (a # comment does not count)"})
			case described:
				if why := quality(e); why != "" {
					out = append(out, Finding{s.Name, e.Coord, why})
				}
			}
		}
		for _, c := range allow[s.Name] {
			if !present[c] {
				out = append(out, Finding{s.Name, c,
					"is in the allowlist but not in the schema; remove it (go run ./cmd/sdldesc -prune)"})
			}
		}
	}
	for n := range allow {
		if !names[n] {
			out = append(out, Finding{n, "", "allowlist names a schema that is not served (renamed or removed?)"})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Coord < out[j].Coord
	})
	return out
}

// Coverage is one schema's described/total counts by kind.
type Coverage struct {
	Name      string
	Described map[Kind]int
	Total     map[Kind]int
}

// Sum returns described and total across kinds.
func (c Coverage) Sum() (int, int) {
	d, t := 0, 0
	for _, k := range Kinds {
		d += c.Described[k]
		t += c.Total[k]
	}
	return d, t
}

// Measure computes coverage for each schema.
func Measure(served []Served) []Coverage {
	var out []Coverage
	for _, s := range served {
		c := Coverage{Name: s.Name, Described: map[Kind]int{}, Total: map[Kind]int{}}
		for _, e := range s.Elements {
			c.Total[e.Kind]++
			if strings.TrimSpace(e.Desc) != "" {
				c.Described[e.Kind]++
			}
		}
		out = append(out, c)
	}
	return out
}

// PrintCoverage writes a coverage table.
func PrintCoverage(w io.Writer, cov []Coverage) {
	fmt.Fprintf(w, "%-30s", "schema")
	for _, k := range Kinds {
		fmt.Fprintf(w, " %13s", k)
	}
	fmt.Fprintf(w, " %15s\n", "all")
	tot := Coverage{Name: "TOTAL", Described: map[Kind]int{}, Total: map[Kind]int{}}
	row := func(c Coverage) {
		fmt.Fprintf(w, "%-30s", c.Name)
		for _, k := range Kinds {
			fmt.Fprintf(w, " %13s", fmt.Sprintf("%d/%d", c.Described[k], c.Total[k]))
		}
		d, t := c.Sum()
		pct := 0.0
		if t > 0 {
			pct = 100 * float64(d) / float64(t)
		}
		fmt.Fprintf(w, " %15s\n", fmt.Sprintf("%d/%d %3.0f%%", d, t, pct))
	}
	for _, c := range cov {
		row(c)
		for _, k := range Kinds {
			tot.Described[k] += c.Described[k]
			tot.Total[k] += c.Total[k]
		}
	}
	row(tot)
}

// Undescribed lists the coordinates of s that carry no description.
func Undescribed(s Served) []string {
	var out []string
	for _, e := range s.Elements {
		if strings.TrimSpace(e.Desc) == "" {
			out = append(out, e.Coord)
		}
	}
	return out
}
