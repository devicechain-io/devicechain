// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sdlcompat

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/scanner"

	"github.com/graph-gophers/graphql-go/ast"
)

// defaultDeprecationReason is what the GraphQL spec — and graphql-go — serve for a bare
// `@deprecated`. It names nothing, so it is refused exactly like an absent reason, and
// refused when written out by hand too: spelling the default does not make it say more.
const defaultDeprecationReason = "No longer supported"

// Finding is one element that breaks a schema rule this package enforces on the
// current tree alone (no baseline involved).
type Finding struct {
	Schema string
	Coord  string
	Detail string
}

func (f Finding) String() string { return fmt.Sprintf("%s: %s: %s", f.Schema, f.Coord, f.Detail) }

// LintDeprecations reports every `@deprecated` that carries no reason a reader can act
// on. The versioning policy promises that a deprecated field or argument is marked with
// a reason that names its replacement, visible in introspection and the reference. A
// bare `@deprecated` is served as "No longer supported", which tells a client it must
// move without telling it where to.
func LintDeprecations(schemas []Parsed) []Finding {
	var out []Finding
	for _, p := range schemas {
		check := func(coord string, dl ast.DirectiveList) {
			d := dl.Get("deprecated")
			if d == nil {
				return
			}
			if why := reasonProblem(d); why != "" {
				out = append(out, Finding{p.Name, coord, why})
			}
		}
		inputs := func(owner, open, closing string, l ast.ArgumentsDefinition) {
			for _, v := range l {
				check(owner+open+v.Name.Name+closing, v.Directives)
			}
		}
		for _, name := range sortedKeys(p.AST.Types) {
			if builtinTypes[name] {
				continue
			}
			switch t := p.AST.Types[name].(type) {
			case *ast.ObjectTypeDefinition:
				for _, f := range t.Fields {
					check(name+"."+f.Name, f.Directives)
					inputs(name+"."+f.Name, "(", ":)", f.Arguments)
				}
			case *ast.InterfaceTypeDefinition:
				for _, f := range t.Fields {
					check(name+"."+f.Name, f.Directives)
					inputs(name+"."+f.Name, "(", ":)", f.Arguments)
				}
			case *ast.InputObject:
				inputs(name, ".", "", t.Values)
			case *ast.EnumTypeDefinition:
				for _, v := range t.EnumValuesDefinition {
					check(name+"."+v.EnumValue, v.Directives)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Coord < out[j].Coord
	})
	return out
}

func reasonProblem(d *ast.Directive) string {
	const fix = `write @deprecated(reason: "Use <replacement> instead.") naming what replaces it`
	v, ok := d.Arguments.Get("reason")
	if !ok {
		return "@deprecated has no reason; " + fix
	}
	p, ok := v.(*ast.PrimitiveValue)
	if !ok || p.Type != scanner.String {
		return "@deprecated reason is not a string; " + fix
	}
	text, err := strconv.Unquote(p.Text)
	if err != nil {
		// A block string, which Unquote does not read. Strip the fences.
		text = strings.Trim(p.Text, `"`)
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "@deprecated reason is blank; " + fix
	case strings.EqualFold(text, defaultDeprecationReason):
		// graphql-go fills the default into a bare @deprecated, so this is also what an
		// absent reason looks like by the time the AST is read.
		return "@deprecated has no reason of its own (it is served as the spec default, " +
			"\"" + defaultDeprecationReason + "\", which names no replacement); " + fix
	}
	return ""
}
