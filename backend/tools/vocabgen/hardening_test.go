// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestTurtleRelativeIRIsResolveByRFC3986(t *testing.T) {
	g := mustParse(t, `
@base <https://example.org/vocab/file.ttl> .
<Term> <https://example.org/p> <../Parent> .
@base <sub/> .
<Other> <https://example.org/p> <#frag> .
<https://example.org/vocab/é> <https://example.org/p> <https://example.org/vocab/é> .
`)
	if got := g.iris("https://example.org/vocab/Term", "https://example.org/p"); len(got) != 1 || got[0] != "https://example.org/Parent" {
		t.Errorf("Term -> %v; want the parent resolved to https://example.org/Parent (not file.ttl../Parent)", got)
	}
	// A relative @base resolves against the previous one.
	if got := g.iris("https://example.org/vocab/sub/Other", "https://example.org/p"); len(got) != 1 || got[0] != "https://example.org/vocab/sub/#frag" {
		t.Errorf("Other -> %v", got)
	}
	// An absolute IRI is taken exactly as written, not re-encoded.
	if got := g.iris("https://example.org/vocab/é", "https://example.org/p"); len(got) != 1 || got[0] != "https://example.org/vocab/é" {
		t.Errorf("absolute IRI was altered: %v", got)
	}
	if _, err := parseTurtle(`<a> <https://e/p> <https://e/o> .`); err == nil {
		t.Error("a relative IRI with no @base in force must be an error, not a guess")
	}
}

func TestTurtleRejectsInvalidScalarEscapes(t *testing.T) {
	for name, doc := range map[string]string{
		"surrogate in an IRI":       `<https://e/\uD800> <https://e/p> <https://e/o> .`,
		"beyond U+10FFFF in an IRI": `<https://e/\U00110000> <https://e/p> <https://e/o> .`,
		"surrogate in a string":     `<https://e/s> <https://e/p> "\uDFFF" .`,
	} {
		if _, err := parseTurtle(doc); err == nil {
			t.Errorf("%s: parsed, and would have been rewritten to U+FFFD", name)
		}
	}
	g := mustParse(t, `<https://e/s> <https://e/p> "caf\u00e9" .`)
	if got := g.label("https://e/s", "https://e/p"); got != "café" {
		t.Errorf("a valid escape: %q", got)
	}
}

func TestBooleanFlagReadsTheDatatype(t *testing.T) {
	const prelude = `
@prefix ex: <https://e/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .
`
	for lit, want := range map[string]bool{
		`true`:                 true,
		`"true"^^xsd:boolean`:  true,
		`"1"^^xsd:boolean`:     true, // the other true lexical form
		`false`:                false,
		`"false"^^xsd:boolean`: false,
		`"0"^^xsd:boolean`:     false,
		`"true"`:               false, // a plain string is not a boolean
		`"true"@en`:            false, // nor is a language-tagged one
		`"1"`:                  false,
		`"true"^^xsd:string`:   false,
		`"yes"^^xsd:boolean`:   false,
		`"TRUE"^^xsd:boolean`:  false,
	} {
		g := mustParse(t, prelude+"ex:Old owl:deprecated "+lit+" .")
		if got := g.flag("https://e/Old", owlNS+"deprecated"); got != want {
			t.Errorf("owl:deprecated %s: flag = %v, want %v", lit, got, want)
		}
	}
}
