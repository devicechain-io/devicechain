// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *graph {
	t.Helper()
	ts, err := parseTurtle(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g := newGraph()
	g.add(ts)
	return g
}

func TestTurtleGrammar(t *testing.T) {
	const doc = `
@base <https://example.org/base/> .
@prefix ex: <https://example.org/ns#> .
PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
# a comment
ex:a a ex:Thing, ex:Other ;
    ex:label "plain", "tagged"@en, 'single', """long
text with "quotes" """ ;
    ex:n 42, -1.5, 1e3 ;
    ex:flag true ;
    ex:typed "7"^^xsd:integer ;
    ex:list ( ex:x ex:y ) ;
    ex:bn [ ex:inner "v" ; ] ;
    ex:dotted ex:name.with.dots ;
    ex:rel <rel> .
[] ex:orphan "o" .
ex:b ex:p ex:c.
`
	g := mustParse(t, doc)
	a := "https://example.org/ns#a"
	if got := g.iris(a, rdfNS+"type"); len(got) != 2 {
		t.Fatalf("types = %v", got)
	}
	var labels []string
	for _, o := range g.objs(a, "https://example.org/ns#label") {
		labels = append(labels, o.Value)
	}
	if len(labels) != 4 || labels[3] != "long\ntext with \"quotes\" " {
		t.Fatalf("labels = %q", labels)
	}
	if got := g.iris(a, "https://example.org/ns#dotted"); !reflect.DeepEqual(got, []string{"https://example.org/ns#name.with.dots"}) {
		t.Fatalf("dotted name = %v", got)
	}
	if got := g.iris(a, "https://example.org/ns#rel"); !reflect.DeepEqual(got, []string{"https://example.org/base/rel"}) {
		t.Fatalf("relative IRI = %v", got)
	}
	if got := g.iris("https://example.org/ns#b", "https://example.org/ns#p"); !reflect.DeepEqual(got, []string{"https://example.org/ns#c"}) {
		t.Fatalf("terminating dot swallowed or kept: %v", got)
	}
	if !g.flag(a, "https://example.org/ns#flag") {
		t.Fatal("boolean not read")
	}
}

func TestTurtleRejectsWhatItDoesNotUnderstand(t *testing.T) {
	for name, doc := range map[string]string{
		"undeclared prefix":   `ex:a ex:b ex:c .`,
		"unterminated string": `@prefix ex: <https://e/> . ex:a ex:b "oops .`,
		"missing dot":         `@prefix ex: <https://e/> . ex:a ex:b ex:c`,
		"bad escape":          `@prefix ex: <https://e/> . ex:a ex:b "\q" .`,
	} {
		if _, err := parseTurtle(doc); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

const brickFixture = `
@prefix brick: <https://brickschema.org/schema/Brick#> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix sh: <http://www.w3.org/ns/shacl#> .
@prefix rec: <https://w3id.org/rec#> .
brick:Equipment a owl:Class ; rdfs:label "Equipment"@en .
brick:Lighting a owl:Class ; rdfs:subClassOf brick:Equipment .
brick:Luminaire a owl:Class, sh:NodeShape ; rdfs:label "Luminaire"@en ; rdfs:subClassOf brick:Lighting, rec:Thing, [ a owl:Restriction ] .
brick:Quantity a owl:Class .
brick:Temperature a brick:Quantity ; rdfs:label "Temperature"@en .
brick:feeds a owl:ObjectProperty ; owl:inverseOf brick:isFedBy ; rdfs:label "Feeds"@en .
brick:isFedBy a owl:ObjectProperty .
brick:Old a owl:Class ; owl:deprecated true ; brick:isReplacedBy brick:Luminaire .
brick:Air a brick:Substance .
brick:Cycle1 a owl:Class ; rdfs:subClassOf brick:Cycle2 .
brick:Cycle2 a owl:Class ; rdfs:subClassOf brick:Cycle1 .
`

func TestBuildOWLClassification(t *testing.T) {
	g := mustParse(t, brickFixture)
	terms, skipped, err := buildOWL(g, owlRules{
		namespace: brickNS, quantityType: brickNS + "Quantity", replacedByPredicate: brickNS + "isReplacedBy",
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]term{}
	for _, tm := range terms {
		by[tm.IRI] = tm
	}
	want := map[string]string{
		"Luminaire": kindClass, "Equipment": kindClass, "Quantity": kindClass,
		"Temperature": kindObservable, "feeds": kindPredicate, "isFedBy": kindPredicate,
	}
	for local, kind := range want {
		if got := by[brickNS+local].Kind; got != kind {
			t.Errorf("%s kind = %q, want %q", local, got, kind)
		}
	}
	if _, ok := by[brickNS+"Air"]; ok {
		t.Error("a substance individual was emitted as a term")
	}
	if len(skipped) != 1 || skipped[0] != brickNS+"Air" {
		t.Errorf("skipped = %v", skipped)
	}
	lum := by[brickNS+"Luminaire"]
	if !reflect.DeepEqual(lum.Parents, []string{brickNS + "Lighting", "https://w3id.org/rec#Thing"}) {
		t.Errorf("Luminaire parents = %v (restrictions must be dropped, named foreign parents kept)", lum.Parents)
	}
	if by[brickNS+"isFedBy"].Inverse != brickNS+"feeds" || by[brickNS+"feeds"].Inverse != brickNS+"isFedBy" {
		t.Errorf("inverse not recorded on both sides: %+v / %+v", by[brickNS+"feeds"], by[brickNS+"isFedBy"])
	}
	old := by[brickNS+"Old"]
	if !old.Deprecated || old.ReplacedBy != brickNS+"Luminaire" {
		t.Errorf("deprecation = %+v", old)
	}
	if by[brickNS+"Temperature"].Parents[0] != brickNS+"Quantity" {
		t.Errorf("quantity individual should list its type as parent: %v", by[brickNS+"Temperature"].Parents)
	}
}

func TestBuildOWLPropertyClassRules(t *testing.T) {
	const doc = `
@prefix saref: <https://saref.etsi.org/core/> .
@prefix s4: <https://saref.etsi.org/saref4bldg/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
saref:Property a owl:Class .
saref:Device a owl:Class .
s4:Temperature a owl:Class ; rdfs:subClassOf saref:Property .
s4:apparentPowerMax a saref:Property .
s4:Sub a s4:Temperature .
s4:Widget a owl:Class ; rdfs:subClassOf saref:Device .
s4:hasX a owl:DatatypeProperty .
`
	g := mustParse(t, doc)
	terms, _, err := buildOWL(g, owlRules{namespace: "https://saref.etsi.org/saref4bldg/", propertyClass: sarefNS + "Property"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tm := range terms {
		got[tm.IRI[len("https://saref.etsi.org/saref4bldg/"):]] = tm.Kind
	}
	want := map[string]string{
		"Temperature": kindObservable, "apparentPowerMax": kindObservable, "Sub": kindObservable,
		"Widget": kindClass, "hasX": kindPredicate,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	// The property class itself, in its own namespace, stays a class.
	core, _, _ := buildOWL(g, owlRules{namespace: sarefNS, propertyClass: sarefNS + "Property"})
	for _, tm := range core {
		if tm.IRI == sarefNS+"Property" && tm.Kind != kindClass {
			t.Errorf("saref:Property itself is %s, want CLASS", tm.Kind)
		}
	}
}

func TestTombstones(t *testing.T) {
	g := mustParse(t, `
@prefix saref: <https://saref.etsi.org/core/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
saref:Observation a owl:Class .
saref:Present a owl:Class .
`)
	rules := owlRules{namespace: sarefNS, tombstones: []tombstone{
		{IRI: sarefNS + "Measurement", Kind: kindClass, Label: "Measurement", ReplacedBy: sarefNS + "Observation", Reason: "r"},
		{IRI: sarefNS + "Present", Kind: kindClass, ReplacedBy: sarefNS + "Observation", Reason: "r"},
	}}
	terms, _, err := buildOWL(g, rules)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]term{}
	for _, tm := range terms {
		by[tm.IRI] = tm
	}
	if m := by[sarefNS+"Measurement"]; !m.Deprecated || m.ReplacedBy != sarefNS+"Observation" || m.Kind != kindClass {
		t.Errorf("absent tombstone not added as deprecated: %+v", m)
	}
	if p := by[sarefNS+"Present"]; !p.Deprecated {
		t.Errorf("declared tombstone must be marked deprecated, not overwritten: %+v", p)
	}
	if by[sarefNS+"Observation"].Deprecated {
		t.Error("an unrelated term was deprecated")
	}
	rules.tombstones[0].Reason = ""
	if _, _, err := buildOWL(g, rules); err == nil {
		t.Error("a tombstone with no reason must be refused")
	}
}

func TestContextClassification(t *testing.T) {
	const ctx = `{"@context": {
	  "id": "@id", "type": "@type",
	  "Streetlight": "https://smartdatamodels.org/dataModel.Streetlighting/Streetlight",
	  "RFId": "https://smartdatamodels.org/dataModel.Streetlighting/RFId",
	  "powerState": {"@id": "https://smartdatamodels.org/dataModel.Streetlighting/powerState", "@type": "@vocab"},
	  "refStreetlightGroup": "https://smartdatamodels.org/dataModel.Streetlighting/refStreetlightGroup",
	  "address": "https://smartdatamodels.org/address",
	  "location": "https://uri.etsi.org/ngsi-ld/location",
	  "alias": "https://smartdatamodels.org/dataModel.Streetlighting/powerState",
	  "refund": "https://smartdatamodels.org/dataModel.Streetlighting/refund"
	}}`
	rules := contextRules{
		namespace:      sdmBase + "Streetlighting/",
		classify:       sdmClassifier([]string{"Streetlight"}, []string{"RFId"}),
		requireClasses: []string{"Streetlight"},
	}
	terms, err := buildContext([]byte(ctx), rules)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tm := range terms {
		got[tm.Label] = tm.Kind
	}
	want := map[string]string{
		"Streetlight": kindClass, "powerState": kindObservable,
		"refStreetlightGroup": kindPredicate,
		"RFId":                kindObservable, // upper-case initial, but an attribute: case does not decide
		"refund":              kindObservable, // "ref" not followed by an upper-case letter is just a word
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (shared-namespace terms and keyword aliases must be left out; duplicate keys collapse)", got, want)
	}

	// An upper-case name that is neither a model nor a known attribute is a
	// decision for a person, not a guess.
	_, err = buildContext([]byte(`{"@context": {"Mystery": "`+sdmBase+`Streetlighting/Mystery"}}`), rules)
	if err == nil || !strings.Contains(err.Error(), "Mystery") {
		t.Errorf("an unclassifiable upper-case name must stop the run, got %v", err)
	}
	// A model the context no longer carries stops the run too.
	rules.requireClasses = []string{"Streetlight", "StreetlightGone"}
	if _, err = buildContext([]byte(ctx), rules); err == nil || !strings.Contains(err.Error(), "StreetlightGone") {
		t.Errorf("a missing required model must stop the run, got %v", err)
	}

	if k, _ := classifyNGSILD("DateTime"); k != "" {
		t.Error("NGSI-LD datatypes are not types of things")
	}
	if k, _ := classifyNGSILD("Property"); k != kindClass {
		t.Error("NGSI-LD Property is a class")
	}
	if k, _ := classifyNGSILD("observedAt"); k != kindPredicate {
		t.Error("NGSI-LD observedAt is a predicate")
	}
}

func TestBuildUnits(t *testing.T) {
	g := mustParse(t, `
@prefix unit: <http://qudt.org/vocab/unit/> .
@prefix qudt: <http://qudt.org/schema/qudt/> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
unit:DEG_C qudt:ucumCode "Cel" ; qudt:uneceCommonCode "CEL" ; rdfs:label "Degree Celsius"@en .
unit:CEL_DUP qudt:ucumCode "Cel" ; rdfs:label "No code"@en .
unit:OLD qudt:ucumCode "old" ; qudt:deprecated true .
unit:KiloW qudt:ucumCode "kW", "KW" ; qudt:uneceCommonCode "KWT" ; rdfs:label "Kilowatt"@en .
`)
	rows, dropped := buildUnits(g)
	var codes []string
	for _, r := range rows {
		codes = append(codes, r.UCUM)
	}
	if !reflect.DeepEqual(codes, []string{"Cel", "KW", "kW"}) {
		t.Fatalf("codes = %v (deprecated units out, duplicates collapsed, sorted)", codes)
	}
	if rows[0].CommonCode != "CEL" || rows[0].IRI != "http://qudt.org/vocab/unit/DEG_C" {
		t.Errorf("the claim with a common code must win: %+v", rows[0])
	}
	if dropped != 1 {
		t.Errorf("dropped = %d", dropped)
	}
}

// fixtureInputs is a complete, tiny input set for generate().
func fixtureInputs(t *testing.T) map[string]source {
	t.Helper()
	mk := func(name, url, data string) source {
		sum := sha256.Sum256([]byte(data))
		return source{Name: name, URL: url, SHA256: hex.EncodeToString(sum[:]), Data: []byte(data)}
	}
	in := map[string]source{}
	add := func(s source) { in[s.Name] = s }
	add(mk(inBrick, "https://example.org/releases/download/v1.4.4/Brick.ttl", brickFixture))
	add(mk(inBrickLicence, "https://example.org/LICENSE", "Copyright (c) Brick fixture\nBSD text"))
	core := `
@prefix saref: <https://saref.etsi.org/core/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
saref:Property a owl:Class .
saref:Observation a owl:Class .
saref:hasValue a owl:DatatypeProperty .
`
	add(mk(inSarefCore, "https://example.org/core/v4.1.1/saref.ttl", core))
	add(mk(inSarefCity, "https://example.org/saref4city/v2.1.1/saref4city.ttl", `
@prefix s4city: <https://saref.etsi.org/saref4city/> .
@prefix owl: <http://www.w3.org/2002/07/owl#> .
s4city:District a owl:Class .
`))
	add(mk(inSarefBldg, "https://example.org/saref4bldg/v2.1.1/saref4bldg.ttl", `
@prefix s4bldg: <https://saref.etsi.org/saref4bldg/> .
@prefix saref: <https://saref.etsi.org/core/> .
s4bldg:apparentPowerMax a saref:Property .
`))
	add(mk(inNGSI, "https://example.org/ngsi.jsonld", `{"@context": {"Property": "https://uri.etsi.org/ngsi-ld/Property", "observedAt": "https://uri.etsi.org/ngsi-ld/observedAt"}}`))
	add(mk(inQUDT, "https://example.org/qudt/v3.5.3/units.ttl", `
@prefix unit: <http://qudt.org/vocab/unit/> .
@prefix qudt: <http://qudt.org/schema/qudt/> .
unit:DEG_C qudt:ucumCode "Cel" ; qudt:uneceCommonCode "CEL" .
`))
	commit := strings.Repeat("ab", 20)
	for _, d := range []struct{ domain, class, attr string }{
		{"Streetlighting", "Streetlight", "powerState"},
		{"Parking", "OnStreetParking", "availableSpotNumber"},
		{"Environment", "AirQualityObserved", "pm25"},
	} {
		ns := sdmBase + d.domain + "/"
		entries := []string{`"` + d.attr + `": "` + ns + d.attr + `"`}
		for _, m := range sdmModels[d.domain] {
			entries = append(entries, `"`+m+`": "`+ns+m+`"`)
		}
		for _, a := range sdmUpperAttributes[d.domain] {
			entries = append(entries, `"`+a+`": "`+ns+a+`"`)
		}
		add(mk(sdmContextInput(d.domain), "https://example.org/"+d.domain+"/"+commit+"/context.jsonld",
			`{"@context": {`+strings.Join(entries, ", ")+`}}`))
		add(mk(schemaPrefix+d.class, "https://example.org/"+d.class+"/schema.json",
			`{"$id": "https://smart-data-models.github.io/dataModel.`+d.domain+`/`+d.class+`/schema.json"}`))
	}
	return in
}

func TestGenerateIsReproducibleAndWellFormed(t *testing.T) {
	in := fixtureInputs(t)
	a, repA, err := generate(in)
	if err != nil {
		t.Fatal(err)
	}
	b, repB, err := generate(in)
	if err != nil {
		t.Fatal(err)
	}
	if repA != repB || len(a) != len(b) {
		t.Fatal("two runs differ in report or file set")
	}
	for name, content := range a {
		if !bytes.Equal(content, b[name]) {
			t.Errorf("%s differs between two runs on the same inputs", name)
		}
	}
	for _, want := range []string{
		"packs/brick_gen.go", "packs/saref_gen.go", "packs/s4city_gen.go", "packs/s4bldg_gen.go",
		"packs/sdm_streetlighting_gen.go", "packs/sdm_parking_gen.go", "packs/sdm_environment_gen.go",
		"packs/ngsi_ld_gen.go", "packs/unit_gen.go", "contexts_gen.go",
		"contexts/ngsi-ld-core-context.jsonld", "contexts/sdm-streetlighting.jsonld",
	} {
		if _, ok := a[want]; !ok {
			t.Errorf("missing output %s", want)
		}
	}
	brick := string(a["packs/brick_gen.go"])
	for _, want := range []string{
		"// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n",
		"// Code generated by backend/tools/vocabgen",
		"Copyright (c) Brick fixture", // the upstream licence notice rides along
		`"1.4.4"`,
		"semantic.KindObservableProperty",
		"Inverse: iri(",
		"Deprecated: true",
	} {
		if !strings.Contains(brick, want) {
			t.Errorf("brick_gen.go lacks %q", want)
		}
	}
	if !strings.Contains(string(a["packs/sdm_parking_gen.go"]), "git-abababababab") {
		t.Error("an SDM pack's version should be the pinned commit")
	}
	if !bytes.Equal(a["contexts/ngsi-ld-core-context.jsonld"], in[inNGSI].Data) {
		t.Error("a pinned context must be the upstream bytes, unchanged")
	}
}

func TestGenerateRefusesBadInputs(t *testing.T) {
	// A schema whose model the context classified as an attribute.
	in := fixtureInputs(t)
	in[schemaPrefix+"powerState"] = source{Name: schemaPrefix + "powerState",
		Data: []byte(`{"$id": "https://smart-data-models.github.io/dataModel.Streetlighting/powerState/schema.json"}`)}
	if _, _, err := generate(in); err == nil || !strings.Contains(err.Error(), "not a CLASS") {
		t.Errorf("a schema model that is not a CLASS must stop the run, got %v", err)
	}
	// An SDM context that is not commit-pinned.
	in = fixtureInputs(t)
	s := in[sdmContextInput("Parking")]
	s.URL = "https://example.org/master/context.jsonld"
	in[sdmContextInput("Parking")] = s
	if _, _, err := generate(in); err == nil {
		t.Error("an unpinned (non-commit) SDM source must be refused")
	}
	// Missing input.
	in = fixtureInputs(t)
	delete(in, inQUDT)
	if _, _, err := generate(in); err == nil {
		t.Error("a missing input must stop the run")
	}
	// A pack that comes out empty.
	in = fixtureInputs(t)
	e := in[inNGSI]
	e.Data = []byte(`{"@context": {"x": "https://elsewhere/x"}}`)
	in[inNGSI] = e
	if _, _, err := generate(in); err == nil || !strings.Contains(err.Error(), "no terms") {
		t.Errorf("an empty pack must not be emitted, got %v", err)
	}
}

func TestLoaderVerifiesHashes(t *testing.T) {
	dir := t.TempDir()
	data := []byte("pinned bytes")
	sum := sha256.Sum256(data)
	good := hex.EncodeToString(sum[:])
	lock, err := parseLock(strings.NewReader("# c\nthing " + good + " https://example.org/thing\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thing"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	l := &loader{lock: lock, cache: dir}
	if _, err := l.get("thing"); err != nil {
		t.Fatalf("good hash refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "thing"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.get("thing"); err == nil || !strings.Contains(err.Error(), "does not match the lock") {
		t.Fatalf("a hash mismatch must fail the run, got %v", err)
	}
	if _, err := l.get("absent"); err == nil {
		t.Error("an input not in the lock must be refused")
	}
	if _, err := (&loader{lock: lock, cache: t.TempDir()}).get("thing"); err == nil {
		t.Error("an uncached input without -fetch must be refused, not fetched silently")
	}
}

func TestParseLockRejectsMalformedLines(t *testing.T) {
	h := strings.Repeat("a", 64)
	for name, doc := range map[string]string{
		"two fields":  "x " + h,
		"short hash":  "x abc https://e/x",
		"non-hex":     "x " + strings.Repeat("z", 64) + " https://e/x",
		"http url":    "x " + h + " http://e/x",
		"duplicate":   "x " + h + " https://e/x\nx " + h + " https://e/y",
		"four fields": "x " + h + " https://e/x extra",
	} {
		if _, err := parseLock(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestCommittedLockCoversEveryInput guards the lock against drifting from the
// code: every input generate() reads must be pinned.
func TestCommittedLockCoversEveryInput(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "hack", "vocab", "sources.lock"))
	if err != nil {
		t.Skipf("lock not reachable from this working directory: %v", err)
	}
	defer f.Close()
	lock, err := parseLock(f)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range lock {
		names = append(names, n)
	}
	for _, n := range requiredInputs(names) {
		if _, ok := lock[n]; !ok {
			t.Errorf("input %q is read by vocabgen but not pinned in sources.lock", n)
		}
	}
}
