// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The kinds, as the generated code spells them.
const (
	kindClass      = "CLASS"
	kindPredicate  = "PREDICATE"
	kindObservable = "OBSERVABLE_PROPERTY"
)

// term is one vocabulary term on its way to a generated table.
type term struct {
	IRI        string
	Label      string
	Kind       string
	Parents    []string
	Inverse    string
	Deprecated bool
	ReplacedBy string
}

// tombstone declares a term the pinned upstream file's own change log says is
// deprecated but no longer declares. It stays in the table as a deprecated
// entry so that a binding attempt gets "deprecated, use X" instead of the less
// helpful "unknown". It is an error for the term to be absent from the
// registry's other direction too: if upstream ever declares it again, the
// generator marks that declaration deprecated rather than overwriting it.
type tombstone struct {
	IRI, Kind, Label, ReplacedBy, Reason string
}

// owlRules is how a pack classifies the terms in a Turtle vocabulary. Every
// rule here is written down with its reason, because the kind of a term is a
// modelling decision, not a fact the upstream file states.
type owlRules struct {
	// namespace is the pack's namespace: only subjects inside it are emitted.
	namespace string
	// quantityType, when set, is an rdf:type whose INSTANCES are observable
	// properties. Reason: Brick models a quantity (Temperature, Frequency) as an
	// individual of brick:Quantity, not as a class.
	quantityType string
	// propertyClass, when set, is a class whose subclasses AND instances are
	// observable properties. Reason: SAREF models "a thing that can be
	// observed" as saref:Property, and its extensions list the concrete
	// properties as individuals of it. The class itself stays a CLASS.
	propertyClass string
	// replacedByPredicate names the predicate that points a deprecated term at
	// its successor (brick:isReplacedBy); empty when the vocabulary has none.
	replacedByPredicate string
	tombstones          []tombstone
}

var (
	owlClassTypes = []string{owlNS + "Class", rdfsNS + "Class"}
	// Reason: an object property links two things and a datatype property
	// attaches a value to one; both are the "predicate" a relationship type or
	// attribute binding names. Annotation properties are metadata ABOUT terms
	// and are deliberately left out.
	predicateTypes = []string{
		rdfNS + "Property",
		owlNS + "ObjectProperty", owlNS + "DatatypeProperty",
		owlNS + "FunctionalProperty", owlNS + "InverseFunctionalProperty",
		owlNS + "TransitiveProperty", owlNS + "SymmetricProperty",
		owlNS + "AsymmetricProperty", owlNS + "ReflexiveProperty", owlNS + "IrreflexiveProperty",
	}
)

func anyType(types map[string]bool, want []string) bool {
	for _, w := range want {
		if types[w] {
			return true
		}
	}
	return false
}

// buildOWL classifies every subject inside r.namespace. Subjects that are none
// of class, predicate or observable property -- Brick's substance individuals,
// ontology headers -- are not terms a binding can name and are returned in
// skipped so the caller can report what it left out.
func buildOWL(g *graph, r owlRules) (terms []term, skipped []string, err error) {
	byIRI := map[string]*term{}
	for _, s := range g.subjectsIn(r.namespace) {
		types := g.types(s)
		var kind string
		switch {
		case r.quantityType != "" && types[r.quantityType]:
			kind = kindObservable
		case anyType(types, owlClassTypes):
			kind = kindClass
			if r.propertyClass != "" && s != r.propertyClass && g.subClassOfStar(s, r.propertyClass) {
				kind = kindObservable
			}
		case anyType(types, predicateTypes):
			kind = kindPredicate
		default:
			if r.propertyClass != "" {
				for t := range types {
					if t == r.propertyClass || g.subClassOfStar(t, r.propertyClass) {
						kind = kindObservable
						break
					}
				}
			}
		}
		if kind == "" {
			skipped = append(skipped, s)
			continue
		}

		t := &term{IRI: s, Kind: kind}
		t.Label = g.label(s, rdfsNS+"label", "http://www.w3.org/2004/02/skos/core#prefLabel")
		if t.Label == "" {
			t.Label = s[len(r.namespace):]
		}
		// Parents: a class's superclasses, a property's superproperties, an
		// individual's types. Named IRIs only -- restrictions are blank nodes
		// and carry no hierarchy a binding could use.
		var parents []string
		switch {
		case anyType(types, owlClassTypes):
			parents = g.iris(s, rdfsNS+"subClassOf")
		case anyType(types, predicateTypes):
			parents = g.iris(s, rdfsNS+"subPropertyOf")
		default:
			for _, p := range g.iris(s, rdfNS+"type") {
				if !strings.HasPrefix(p, owlNS) {
					parents = append(parents, p)
				}
			}
		}
		for _, p := range parents {
			if p != s {
				t.Parents = append(t.Parents, p)
			}
		}
		t.Deprecated = g.flag(s, owlNS+"deprecated")
		if t.Deprecated && r.replacedByPredicate != "" {
			if rb := g.iris(s, r.replacedByPredicate); len(rb) > 0 {
				t.ReplacedBy = rb[0]
			}
		}
		byIRI[s] = t
	}

	// Inverses are recorded on BOTH terms whichever side the vocabulary
	// declared. A term's own declaration wins over a reverse one; among several
	// the first in sort order, so the output does not depend on file order.
	for _, s := range sortedTermKeys(byIRI) {
		if byIRI[s].Kind != kindPredicate {
			continue
		}
		if inv := g.iris(s, owlNS+"inverseOf"); len(inv) > 0 && byIRI[s].Inverse == "" {
			byIRI[s].Inverse = inv[0]
		}
	}
	for _, s := range sortedTermKeys(byIRI) {
		inv := byIRI[s].Inverse
		if inv == "" {
			continue
		}
		if other, ok := byIRI[inv]; ok && other.Inverse == "" {
			other.Inverse = s
		}
	}

	for _, ts := range r.tombstones {
		if !strings.HasPrefix(ts.IRI, r.namespace) {
			return nil, nil, fmt.Errorf("tombstone %s is outside namespace %s", ts.IRI, r.namespace)
		}
		if ts.Reason == "" {
			return nil, nil, fmt.Errorf("tombstone %s has no reason", ts.IRI)
		}
		if cur, ok := byIRI[ts.IRI]; ok {
			cur.Deprecated = true
			if cur.ReplacedBy == "" {
				cur.ReplacedBy = ts.ReplacedBy
			}
			continue
		}
		byIRI[ts.IRI] = &term{IRI: ts.IRI, Kind: ts.Kind, Label: ts.Label, Deprecated: true, ReplacedBy: ts.ReplacedBy}
	}

	for _, k := range sortedTermKeys(byIRI) {
		terms = append(terms, *byIRI[k])
	}
	return terms, skipped, nil
}

func sortedTermKeys(m map[string]*term) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// contextTerms reads a JSON-LD context document into key -> IRI. Both forms a
// term may take are understood ("key": "iri" and "key": {"@id": "iri", ...});
// keyword aliases ("id": "@id") are dropped. The document may be a bare context
// or {"@context": ...} holding an object or an array of objects and strings.
func contextTerms(data []byte) (map[string]string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("context is not a JSON object: %w", err)
	}
	raw, ok := doc["@context"]
	if !ok {
		return nil, fmt.Errorf("document has no @context")
	}
	var parts []json.RawMessage
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, err
		}
	} else {
		parts = []json.RawMessage{raw}
	}
	out := map[string]string{}
	for _, p := range parts {
		if len(p) == 0 || p[0] != '{' {
			continue // a remote context reference; nothing to read
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(p, &obj); err != nil {
			return nil, err
		}
		for k, v := range obj {
			if strings.HasPrefix(k, "@") {
				continue
			}
			var s string
			if err := json.Unmarshal(v, &s); err == nil {
				if !strings.HasPrefix(s, "@") {
					out[k] = s
				}
				continue
			}
			var def struct {
				ID string `json:"@id"`
			}
			if err := json.Unmarshal(v, &def); err == nil && def.ID != "" && !strings.HasPrefix(def.ID, "@") {
				out[k] = def.ID
			}
		}
	}
	return out, nil
}

// contextRules is how a pack classifies the terms in a JSON-LD context.
type contextRules struct {
	namespace string
	// classify returns the term's kind from its local name, "" to leave the term
	// out, or an error for a name it cannot classify with confidence.
	classify func(local string) (string, error)
	// requireClasses are local names that must come out as CLASS; one that does
	// not is an error, so a model upstream renamed or dropped stops the run.
	requireClasses []string
}

// buildContext emits every context term whose IRI is inside r.namespace,
// classified by r.classify. Terms outside the namespace (the shared
// smartdatamodels.org attributes, geojson) belong to no pack. Two keys for one
// IRI collapse to one term.
func buildContext(data []byte, r contextRules) ([]term, error) {
	m, err := contextTerms(data)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []term
	var iris []string
	for _, iri := range m {
		iris = append(iris, iri)
	}
	sort.Strings(iris)
	classes := map[string]bool{}
	for _, iri := range iris {
		if seen[iri] || !strings.HasPrefix(iri, r.namespace) || len(iri) == len(r.namespace) {
			continue
		}
		seen[iri] = true
		local := iri[len(r.namespace):]
		if strings.ContainsAny(local, "/#?") {
			continue
		}
		kind, err := r.classify(local)
		if err != nil {
			return nil, err
		}
		if kind == "" {
			continue
		}
		if kind == kindClass {
			classes[local] = true
		}
		out = append(out, term{IRI: iri, Label: local, Kind: kind})
	}
	for _, c := range r.requireClasses {
		if !classes[c] {
			return nil, fmt.Errorf("%s%s was expected to be a CLASS and is not in the context", r.namespace, c)
		}
	}
	return out, nil
}

func startsUpper(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

// sdmClassifier builds the Smart Data Models classifier for one domain.
//
// The project's contribution guidelines name entity TYPES in UpperCamelCase and
// attributes in lowerCamelCase, with relationship attributes named ref<Type>.
// That convention is not reliable on its own -- the Environment context lists
// attributes such as LAeq, CO2eq and RFID, and Streetlighting lists RFId -- so
// the types are NOT inferred from the case of a name. A type is a CLASS only if
// it is one of models, the model directories of the pinned repository commit;
// an upper-case-initial name that is neither a model nor a known upper-case
// attribute stops the run, so a context bump that adds either is read by a
// person instead of being guessed at.
//
// Reason for each kind: a model is a CLASS; a ref<Type> attribute is a link
// between two entities, a PREDICATE; every other attribute is a measured or
// declared property of the entity, an OBSERVABLE_PROPERTY.
func sdmClassifier(models, upperAttributes []string) func(string) (string, error) {
	isModel := map[string]bool{}
	for _, m := range models {
		isModel[m] = true
	}
	isUpperAttr := map[string]bool{}
	for _, a := range upperAttributes {
		isUpperAttr[a] = true
	}
	return func(local string) (string, error) {
		switch {
		case isModel[local]:
			return kindClass, nil
		case isUpperAttr[local]:
			return kindObservable, nil
		case startsUpper(local):
			return "", fmt.Errorf("%q starts with an upper-case letter but is neither a known model nor a known upper-case attribute; classify it in vocabgen's rules", local)
		case strings.HasPrefix(local, "ref") && len(local) > 3 && startsUpper(local[3:]):
			return kindPredicate, nil
		}
		return kindObservable, nil
	}
}

// classifyNGSILD: the core context names types in UpperCamelCase (Property,
// Relationship, Subscription) and attributes in lowerCamelCase (observedAt,
// datasetId). A type is a CLASS and an attribute a PREDICATE; the core context
// has no observable properties. DateTime, Date and Time are literal datatypes,
// not types of things, and are left out.
func classifyNGSILD(local string) (string, error) {
	switch local {
	case "DateTime", "Date", "Time":
		return "", nil
	}
	if startsUpper(local) {
		return kindClass, nil
	}
	return kindPredicate, nil
}

// unit is one UCUM/UN-CEFACT row.
type unit struct {
	UCUM, CommonCode, Label, IRI string
}

const qudtNS = "http://qudt.org/schema/qudt/"

// buildUnits reads QUDT's unit vocabulary into one row per UCUM code.
//
// QUDT carries a UCUM code and a UN/CEFACT common code on each unit, which is
// exactly the table the platform needs, under a licence it can ship. A unit may
// list several UCUM spellings; each spelling is a row. Where two units claim the
// same UCUM code the better claim wins: not deprecated, then having a common
// code, then the lexicographically first IRI. dropped counts the losers.
func buildUnits(g *graph) (rows []unit, dropped int) {
	best := map[string]unit{}
	for s := range g.by {
		if !strings.HasPrefix(s, "http://qudt.org/vocab/unit/") {
			continue
		}
		if g.flag(s, qudtNS+"deprecated") {
			continue
		}
		var common string
		var commons []string
		for _, o := range g.objs(s, qudtNS+"uneceCommonCode") {
			if o.Kind == nodeLiteral && strings.TrimSpace(o.Value) != "" {
				commons = append(commons, strings.TrimSpace(o.Value))
			}
		}
		sort.Strings(commons)
		if len(commons) > 0 {
			common = commons[0]
		}
		label := g.label(s, rdfsNS+"label")
		for _, o := range g.objs(s, qudtNS+"ucumCode") {
			code := strings.TrimSpace(o.Value)
			if o.Kind != nodeLiteral || code == "" {
				continue
			}
			cand := unit{UCUM: code, CommonCode: common, Label: label, IRI: s}
			cur, have := best[code]
			if !have {
				best[code] = cand
				continue
			}
			dropped++
			curHas := cur.CommonCode != ""
			candHas := cand.CommonCode != ""
			if (candHas && !curHas) || (candHas == curHas && cand.IRI < cur.IRI) {
				best[code] = cand
			}
		}
	}
	for _, u := range best {
		rows = append(rows, u)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UCUM < rows[j].UCUM })
	return rows, dropped
}
