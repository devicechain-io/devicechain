// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package jsonschema classifies the differences between two versions of a JSON Schema
// (or an OpenAPI v3 schema, which is the same vocabulary for what is compared here) the
// way the rest of the API-stability gate classifies a GraphQL or command-line change:
// breaking, dangerous, or safe, for something written against the OLD schema.
//
// The same engine serves three callers. The device wire contract fails the build on a
// breaking change; the Helm values schema and the Instance CRD schema are best-effort
// surfaces whose changes are printed and never fail (the versioning policy lists them
// as not covered). The engine does not know which it is serving.
//
// Direction. A contract schema describes a document that one side writes and another
// reads, and for a device the platform is the reader of some (an event) and the writer
// of others (a command). The rules are the conservative union:
//
//   - a property that disappears, a property that becomes required, a type or enum that
//     narrows, a closed object that was open, a bound that tightens, a pattern that is
//     newly imposed, a constant that changes: BREAKING (whoever wrote against the old
//     schema is now refused, or whoever read it loses what it relied on);
//   - the opposite loosenings, a new enum value, a changed default, a removed pattern or
//     format, a dropped composition constraint: DANGEROUS - reported, never failing;
//   - a new optional property: SAFE.
//
// WHAT THE TOOL CANNOT DECIDE, IT FAILS. It does not evaluate a schema, so a change whose
// direction depends on the expressions - a changed pattern or format, an `if` or `not`
// that changed, composition entries that cannot be paired, an external $ref that points
// elsewhere, any validation keyword it does not model - is BREAKING and takes a
// declaration. Reporting such a change instead would let a narrowing written inside an
// `allOf` or a `then` pass a gate whose job is to catch narrowings. Entries of
// allOf/anyOf/oneOf and `then`/`else` that CAN be paired are compared like any other
// subschema, so a property newly required inside a conditional is found where it is.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-docsgql/apicompat/decl"
)

// Kinds maps every change kind to its severity; the declarations parser takes it.
var Kinds = map[string]decl.Severity{
	"schema-removed":                 decl.Breaking,
	"property-removed":               decl.Breaking,
	"required-added":                 decl.Breaking,
	"type-narrowed":                  decl.Breaking,
	"enum-narrowed":                  decl.Breaking,
	"const-changed":                  decl.Breaking,
	"additional-properties-closed":   decl.Breaking,
	"additional-properties-narrowed": decl.Breaking,
	"bound-tightened":                decl.Breaking,
	"pattern-added":                  decl.Breaking,
	"pattern-changed":                decl.Breaking,
	"format-added":                   decl.Breaking,
	"format-changed":                 decl.Breaking,
	"composition-narrowed":           decl.Breaking,
	"composition-changed":            decl.Breaking,
	"ref-changed":                    decl.Breaking,
	"keyword-changed":                decl.Breaking,
	"schema-changed":                 decl.Breaking,
	"items-constrained":              decl.Breaking,
	"required-removed":               decl.Dangerous,
	"type-widened":                   decl.Dangerous,
	"enum-widened":                   decl.Dangerous,
	"const-removed":                  decl.Dangerous,
	"additional-properties-opened":   decl.Dangerous,
	"bound-loosened":                 decl.Dangerous,
	"pattern-removed":                decl.Dangerous,
	"format-removed":                 decl.Dangerous,
	"default-changed":                decl.Dangerous,
	"composition-widened":            decl.Dangerous,
	"items-unconstrained":            decl.Dangerous,
	"schema-added":                   decl.Safe,
	"property-added":                 decl.Safe,
}

// Load reads one JSON document.
func Load(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return v, nil
}

type differ struct {
	surface string
	baseDoc any
	headDoc any
	out     []decl.Change
}

// Compare classifies every difference between two parsed schema documents. surface is
// what a declaration names (the schema file). The returned order is deterministic.
func Compare(surface string, base, head any) []decl.Change {
	d := &differ{surface: surface, baseDoc: base, headDoc: head}
	d.node("$", base, head, nil, nil)
	decl.Sort(d.out)
	return d.out
}

func (d *differ) add(coord, kind, detail string) {
	if _, ok := Kinds[kind]; !ok {
		// An unlisted kind would take the zero severity, Safe: a break that passes.
		panic("jsonschema: change kind " + kind + " is not in Kinds")
	}
	d.out = append(d.out, decl.Change{Surface: d.surface, Coord: coord, Kind: kind, Severity: Kinds[kind], Detail: detail})
}

// resolve follows local $ref chains ("#/$defs/x") in doc. A reference that cannot be
// followed (a file or URL, a dangling pointer) is left in place for the caller to
// compare as an opaque string. seen carries the refs on the current descent, so a
// recursive schema terminates: the second visit returns stop.
func resolve(doc, n any, seen map[string]bool) (out any, stop bool) {
	for i := 0; i < 32; i++ {
		m, ok := n.(map[string]any)
		if !ok {
			return n, false
		}
		ref, _ := m["$ref"].(string)
		if ref == "" || !strings.HasPrefix(ref, "#") {
			return n, false
		}
		if seen[ref] {
			return nil, true
		}
		target, ok := pointer(doc, ref[1:])
		if !ok {
			return n, false
		}
		seen[ref] = true
		// Sibling keywords of a $ref (allowed since draft 2019-09) override the target.
		tm, tok := target.(map[string]any)
		if !tok || len(m) == 1 {
			n = target
			continue
		}
		merged := map[string]any{}
		for k, v := range tm {
			merged[k] = v
		}
		for k, v := range m {
			if k != "$ref" {
				merged[k] = v
			}
		}
		n = merged
	}
	return n, false
}

func pointer(doc any, p string) (any, bool) {
	if p == "" {
		return doc, true
	}
	if !strings.HasPrefix(p, "/") {
		return nil, false
	}
	cur := doc
	for _, seg := range strings.Split(p[1:], "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[seg]
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

func cloneSeen(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (d *differ) node(path string, b, h any, bSeen, hSeen map[string]bool) {
	bSeen, hSeen = cloneSeen(bSeen), cloneSeen(hSeen)
	b, bStop := resolve(d.baseDoc, b, bSeen)
	h, hStop := resolve(d.headDoc, h, hSeen)
	if bStop && hStop {
		return
	}
	if bStop || hStop {
		// One side recursed back into a definition it is already inside and the other
		// did not: the shapes differ, and there is nothing further to pair.
		d.add(path, "schema-changed", "one side is a recursive reference and the other is not")
		return
	}
	bm, bok := b.(map[string]any)
	hm, hok := h.(map[string]any)
	if !bok || !hok {
		if !reflect.DeepEqual(b, h) {
			d.add(path, "schema-changed", fmt.Sprintf("%s -> %s", brief(b), brief(h)))
		}
		return
	}
	if br, hr := str(bm["$ref"]), str(hm["$ref"]); br != hr {
		d.add(path, "ref-changed", fmt.Sprintf("%q -> %q", br, hr))
	}

	d.types(path, bm, hm)
	d.enum(path, bm, hm)
	d.constant(path, bm, hm)
	d.bounds(path, bm, hm)
	d.pattern(path, bm, hm)
	d.format(path, bm, hm)
	d.multipleOf(path, bm, hm)
	d.uniqueItems(path, bm, hm)
	if !reflect.DeepEqual(bm["default"], hm["default"]) {
		d.add(path, "default-changed", fmt.Sprintf("%s -> %s", brief(bm["default"]), brief(hm["default"])))
	}
	d.composition(path, bm, hm, bSeen, hSeen)
	d.otherKeywords(path, bm, hm)

	d.properties(path, bm, hm, bSeen, hSeen)
	d.required(path, bm, hm)
	d.additional(path, bm, hm, bSeen, hSeen)
	d.items(path, bm, hm, bSeen, hSeen)
}

func (d *differ) types(path string, bm, hm map[string]any) {
	bt, ht := typeSet(bm["type"]), typeSet(hm["type"])
	if len(bt) == 0 && len(ht) == 0 {
		return
	}
	narrowed, widened := false, false
	switch {
	case len(bt) == 0: // was "any type"
		narrowed = true
	case len(ht) == 0:
		widened = true
	default:
		for _, t := range bt {
			// every integer is a number, so integer -> number is not a narrowing
			if !contains(ht, t) && !(t == "integer" && contains(ht, "number")) {
				narrowed = true
			}
		}
		for _, t := range ht {
			if !contains(bt, t) {
				widened = true
			}
		}
	}
	detail := fmt.Sprintf("%s -> %s", typeStr(bt), typeStr(ht))
	if narrowed {
		d.add(path, "type-narrowed", detail)
	}
	if widened {
		d.add(path, "type-widened", detail)
	}
}

func typeStr(ts []string) string {
	if len(ts) == 0 {
		return "(any)"
	}
	return strings.Join(ts, "|")
}

func typeSet(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = []string{t}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (d *differ) enum(path string, bm, hm map[string]any) {
	be, bok := bm["enum"].([]any)
	he, hok := hm["enum"].([]any)
	switch {
	case !bok && !hok:
		return
	case !bok:
		d.add(path, "enum-narrowed", "was unrestricted, now one of "+enumStr(he))
		return
	case !hok:
		d.add(path, "enum-widened", "enum constraint removed")
		return
	}
	bs, hs := valueSet(be), valueSet(he)
	var removed, added []string
	for v := range bs {
		if !hs[v] {
			removed = append(removed, v)
		}
	}
	for v := range hs {
		if !bs[v] {
			added = append(added, v)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	if len(removed) > 0 {
		d.add(path, "enum-narrowed", "no longer accepts "+strings.Join(removed, ", "))
	}
	if len(added) > 0 {
		d.add(path, "enum-widened", "now also accepts "+strings.Join(added, ", "))
	}
}

func valueSet(vs []any) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		b, _ := json.Marshal(v)
		m[string(b)] = true
	}
	return m
}

func enumStr(vs []any) string {
	var out []string
	for v := range valueSet(vs) {
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func (d *differ) constant(path string, bm, hm map[string]any) {
	bv, bok := bm["const"]
	hv, hok := hm["const"]
	switch {
	case bok && hok && !reflect.DeepEqual(bv, hv):
		d.add(path, "const-changed", fmt.Sprintf("%s -> %s", brief(bv), brief(hv)))
	case !bok && hok:
		d.add(path, "const-changed", "now must equal "+brief(hv))
	case bok && !hok:
		d.add(path, "const-removed", "no longer pinned to "+brief(bv))
	}
}

// bound keywords: raising a minimum or lowering a maximum tightens.
var minimums = []string{"minimum", "exclusiveMinimum", "minLength", "minItems", "minProperties"}
var maximums = []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems", "maxProperties"}

func (d *differ) bounds(path string, bm, hm map[string]any) {
	check := func(k string, lower bool) {
		bv, bok := num(bm[k])
		hv, hok := num(hm[k])
		var tighter, looser bool
		switch {
		case !bok && !hok:
			if !reflect.DeepEqual(bm[k], hm[k]) {
				// a boolean exclusiveMinimum (draft 4, OpenAPI 3.0) or a malformed bound
				d.add(path+"{"+k+"}", "keyword-changed", fmt.Sprintf("%s -> %s", brief(bm[k]), brief(hm[k])))
			}
			return
		case !bok:
			tighter = true
		case !hok:
			looser = true
		case bv == hv:
			return
		default:
			// raising a minimum or lowering a maximum tightens
			tighter = (lower && hv > bv) || (!lower && hv < bv)
			looser = !tighter
		}
		coord := path + "{" + k + "}"
		switch {
		case tighter:
			d.add(coord, "bound-tightened", fmt.Sprintf("%s -> %s", brief(bm[k]), brief(hm[k])))
		case looser:
			d.add(coord, "bound-loosened", fmt.Sprintf("%s -> %s", brief(bm[k]), brief(hm[k])))
		}
	}
	for _, k := range minimums {
		check(k, true)
	}
	for _, k := range maximums {
		check(k, false)
	}
}

func (d *differ) format(path string, bm, hm map[string]any) {
	bf, hf := bm["format"], hm["format"]
	switch {
	case reflect.DeepEqual(bf, hf):
	case bf == nil:
		d.add(path, "format-added", fmt.Sprintf("must now be a %s", brief(hf)))
	case hf == nil:
		d.add(path, "format-removed", fmt.Sprintf("no longer must be a %s", brief(bf)))
	default:
		d.add(path, "format-changed", fmt.Sprintf("%s -> %s", brief(bf), brief(hf)))
	}
}

// multipleOf loosens when the old divisor is a multiple of the new one (2 -> 1: every
// value that passed still passes); any other change, 2 -> 4 or 2 -> 3, can refuse a value
// that passed, so it tightens.
func (d *differ) multipleOf(path string, bm, hm map[string]any) {
	coord := path + "{multipleOf}"
	bv, bok := num(bm["multipleOf"])
	hv, hok := num(hm["multipleOf"])
	detail := fmt.Sprintf("%s -> %s", brief(bm["multipleOf"]), brief(hm["multipleOf"]))
	switch {
	case !bok && !hok:
		if !reflect.DeepEqual(bm["multipleOf"], hm["multipleOf"]) {
			d.add(coord, "keyword-changed", detail)
		}
	case bok && hok && bv == hv:
	case !hok:
		d.add(coord, "bound-loosened", detail)
	case bok && hv != 0 && isMultiple(bv, hv):
		d.add(coord, "bound-loosened", detail)
	default:
		d.add(coord, "bound-tightened", detail)
	}
}

func isMultiple(a, b float64) bool {
	r := a / b
	return r == math.Trunc(r)
}

func (d *differ) uniqueItems(path string, bm, hm map[string]any) {
	b, _ := bm["uniqueItems"].(bool)
	h, _ := hm["uniqueItems"].(bool)
	switch {
	case !b && h:
		d.add(path+"{uniqueItems}", "bound-tightened", "array items must now be unique")
	case b && !h:
		d.add(path+"{uniqueItems}", "bound-loosened", "array items may now repeat")
	}
}

// composition compares allOf/anyOf/oneOf, if/then/else, not and the dependent keywords.
//
// allOf entries are constraints (one more narrows), anyOf entries alternatives (one
// fewer narrows), and oneOf can narrow either way (a new branch can make a value match
// two). Entries identical on both sides are set aside; the rest are paired in order when
// as many changed on each side, and compared like any subschema, so a narrowing inside
// one is found where it is. Entries that cannot be paired are a narrowing or a widening
// by the rule above.
//
// `if` and `not` invert: a narrower `if` applies its `then` less often, a narrower `not`
// refuses more. Neither direction can be read off the subschema, so any change to them
// is composition-changed, breaking. So is any change to the dependent keywords.
func (d *differ) composition(path string, bm, hm map[string]any, bSeen, hSeen map[string]bool) {
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		bv, bHas := bm[k]
		hv, hHas := hm[k]
		if !bHas && !hHas {
			continue
		}
		ba, _ := bv.([]any)
		ha, _ := hv.([]any)
		var ub, uh []int
		used := make([]bool, len(ha))
		for i, b := range ba {
			match := false
			for j, h := range ha {
				if !used[j] && reflect.DeepEqual(b, h) {
					used[j], match = true, true
					break
				}
			}
			if !match {
				ub = append(ub, i)
			}
		}
		for j := range ha {
			if !used[j] {
				uh = append(uh, j)
			}
		}
		if len(ub) == len(uh) {
			for n := range ub {
				d.node(fmt.Sprintf("%s(%s[%d])", path, k, uh[n]), ba[ub[n]], ha[uh[n]], bSeen, hSeen)
			}
			continue
		}
		var narrowed bool
		switch k {
		case "allOf":
			narrowed = len(uh) > len(ub)
		case "anyOf":
			// a new anyOf where there was none is a new constraint; dropping it is not
			narrowed = !bHas || (hHas && len(uh) < len(ub))
		default:
			narrowed = true
		}
		detail := fmt.Sprintf("%d entries changed or removed, %d changed or added; they cannot be paired", len(ub), len(uh))
		if narrowed {
			d.add(path+"("+k+")", "composition-narrowed", detail)
		} else {
			d.add(path+"("+k+")", "composition-widened", detail)
		}
	}
	for _, k := range []string{"then", "else"} {
		bv, bHas := bm[k]
		hv, hHas := hm[k]
		switch {
		case !bHas && !hHas:
		case !bHas:
			d.add(path+"("+k+")", "composition-narrowed", k+" added")
		case !hHas:
			d.add(path+"("+k+")", "composition-widened", k+" removed")
		default:
			d.node(path+"("+k+")", bv, hv, bSeen, hSeen)
		}
	}
	for _, k := range []string{"if", "not", "dependentRequired", "dependentSchemas", "dependencies"} {
		if !reflect.DeepEqual(bm[k], hm[k]) {
			d.add(path+"("+k+")", "composition-changed", k+" differs; the tool cannot tell whether that narrows, so it is breaking until declared")
		}
	}
}

// modelled lists every keyword the differ compares by meaning; annotations carry no
// validation.
var modelled = map[string]bool{
	"type": true, "enum": true, "const": true, "pattern": true, "format": true, "default": true,
	"minimum": true, "exclusiveMinimum": true, "minLength": true, "minItems": true, "minProperties": true,
	"maximum": true, "exclusiveMaximum": true, "maxLength": true, "maxItems": true, "maxProperties": true,
	"multipleOf": true, "uniqueItems": true,
	"allOf": true, "anyOf": true, "oneOf": true, "not": true, "if": true, "then": true, "else": true,
	"dependentRequired": true, "dependentSchemas": true, "dependencies": true,
	"properties": true, "patternProperties": true, "required": true, "additionalProperties": true,
	"items": true, "prefixItems": true, "$ref": true,
}

var annotations = map[string]bool{
	"title": true, "description": true, "examples": true, "example": true, "$comment": true,
	"deprecated": true, "readOnly": true, "writeOnly": true, "$schema": true, "$anchor": true,
	"$defs": true, "definitions": true,
}

// otherKeywords fails conservatively on a change to any keyword the differ does not
// model (contains, propertyNames, unevaluatedProperties, an OpenAPI nullable, an
// x-kubernetes-validations rule, a changed $id, ...): ignoring it would pass whatever it
// narrows.
func (d *differ) otherKeywords(path string, bm, hm map[string]any) {
	keys := map[string]bool{}
	for k := range bm {
		keys[k] = true
	}
	for k := range hm {
		keys[k] = true
	}
	for _, k := range sortedBoolKeys(keys) {
		if modelled[k] || annotations[k] {
			continue
		}
		if !reflect.DeepEqual(bm[k], hm[k]) {
			d.add(path+"{"+k+"}", "keyword-changed", fmt.Sprintf("%s -> %s; the tool does not model %s, so it is breaking until declared", brief(bm[k]), brief(hm[k]), k))
		}
	}
}

func (d *differ) pattern(path string, bm, hm map[string]any) {
	bp, hp := str(bm["pattern"]), str(hm["pattern"])
	switch {
	case bp == hp:
	case bp == "":
		d.add(path, "pattern-added", fmt.Sprintf("must now match %q", hp))
	case hp == "":
		d.add(path, "pattern-removed", fmt.Sprintf("no longer must match %q", bp))
	default:
		d.add(path, "pattern-changed", fmt.Sprintf("%q -> %q; whether it narrows depends on the expressions", bp, hp))
	}
}

func (d *differ) properties(path string, bm, hm map[string]any, bSeen, hSeen map[string]bool) {
	for _, key := range []string{"properties", "patternProperties"} {
		bp, _ := bm[key].(map[string]any)
		hp, _ := hm[key].(map[string]any)
		for _, name := range sortedKeys(bp) {
			coord := path + "." + coordName(name)
			if key == "patternProperties" {
				coord = path + ".~" + coordName(name)
			}
			hv, ok := hp[name]
			if !ok {
				d.add(coord, "property-removed", fmt.Sprintf("%s no longer has %q", pathNoun(path), name))
				continue
			}
			d.node(coord, bp[name], hv, bSeen, hSeen)
		}
		for _, name := range sortedKeys(hp) {
			if _, ok := bp[name]; !ok {
				coord := path + "." + coordName(name)
				if key == "patternProperties" {
					coord = path + ".~" + coordName(name)
				}
				d.add(coord, "property-added", "")
			}
		}
	}
}

func pathNoun(p string) string {
	if p == "$" {
		return "the document"
	}
	return p
}

func coordName(s string) string {
	return strings.NewReplacer(" ", "%20", "\t", "%09").Replace(s)
}

func (d *differ) required(path string, bm, hm map[string]any) {
	br, hr := strSet(bm["required"]), strSet(hm["required"])
	for _, n := range sortedBoolKeys(hr) {
		if !br[n] {
			d.add(path+"."+coordName(n), "required-added", fmt.Sprintf("%q must now be present", n))
		}
	}
	for _, n := range sortedBoolKeys(br) {
		if !hr[n] {
			d.add(path+"."+coordName(n), "required-removed", fmt.Sprintf("%q may now be absent", n))
		}
	}
}

func (d *differ) additional(path string, bm, hm map[string]any, bSeen, hSeen map[string]bool) {
	b, h := additionalOf(bm), additionalOf(hm)
	coord := path + "{*}"
	bb, bIsBool := b.(bool)
	hb, hIsBool := h.(bool)
	switch {
	case bIsBool && hIsBool:
		if bb && !hb {
			d.add(path, "additional-properties-closed", "unlisted properties were accepted and are now refused")
		} else if !bb && hb {
			d.add(path, "additional-properties-opened", "unlisted properties are now accepted")
		}
	case bIsBool && !hIsBool:
		if bb {
			d.add(path, "additional-properties-narrowed", "unlisted properties were unrestricted and must now match a schema")
		} else {
			d.add(path, "additional-properties-opened", "unlisted properties are now accepted if they match a schema")
		}
	case !bIsBool && hIsBool:
		if hb {
			d.add(path, "additional-properties-opened", "unlisted properties are no longer constrained")
		} else {
			d.add(path, "additional-properties-closed", "unlisted properties were accepted and are now refused")
		}
	default:
		d.node(coord, b, h, bSeen, hSeen)
	}
}

// additionalOf is the additionalProperties of a schema object, true when absent.
func additionalOf(m map[string]any) any {
	v, ok := m["additionalProperties"]
	if !ok {
		return true
	}
	return v
}

func (d *differ) items(path string, bm, hm map[string]any, bSeen, hSeen map[string]bool) {
	bi, bok := bm["items"]
	hi, hok := hm["items"]
	coord := path + "[]"
	switch {
	case bok && hok:
		d.node(coord, bi, hi, bSeen, hSeen)
	case !bok && hok:
		d.add(coord, "items-constrained", "array items were unrestricted and must now match a schema")
	case bok && !hok:
		d.add(coord, "items-unconstrained", "array items are no longer constrained")
	}
	bp, _ := bm["prefixItems"].([]any)
	hp, _ := hm["prefixItems"].([]any)
	for i := 0; i < len(bp) && i < len(hp); i++ {
		d.node(fmt.Sprintf("%s[%d]", path, i), bp[i], hp[i], bSeen, hSeen)
	}
	if len(bp) != len(hp) {
		d.add(path+"(prefixItems)", "composition-changed", fmt.Sprintf("prefixItems has %d entries, was %d", len(hp), len(bp)))
	}
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func strSet(v any) map[string]bool {
	m := map[string]bool{}
	if a, ok := v.([]any); ok {
		for _, e := range a {
			if s, ok := e.(string); ok {
				m[s] = true
			}
		}
	}
	return m
}

func brief(v any) string {
	if v == nil {
		return "(none)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(b) > 80 {
		return string(b[:77]) + "..."
	}
	return string(b)
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedBoolKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
