// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package semantic_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/semantic"
	"github.com/devicechain-io/dc-microservice/semantic/packs"
)

const (
	brickNS = "https://brickschema.org/schema/Brick#"
	sarefNS = "https://saref.etsi.org/core/"
)

var allPacks = semantic.TenantContext{EnabledPacks: []string{
	"brick", "saref", "s4city", "s4bldg", "sdm-streetlighting", "sdm-parking", "sdm-environment", "ngsi-ld",
}}

func reg(t *testing.T) *semantic.Registry {
	t.Helper()
	r, err := packs.Default()
	if err != nil {
		t.Fatalf("generated packs are inconsistent: %v", err)
	}
	return r
}

func ctxWith(ids ...string) semantic.TenantContext { return semantic.TenantContext{EnabledPacks: ids} }

func TestValidateReturnsTheExpandedIRI(t *testing.T) {
	r := reg(t)
	cases := []struct {
		curie string
		kind  semantic.Kind
		want  string
	}{
		{"brick:Luminaire", semantic.KindClass, brickNS + "Luminaire"},
		{"brick:feeds", semantic.KindPredicate, brickNS + "feeds"},
		{"brick:Operative_Temperature", semantic.KindObservableProperty, brickNS + "Operative_Temperature"},
		{"saref:Device", semantic.KindClass, sarefNS + "Device"},
		{"saref:Observation", semantic.KindClass, sarefNS + "Observation"},
		{"s4bldg:apparentPowerMax", semantic.KindObservableProperty, "https://saref.etsi.org/saref4bldg/apparentPowerMax"},
		{"sdm-streetlighting:Streetlight", semantic.KindClass, "https://smartdatamodels.org/dataModel.Streetlighting/Streetlight"},
		{"sdm-streetlighting:refStreetlightGroup", semantic.KindPredicate, "https://smartdatamodels.org/dataModel.Streetlighting/refStreetlightGroup"},
		{"sdm-parking:OnStreetParking", semantic.KindClass, "https://smartdatamodels.org/dataModel.Parking/OnStreetParking"},
		{"sdm-environment:pm25", semantic.KindObservableProperty, "https://smartdatamodels.org/dataModel.Environment/pm25"},
		{"ngsi-ld:Property", semantic.KindClass, "https://uri.etsi.org/ngsi-ld/Property"},
		{"ngsi-ld:observedAt", semantic.KindPredicate, "https://uri.etsi.org/ngsi-ld/observedAt"},
		// A full IRI inside an enabled namespace is accepted as itself.
		{brickNS + "Luminaire", semantic.KindClass, brickNS + "Luminaire"},
	}
	for _, c := range cases {
		got, err := r.Validate(c.curie, c.kind, allPacks)
		if err != nil {
			t.Errorf("Validate(%q, %s): %v", c.curie, c.kind, err)
			continue
		}
		if string(got) != c.want {
			t.Errorf("Validate(%q) = %q, want %q", c.curie, got, c.want)
		}
	}
}

// reach is an oracle written separately from Registry.IsA: a plain depth-first
// walk over the GENERATED parents, so the test asserts against the data and not
// against a memory of Brick.
func reach(p *semantic.Pack, from, to semantic.IRI) bool {
	seen := map[semantic.IRI]bool{}
	var walk func(semantic.IRI) bool
	walk = func(cur semantic.IRI) bool {
		if cur == to {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		for _, parent := range p.Terms[cur].Parents {
			if walk(parent) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

func TestIsAMatchesTheGeneratedData(t *testing.T) {
	r := reg(t)
	brick, ok := r.Pack("brick")
	if !ok {
		t.Fatal("no brick pack")
	}
	equipment := semantic.IRI(brickNS + "Equipment")
	if _, ok := brick.Terms[equipment]; !ok {
		t.Fatal("pinned Brick no longer declares brick:Equipment; this test's anchor needs a decision")
	}
	trues := 0
	for iri, term := range brick.Terms {
		if term.Kind != semantic.KindClass {
			continue
		}
		want := reach(brick, iri, equipment)
		if got := r.IsA(iri, equipment); got != want {
			t.Fatalf("IsA(%s, brick:Equipment) = %v, generated data says %v", iri, got, want)
		}
		if want {
			trues++
		}
	}
	if trues < 50 {
		t.Fatalf("only %d Brick classes reach brick:Equipment; the oracle comparison is close to vacuous", trues)
	}
	lum := semantic.IRI(brickNS + "Luminaire")
	if got, want := r.IsA(lum, equipment), reach(brick, lum, equipment); got != want {
		t.Fatalf("IsA(Luminaire, Equipment) = %v, data says %v", got, want)
	}
	if !r.IsA(lum, lum) {
		t.Error("IsA is reflexive for a known term")
	}
	if r.IsA(equipment, lum) {
		t.Error("IsA must not run backwards")
	}
}

func TestRefusals(t *testing.T) {
	r := reg(t)
	brickOnly := ctxWith("brick")
	cases := []struct {
		name  string
		curie string
		kind  semantic.Kind
		ctx   semantic.TenantContext
		want  error
	}{
		{"unknown term in an enabled pack", "brick:NoSuchThing", semantic.KindClass, allPacks, semantic.ErrUnknownTerm},
		{"unknown term by full IRI", brickNS + "NoSuchThing", semantic.KindClass, allPacks, semantic.ErrUnknownTerm},
		{"pack not enabled", "brick:Luminaire", semantic.KindClass, ctxWith("saref"), semantic.ErrUnknownPrefix},
		{"full IRI of a pack that is not enabled", brickNS + "Luminaire", semantic.KindClass, ctxWith("saref"), semantic.ErrUnknownPrefix},
		{"undeclared prefix", "nope:Thing", semantic.KindClass, allPacks, semantic.ErrUnknownPrefix},
		{"full IRI outside every namespace", "https://elsewhere.example/x", semantic.KindClass, allPacks, semantic.ErrUnknownPrefix},
		{"bare word", "Luminaire", semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"empty", "", semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"empty local part", "brick:", semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"local part starting with //", "brick://x", semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"namespace with no term", brickNS, semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"whitespace", "brick:Lumi naire", semantic.KindClass, allPacks, semantic.ErrMalformed},
		{"a predicate asked for as a class", "brick:feeds", semantic.KindClass, allPacks, semantic.ErrWrongKind},
		{"a class asked for as a predicate", "brick:Luminaire", semantic.KindPredicate, allPacks, semantic.ErrWrongKind},
		{"a class asked for as an observable property", "saref:Device", semantic.KindObservableProperty, allPacks, semantic.ErrWrongKind},
		{"a quantity asked for as a class", "brick:Operative_Temperature", semantic.KindClass, allPacks, semantic.ErrWrongKind},
		{"deprecated SAREF class", "saref:Measurement", semantic.KindClass, allPacks, semantic.ErrDeprecatedTerm},
		{"deprecated SAREF predicate", "saref:isMeasurementOf", semantic.KindPredicate, allPacks, semantic.ErrDeprecatedTerm},
		{"unknown pack in the context", "brick:Luminaire", semantic.KindClass, ctxWith("brick", "no-such-pack"), semantic.ErrUnknownPack},
		{"invalid kind", "brick:Luminaire", semantic.Kind("THING"), brickOnly, semantic.ErrInvalidKind},
		{"over the length limit", "brick:" + strings.Repeat("a", semantic.MaxCURIEBytes), semantic.KindClass, brickOnly, semantic.ErrMalformed},
	}
	for _, c := range cases {
		_, err := r.Validate(c.curie, c.kind, c.ctx)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: Validate(%.40q) = %v, want %v", c.name, c.curie, err, c.want)
			continue
		}
		var te *semantic.TermError
		if !errors.As(err, &te) || te.Term == "" && c.curie != "" {
			t.Errorf("%s: error does not name the offending term: %#v", c.name, err)
		}
	}
	_, err := r.Validate("saref:Measurement", semantic.KindClass, allPacks)
	if err == nil || !strings.Contains(err.Error(), sarefNS+"Observation") {
		t.Errorf("a deprecated term should say what replaces it, got %v", err)
	}
}

func TestDeprecatedBrickTermIsRefusedAndNamesItsReplacement(t *testing.T) {
	r := reg(t)
	brick, _ := r.Pack("brick")
	var found bool
	keys := make([]string, 0, len(brick.Terms))
	for iri := range brick.Terms {
		keys = append(keys, string(iri))
	}
	sort.Strings(keys)
	for _, k := range keys {
		term := brick.Terms[semantic.IRI(k)]
		if !term.Deprecated || term.ReplacedBy == "" {
			continue
		}
		found = true
		_, err := r.Validate(k, term.Kind, allPacks)
		if !errors.Is(err, semantic.ErrDeprecatedTerm) {
			t.Fatalf("%s is deprecated in the data but Validate says %v", k, err)
		}
		if !strings.Contains(err.Error(), string(term.ReplacedBy)) {
			t.Fatalf("%s: the refusal should name %s: %v", k, term.ReplacedBy, err)
		}
		break
	}
	if !found {
		t.Fatal("the pinned Brick has no deprecated term with a replacement; the deprecation path is untested")
	}
}

func TestCustomPrefixes(t *testing.T) {
	r := reg(t)
	ctx := semantic.TenantContext{
		EnabledPacks:   []string{"brick"},
		CustomPrefixes: map[string]string{"ex": "https://city.example/ontology#"},
	}
	got, err := r.Validate("ex:TrafficLight", semantic.KindClass, ctx)
	if err != nil || got != "https://city.example/ontology#TrafficLight" {
		t.Fatalf("custom term: %q, %v", got, err)
	}
	// Opaque: any kind is accepted and nothing is reasoned over.
	for _, k := range []semantic.Kind{semantic.KindClass, semantic.KindPredicate, semantic.KindObservableProperty} {
		if _, err := r.Validate("ex:Anything", k, ctx); err != nil {
			t.Errorf("custom term refused as %s: %v", k, err)
		}
	}
	res, err := r.Resolve("ex:TrafficLight", semantic.KindClass, ctx)
	if err != nil || !res.Opaque || res.Pack != "" {
		t.Fatalf("a custom term must be labelled opaque: %+v, %v", res, err)
	}
	res, err = r.Resolve("brick:Luminaire", semantic.KindClass, ctx)
	if err != nil || res.Opaque || res.Pack != "brick" || res.Term.Label == "" {
		t.Fatalf("a pack term must not be opaque: %+v, %v", res, err)
	}
	// IsA on a custom term is false -- never true, not even reflexively, not even
	// against a real ancestor name it might claim.
	custom := semantic.IRI("https://city.example/ontology#TrafficLight")
	if r.IsA(custom, custom) || r.IsA(custom, brickNS+"Equipment") {
		t.Error("IsA on a custom-prefix term must be false")
	}
	if _, ok := r.Term(custom); ok {
		t.Error("a custom term must not appear in the registry")
	}
	// A full IRI in a custom namespace is accepted too.
	if _, err := r.Validate("https://city.example/ontology#Lamp", semantic.KindClass, ctx); err != nil {
		t.Errorf("full custom IRI: %v", err)
	}
	// A custom prefix is not a pack: without it declared, the prefix is unknown.
	if _, err := r.Validate("ex:TrafficLight", semantic.KindClass, ctxWith("brick")); !errors.Is(err, semantic.ErrUnknownPrefix) {
		t.Errorf("undeclared custom prefix: %v", err)
	}
}

func TestCustomPrefixContextRules(t *testing.T) {
	r := reg(t)
	bad := func(name string, custom map[string]string, want error) {
		t.Helper()
		ctx := semantic.TenantContext{EnabledPacks: []string{"saref"}, CustomPrefixes: custom}
		if _, err := r.Validate("saref:Device", semantic.KindClass, ctx); !errors.Is(err, want) {
			t.Errorf("%s: Validate = %v, want %v", name, err, want)
		}
		if err := r.ValidateContext(ctx); !errors.Is(err, want) {
			t.Errorf("%s: ValidateContext = %v, want %v", name, err, want)
		}
	}
	bad("shadows a pack prefix", map[string]string{"brick": "https://city.example/b#"}, semantic.ErrPrefixShadow)
	bad("shadows the prefix of a pack that is not even enabled", map[string]string{"ngsi-ld": "https://city.example/n#"}, semantic.ErrPrefixShadow)
	bad("namespace inside a pack namespace", map[string]string{"mine": sarefNS + "mine/"}, semantic.ErrPrefixShadow)
	bad("namespace inside a fragment-style pack namespace", map[string]string{"mine": brickNS + "Mine/"}, semantic.ErrMalformed)
	bad("namespace containing a pack namespace", map[string]string{"mine": "https://brickschema.org/"}, semantic.ErrPrefixShadow)
	bad("two custom namespaces overlap", map[string]string{"a": "https://c.example/o/", "b": "https://c.example/o/sub/"}, semantic.ErrPrefixShadow)
	bad("same namespace twice", map[string]string{"a": "https://c.example/o#", "b": "https://c.example/o#"}, semantic.ErrPrefixShadow)
	bad("plain http namespace", map[string]string{"a": "http://c.example/o#"}, semantic.ErrMalformed)
	bad("ftp namespace", map[string]string{"a": "ftp://c.example/o#"}, semantic.ErrMalformed)
	bad("https with no host", map[string]string{"a": "https:///o#"}, semantic.ErrMalformed)
	bad("empty namespace", map[string]string{"a": ""}, semantic.ErrMalformed)
	bad("prefix is a scheme", map[string]string{"https": "https://c.example/o#"}, semantic.ErrMalformed)
	bad("prefix with a space", map[string]string{"a b": "https://c.example/o#"}, semantic.ErrMalformed)
	bad("prefix starting with a digit", map[string]string{"1a": "https://c.example/o#"}, semantic.ErrMalformed)

	many := map[string]string{}
	for i := 0; i <= semantic.MaxCustomPrefixes; i++ {
		many["p"+strings.Repeat("x", i)] = "https://c.example/" + strings.Repeat("x", i) + "/"
	}
	bad("too many custom prefixes", many, semantic.ErrLimit)

	ok := map[string]string{"a": "https://c.example/a#", "urn-ns": "urn:example:city:"}
	fill := semantic.MaxCustomPrefixes - len(ok)
	for i := 0; i < fill; i++ {
		ok["q"+strings.Repeat("y", i)] = "https://c.example/" + strings.Repeat("y", i) + "q/"
	}
	if len(ok) != semantic.MaxCustomPrefixes {
		t.Fatalf("test setup: %d prefixes", len(ok))
	}
	if err := r.ValidateContext(semantic.TenantContext{CustomPrefixes: ok}); err != nil {
		t.Errorf("exactly the limit must be allowed: %v", err)
	}
	if got, err := r.Validate("urn-ns:lamp1", semantic.KindClass, semantic.TenantContext{CustomPrefixes: ok}); err != nil || got != "urn:example:city:lamp1" {
		t.Errorf("urn namespace: %q, %v", got, err)
	}
}

func TestCyclicParentsTerminate(t *testing.T) {
	const ns = "https://cycle.example/ns#"
	mk := func(local string, parents ...string) semantic.Term {
		term := semantic.Term{IRI: semantic.IRI(ns + local), Label: local, Kind: semantic.KindClass}
		for _, p := range parents {
			term.Parents = append(term.Parents, semantic.IRI(ns+p))
		}
		return term
	}
	terms := map[semantic.IRI]semantic.Term{}
	for _, term := range []semantic.Term{mk("A", "B"), mk("B", "A", "C"), mk("C", "C"), mk("Z")} {
		terms[term.IRI] = term
	}
	r, err := semantic.NewRegistry([]*semantic.Pack{{
		ID: "cycle", Prefix: "cyc", Namespace: ns, Version: "1", Licence: "Apache-2.0", Terms: terms,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsA(ns+"A", ns+"C") || !r.IsA(ns+"B", ns+"A") || !r.IsA(ns+"A", ns+"A") {
		t.Error("a cycle must not hide a real ancestor")
	}
	if r.IsA(ns+"A", ns+"Z") || r.IsA(ns+"C", ns+"A") {
		t.Error("a cycle must not invent an ancestor")
	}
}

func TestNewRegistryRefusesInconsistentPacks(t *testing.T) {
	const ns = "https://x.example/ns#"
	good := func() *semantic.Pack {
		return &semantic.Pack{ID: "x", Prefix: "x", Namespace: ns, Terms: map[semantic.IRI]semantic.Term{
			ns + "A": {IRI: ns + "A", Kind: semantic.KindClass},
		}}
	}
	if _, err := semantic.NewRegistry([]*semantic.Pack{good()}, nil); err != nil {
		t.Fatalf("baseline pack refused: %v", err)
	}
	mutate := map[string]func(p *semantic.Pack){
		"term filed under the wrong key": func(p *semantic.Pack) {
			p.Terms[ns+"B"] = semantic.Term{IRI: ns + "A", Kind: semantic.KindClass}
		},
		"term outside the namespace": func(p *semantic.Pack) {
			p.Terms["https://other.example/A"] = semantic.Term{IRI: "https://other.example/A", Kind: semantic.KindClass}
		},
		"invalid kind": func(p *semantic.Pack) { p.Terms[ns+"A"] = semantic.Term{IRI: ns + "A", Kind: "THING"} },
		"bad id":       func(p *semantic.Pack) { p.ID = "Bad ID" },
		"bad prefix":   func(p *semantic.Pack) { p.Prefix = "1x" },
		"bad namespace": func(p *semantic.Pack) {
			p.Namespace = "ftp://x.example/"
		},
	}
	for name, m := range mutate {
		p := good()
		m(p)
		if _, err := semantic.NewRegistry([]*semantic.Pack{p}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other := good()
	other.ID, other.Prefix = "y", "y"
	if _, err := semantic.NewRegistry([]*semantic.Pack{good(), other}, nil); err == nil {
		t.Error("two packs with overlapping namespaces accepted")
	}
	dupID := good()
	dupID.Prefix, dupID.Namespace = "y", "https://y.example/ns#"
	dupID.Terms = map[semantic.IRI]semantic.Term{"https://y.example/ns#A": {IRI: "https://y.example/ns#A", Kind: semantic.KindClass}}
	if _, err := semantic.NewRegistry([]*semantic.Pack{good(), dupID}, nil); err == nil {
		t.Error("duplicate pack id accepted")
	}
}

func TestUnitTable(t *testing.T) {
	r := reg(t)
	u, ok := r.UnitByUCUM("Cel")
	if !ok || u.CommonCode != "CEL" {
		t.Fatalf("Cel = %+v, %v", u, ok)
	}
	back, ok := r.UnitByCommonCode("CEL")
	if !ok || back.UCUM != "Cel" {
		t.Fatalf("CEL = %+v, %v", back, ok)
	}
	if kw, ok := r.UnitByUCUM("kW"); !ok || kw.CommonCode != "KWT" {
		t.Errorf("kW = %+v, %v", kw, ok)
	}
	if _, ok := r.UnitByUCUM("not-a-unit"); ok {
		t.Error("an unknown unit was found")
	}
	units := r.Units()
	if len(units) < 500 {
		t.Fatalf("only %d units", len(units))
	}
	if !sort.SliceIsSorted(units, func(i, j int) bool { return units[i].UCUM < units[j].UCUM }) {
		t.Error("Units() is not sorted by UCUM code")
	}
	if p, ok := r.UnitPack(); !ok || p.Licence == "" || p.SourceSHA256 == "" || len(p.Units) != 0 {
		t.Errorf("unit provenance = %+v, %v", p, ok)
	}
}

func readLock(t *testing.T) map[string][2]string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "hack", "vocab", "sources.lock"))
	if err != nil {
		t.Fatalf("the source lock must be reachable from the semantic package: %v", err)
	}
	defer f.Close()
	out := map[string][2]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fl := strings.Fields(line)
		out[fl[0]] = [2]string{fl[1], fl[2]}
	}
	return out
}

// TestPacksCiteTheLock ties each generated pack to a line of the lock: the same
// URL and the same digest. A table regenerated from a different file than the
// one the lock pins fails here even though it compiles.
func TestPacksCiteTheLock(t *testing.T) {
	lock := readLock(t)
	byPack := map[string]string{
		"brick": "brick-ttl", "saref": "saref-core-ttl", "s4city": "saref4city-ttl", "s4bldg": "saref4bldg-ttl",
		"ngsi-ld": "ngsi-ld-core-context", "sdm-streetlighting": "sdm-streetlighting-context",
		"sdm-parking": "sdm-parking-context", "sdm-environment": "sdm-environment-context",
	}
	r := reg(t)
	if len(r.Packs()) != len(byPack) {
		t.Fatalf("%d packs generated, test knows %d", len(r.Packs()), len(byPack))
	}
	for _, p := range r.Packs() {
		name, ok := byPack[p.ID]
		if !ok {
			t.Errorf("pack %s has no lock mapping in this test", p.ID)
			continue
		}
		if got := lock[name]; got[0] != p.SourceSHA256 || got[1] != p.SourceURL {
			t.Errorf("pack %s cites %s %s, lock says %v", p.ID, p.SourceURL, p.SourceSHA256, got)
		}
		if p.Licence == "" || p.Version == "" || len(p.Terms) == 0 {
			t.Errorf("pack %s is missing licence, version or terms", p.ID)
		}
	}
	up, _ := r.UnitPack()
	if got := lock["qudt-units-ttl"]; got[0] != up.SourceSHA256 || got[1] != up.SourceURL {
		t.Errorf("unit table cites %s %s, lock says %v", up.SourceURL, up.SourceSHA256, got)
	}
}

// TestLicenceNoticesArePresent covers the redistribution terms of what is
// embedded: every generated file carries the SPDX header and the upstream
// notice, and the repository NOTICE credits each vocabulary.
func TestLicenceNoticesArePresent(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("packs", "*_gen.go"))
	if err != nil || len(files) != 9 {
		t.Fatalf("generated files = %v, %v", files, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		head := string(b)
		if i := strings.Index(head, "\npackage "); i > 0 {
			head = head[:i]
		}
		if !strings.HasPrefix(head, "// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n") {
			t.Errorf("%s lacks the SPDX header", f)
		}
		if !strings.Contains(head, "DO NOT EDIT") {
			t.Errorf("%s is not marked generated", f)
		}
		if !strings.Contains(head, "Redistribution and use") && !strings.Contains(head, "Creative Commons") {
			t.Errorf("%s carries no upstream licence notice", f)
		}
	}
	notice, err := os.ReadFile(filepath.Join("..", "..", "..", "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Brick", "SAREF", "Smart Data Models", "NGSI-LD", "QUDT", "UCUM", "BSD-3-Clause", "CC BY 4.0", "Redistributions in binary form must reproduce", "creativecommons.org/licenses/by/4.0", "UCUM License"} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("NOTICE does not mention %q", want)
		}
	}
}

func TestPinnedContexts(t *testing.T) {
	lock := readLock(t)
	byFile := map[string]string{
		"ngsi-ld-core-context.jsonld": "ngsi-ld-core-context",
		"sdm-streetlighting.jsonld":   "sdm-streetlighting-context",
		"sdm-parking.jsonld":          "sdm-parking-context",
		"sdm-environment.jsonld":      "sdm-environment-context",
	}
	infos := semantic.PinnedContexts()
	if len(infos) != len(byFile) {
		t.Fatalf("%d pinned contexts", len(infos))
	}
	for _, info := range infos {
		b, ok := semantic.PinnedContext(info.URL)
		if !ok {
			t.Errorf("%s: listed but not served", info.URL)
			continue
		}
		sum := sha256.Sum256(b)
		got := hex.EncodeToString(sum[:])
		if got != info.SHA256 || got != lock[byFile[info.File]][0] {
			t.Errorf("%s: served digest %s, index %s, lock %s", info.URL, got, info.SHA256, lock[byFile[info.File]][0])
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(b, &doc) != nil || doc["@context"] == nil {
			t.Errorf("%s: not a JSON-LD context", info.URL)
		}
		if info.Licence == "" || !strings.HasPrefix(info.URL, "https://") {
			t.Errorf("%s: licence or URL missing", info.URL)
		}
	}
	for _, url := range []string{
		"", "https://uri.etsi.org/ngsi-ld/v1/ngsi-ld-core-context.jsonld/",
		"https://URI.etsi.org/ngsi-ld/v1/ngsi-ld-core-context.jsonld",
		"https://uri.etsi.org/ngsi-ld/v1/ngsi-ld-core-context.jsonld?x=1",
		"https://uri.etsi.org/ngsi-ld/v1/ngsi-ld-core-context-v1.8.jsonld",
	} {
		if _, ok := semantic.PinnedContext(url); ok {
			t.Errorf("%q must not be served: only exact pinned URLs are", url)
		}
	}
}
