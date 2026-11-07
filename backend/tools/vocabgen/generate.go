// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
)

// packSpec is everything vocabgen knows about one pack: where it comes from and
// how its terms are classified. The classification rules live next to their
// reasons in build.go and in the specs below.
type packSpec struct {
	GoName    string
	ID        string
	Prefix    string
	Namespace string
	Licence   string // SPDX identifier
	// Primary is the lock input recorded as the pack's source (URL and digest).
	Primary string
	// Inputs are parsed and merged into the graph the rules read.
	Inputs []string
	// Version derives the pack's version from its primary source.
	Version func(primary source, in map[string]source) (string, error)
	// Notice is the upstream licence notice reproduced in the generated file.
	Notice func(in map[string]source) (string, error)
	build  func(in map[string]source) ([]term, []string, error)
}

const (
	brickNS = "https://brickschema.org/schema/Brick#"
	sarefNS = "https://saref.etsi.org/core/"
	ngsiNS  = "https://uri.etsi.org/ngsi-ld/"
	sdmBase = "https://smartdatamodels.org/dataModel."
)

// Lock input names.
const (
	inBrick        = "brick-ttl"
	inBrickLicence = "brick-license"
	inSarefCore    = "saref-core-ttl"
	inSarefCity    = "saref4city-ttl"
	inSarefBldg    = "saref4bldg-ttl"
	inNGSI         = "ngsi-ld-core-context"
	inQUDT         = "qudt-units-ttl"
	schemaPrefix   = "sdm-schema-"
)

func sdmContextInput(domain string) string { return "sdm-" + strings.ToLower(domain) + "-context" }

func graphOf(in map[string]source, names ...string) (*graph, error) {
	g := newGraph()
	for _, n := range names {
		s, ok := in[n]
		if !ok {
			return nil, fmt.Errorf("input %q was not loaded", n)
		}
		ts, err := parseTurtle(string(s.Data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		g.add(ts)
	}
	return g, nil
}

var (
	urlTag    = regexp.MustCompile(`/(v[0-9]+(?:\.[0-9]+)*)/`)
	urlCommit = regexp.MustCompile(`/([0-9a-f]{40})/`)
)

func tagVersion(trim string) func(source, map[string]source) (string, error) {
	return func(p source, _ map[string]source) (string, error) {
		m := urlTag.FindStringSubmatch(p.URL)
		if m == nil {
			return "", fmt.Errorf("%s: no version tag in %s", p.Name, p.URL)
		}
		return strings.TrimPrefix(m[1], trim), nil
	}
}

func commitVersion(p source, _ map[string]source) (string, error) {
	m := urlCommit.FindStringSubmatch(p.URL)
	if m == nil {
		return "", fmt.Errorf("%s: the url must pin a 40-hex commit, got %s", p.Name, p.URL)
	}
	return "git-" + m[1][:12], nil
}

func fixedVersion(v string) func(source, map[string]source) (string, error) {
	return func(source, map[string]source) (string, error) { return v, nil }
}

const bsd3Terms = `Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.`

func staticNotice(s string) func(map[string]source) (string, error) {
	return func(map[string]source) (string, error) { return s, nil }
}

var etsiNotice = `This table is derived from the SAREF family of ontologies published by ETSI
(https://saref.etsi.org/), licensed under the ETSI Software License, a
BSD-3-Clause licence (https://forge.etsi.org/etsi-software-license).

Copyright (c) ETSI. All rights reserved.

` + bsd3Terms

var ngsiNotice = `This table is derived from the NGSI-LD core context published by ETSI
(https://uri.etsi.org/ngsi-ld/v1/ngsi-ld-core-context.jsonld), licensed under the
BSD-3-Clause licence of the ETSI CIM NGSI-LD repository.

Copyright 2019 ETSI. All rights reserved.

` + bsd3Terms

func sdmNotice(domain string) string {
	return fmt.Sprintf(`This table is derived from the Smart Data Models "dataModel.%s" context
(https://github.com/smart-data-models/dataModel.%s), licensed under the Creative Commons Attribution 4.0 International licence
(https://creativecommons.org/licenses/by/4.0/).

Attribution: the Smart Data Models initiative and the contributors listed in the
repository's CONTRIBUTORS.yaml. Changes: the context's term names were
classified and converted into a Go table; no upstream definition was altered.`, domain, domain)
}

const unitNotice = `This table is derived from the QUDT unit vocabulary (https://qudt.org/), issued
under the Creative Commons Attribution 4.0 International licence
(https://creativecommons.org/licenses/by/4.0/). Attribution should be made to
QUDT.org. Changes: one row per UCUM code was selected and converted into a Go
table.

The UCUM codes it carries are subject to the UCUM licence
(https://ucum.org/license): the Unified Code for Units of Measure is copyright
1999-2024 Regenstrief Institute, Inc. and the UCUM Organization, all rights
reserved. The UCUM table and UCUM codes are provided AS IS; any express or
implied warranty is disclaimed.`

func owlBuild(names []string, rules owlRules) func(map[string]source) ([]term, []string, error) {
	return func(in map[string]source) ([]term, []string, error) {
		g, err := graphOf(in, names...)
		if err != nil {
			return nil, nil, err
		}
		return buildOWL(g, rules)
	}
}

func contextBuild(name string, rules contextRules) func(map[string]source) ([]term, []string, error) {
	return func(in map[string]source) ([]term, []string, error) {
		s, ok := in[name]
		if !ok {
			return nil, nil, fmt.Errorf("input %q was not loaded", name)
		}
		ts, err := buildContext(s.Data, rules)
		return ts, nil, err
	}
}

// sdmModels are the model directories of each pinned Smart Data Models commit
// (the entity types), and sdmUpperAttributes the attributes whose names start
// with a capital. See sdmClassifier for why neither is inferred from case.
var (
	sdmModels = map[string][]string{
		"Streetlighting": {"Streetlight", "StreetlightControlCabinet", "StreetlightFeeder", "StreetlightGroup", "StreetlightModel"},
		"Parking":        {"OffStreetParking", "OnStreetParking", "ParkingAccess", "ParkingGroup", "ParkingSpot"},
		"Environment": {
			"AeroAllergenObserved", "AirQualityForecast", "AirQualityMonitoring", "AirQualityObserved", "CarbonFootprint",
			"ElectroMagneticObserved", "EnvironmentObserved", "FloodMonitoring", "IndoorEnvironmentObserved", "MosquitoDensity",
			"NightSkyQuality", "NoiseLevelObserved", "NoisePollution", "NoisePollutionForecast", "PhreaticObserved",
			"RainFallRadarObserved", "TrafficEnvironmentImpact", "TrafficEnvironmentImpactForecast", "WaterObserved",
		},
	}
	sdmUpperAttributes = map[string][]string{
		"Streetlighting": {"RFId"},
		"Environment":    {"CO2eq", "LANight", "LAS", "LAeq", "LAeq2", "LAeq_d", "LAmax", "LAmax2", "Laeq2", "Lamax2", "RFID"},
	}
)

func sdmSpec(domain, goName string) packSpec {
	ns := sdmBase + domain + "/"
	in := sdmContextInput(domain)
	return packSpec{
		GoName: goName, ID: "sdm-" + strings.ToLower(domain), Prefix: "sdm-" + strings.ToLower(domain),
		Namespace: ns, Licence: "CC-BY-4.0", Primary: in, Inputs: []string{in},
		Version: commitVersion,
		Notice:  staticNotice(sdmNotice(domain)),
		build: contextBuild(in, contextRules{
			namespace: ns, classify: sdmClassifier(sdmModels[domain], sdmUpperAttributes[domain]), requireClasses: sdmModels[domain],
		}),
	}
}

func specs() []packSpec {
	return []packSpec{
		{
			GoName: "Brick", ID: "brick", Prefix: "brick", Namespace: brickNS, Licence: "BSD-3-Clause",
			Primary: inBrick, Inputs: []string{inBrick},
			Version: tagVersion("v"),
			Notice: func(in map[string]source) (string, error) {
				l, ok := in[inBrickLicence]
				if !ok {
					return "", fmt.Errorf("input %q was not loaded", inBrickLicence)
				}
				return "This table is derived from the Brick schema (https://brickschema.org/), licensed\nunder the BSD-3-Clause licence reproduced below.\n\n" +
					strings.TrimSpace(string(l.Data)), nil
			},
			build: owlBuild([]string{inBrick}, owlRules{
				namespace:           brickNS,
				quantityType:        brickNS + "Quantity",
				replacedByPredicate: brickNS + "isReplacedBy",
			}),
		},
		{
			GoName: "Saref", ID: "saref", Prefix: "saref", Namespace: sarefNS, Licence: "BSD-3-Clause",
			Primary: inSarefCore, Inputs: []string{inSarefCore},
			Version: tagVersion("v"),
			Notice:  staticNotice(etsiNotice),
			build: owlBuild([]string{inSarefCore}, owlRules{
				namespace:     sarefNS,
				propertyClass: sarefNS + "Property",
				tombstones: []tombstone{
					{
						IRI: sarefNS + "Measurement", Kind: kindClass, Label: "Measurement", ReplacedBy: sarefNS + "Observation",
						Reason: "the pinned core's change log: \"Deprecated saref:Measurement in favour of saref:Observation\"; v4.1.1 no longer declares it",
					},
					{
						IRI: sarefNS + "isMeasurementOf", Kind: kindPredicate, Label: "is measurement of",
						Reason: "the pinned core's change log: \"Deprecated saref:isMeasurementOf together with saref:Measurement\"; v4.1.1 no longer declares it",
					},
				},
			}),
		},
		{
			GoName: "Saref4City", ID: "s4city", Prefix: "s4city", Namespace: "https://saref.etsi.org/saref4city/", Licence: "BSD-3-Clause",
			Primary: inSarefCity, Inputs: []string{inSarefCity, inSarefCore},
			Version: tagVersion("v"),
			Notice:  staticNotice(etsiNotice),
			build: owlBuild([]string{inSarefCity, inSarefCore}, owlRules{
				namespace: "https://saref.etsi.org/saref4city/", propertyClass: sarefNS + "Property",
			}),
		},
		{
			GoName: "Saref4Bldg", ID: "s4bldg", Prefix: "s4bldg", Namespace: "https://saref.etsi.org/saref4bldg/", Licence: "BSD-3-Clause",
			Primary: inSarefBldg, Inputs: []string{inSarefBldg, inSarefCore},
			Version: tagVersion("v"),
			Notice:  staticNotice(etsiNotice),
			build: owlBuild([]string{inSarefBldg, inSarefCore}, owlRules{
				namespace: "https://saref.etsi.org/saref4bldg/", propertyClass: sarefNS + "Property",
			}),
		},
		sdmSpec("Streetlighting", "SDMStreetlighting"),
		sdmSpec("Parking", "SDMParking"),
		sdmSpec("Environment", "SDMEnvironment"),
		{
			GoName: "NGSILD", ID: "ngsi-ld", Prefix: "ngsi-ld", Namespace: ngsiNS, Licence: "BSD-3-Clause",
			Primary: inNGSI, Inputs: []string{inNGSI},
			Version: fixedVersion("v1"),
			Notice:  staticNotice(ngsiNotice),
			build:   contextBuild(inNGSI, contextRules{namespace: ngsiNS, classify: classifyNGSILD}),
		},
	}
}

// pinnedContext is a context file the semantic package embeds and serves by
// exact URL.
type pinnedContext struct {
	Input   string
	File    string
	URL     string
	Licence string
}

func pinnedContexts() []pinnedContext {
	return []pinnedContext{
		{inNGSI, "ngsi-ld-core-context.jsonld", "https://uri.etsi.org/ngsi-ld/v1/ngsi-ld-core-context.jsonld", "BSD-3-Clause"},
		{sdmContextInput("Streetlighting"), "sdm-streetlighting.jsonld", "https://smart-data-models.github.io/dataModel.Streetlighting/context.jsonld", "CC-BY-4.0"},
		{sdmContextInput("Parking"), "sdm-parking.jsonld", "https://smart-data-models.github.io/dataModel.Parking/context.jsonld", "CC-BY-4.0"},
		{sdmContextInput("Environment"), "sdm-environment.jsonld", "https://smart-data-models.github.io/dataModel.Environment/context.jsonld", "CC-BY-4.0"},
	}
}

// requiredInputs is every lock name generate reads, schemas included.
func requiredInputs(lockNames []string) []string {
	seen := map[string]bool{
		inBrick: true, inBrickLicence: true, inSarefCore: true, inSarefCity: true, inSarefBldg: true,
		inNGSI: true, inQUDT: true,
	}
	for _, d := range []string{"Streetlighting", "Parking", "Environment"} {
		seen[sdmContextInput(d)] = true
	}
	for _, n := range lockNames {
		if strings.HasPrefix(n, schemaPrefix) {
			seen[n] = true
		}
	}
	return sortedKeys(boolSet(seen))
}

func boolSet(m map[string]bool) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// generate builds every output file from verified inputs. It is pure: the same
// inputs give byte-identical files, in the same order, on every run. Paths are
// relative to backend/core/semantic.
func generate(in map[string]source) (files map[string][]byte, report string, err error) {
	files = map[string][]byte{}
	var rep strings.Builder
	built := map[string]map[string]term{} // pack id -> iri -> term

	for _, sp := range specs() {
		primary, ok := in[sp.Primary]
		if !ok {
			return nil, "", fmt.Errorf("pack %s: input %q was not loaded", sp.ID, sp.Primary)
		}
		version, err := sp.Version(primary, in)
		if err != nil {
			return nil, "", fmt.Errorf("pack %s: %w", sp.ID, err)
		}
		notice, err := sp.Notice(in)
		if err != nil {
			return nil, "", fmt.Errorf("pack %s: %w", sp.ID, err)
		}
		terms, skipped, err := sp.build(in)
		if err != nil {
			return nil, "", fmt.Errorf("pack %s: %w", sp.ID, err)
		}
		if len(terms) == 0 {
			return nil, "", fmt.Errorf("pack %s: no terms were produced; refusing to emit an empty pack", sp.ID)
		}
		for i := range terms {
			if !strings.HasPrefix(terms[i].IRI, sp.Namespace) {
				return nil, "", fmt.Errorf("pack %s: term %s is outside %s", sp.ID, terms[i].IRI, sp.Namespace)
			}
		}
		built[sp.ID] = map[string]term{}
		counts := map[string]int{}
		for _, t := range terms {
			built[sp.ID][t.IRI] = t
			counts[t.Kind]++
		}
		src, err := emitPack(sp, version, primary, notice, terms)
		if err != nil {
			return nil, "", fmt.Errorf("pack %s: %w", sp.ID, err)
		}
		files["packs/"+strings.ReplaceAll(sp.ID, "-", "_")+"_gen.go"] = src
		fmt.Fprintf(&rep, "%-20s %5d terms (class %d, predicate %d, observable %d), %d subjects left out\n",
			sp.ID, len(terms), counts[kindClass], counts[kindPredicate], counts[kindObservable], len(skipped))
	}

	if err := checkSchemas(in, built); err != nil {
		return nil, "", err
	}

	q, ok := in[inQUDT]
	if !ok {
		return nil, "", fmt.Errorf("input %q was not loaded", inQUDT)
	}
	qg, err := graphOf(in, inQUDT)
	if err != nil {
		return nil, "", err
	}
	rows, dropped := buildUnits(qg)
	if len(rows) == 0 {
		return nil, "", fmt.Errorf("unit table: no rows; refusing to emit an empty table")
	}
	qv, err := tagVersion("v")(q, in)
	if err != nil {
		return nil, "", err
	}
	usrc, err := emitUnits(rows, "qudt-"+qv, q)
	if err != nil {
		return nil, "", err
	}
	files["packs/unit_gen.go"] = usrc
	withCode := 0
	for _, r := range rows {
		if r.CommonCode != "" {
			withCode++
		}
	}
	fmt.Fprintf(&rep, "%-20s %5d units (%d with a UN/CEFACT common code), %d duplicate UCUM claims dropped\n", "unit", len(rows), withCode, dropped)

	pinned := pinnedContexts()
	sort.Slice(pinned, func(i, j int) bool { return pinned[i].URL < pinned[j].URL })
	for _, pc := range pinned {
		s, ok := in[pc.Input]
		if !ok {
			return nil, "", fmt.Errorf("pinned context: input %q was not loaded", pc.Input)
		}
		files["contexts/"+pc.File] = append([]byte(nil), s.Data...)
	}
	cg, err := emitContextIndex(pinned, in)
	if err != nil {
		return nil, "", err
	}
	files["contexts_gen.go"] = cg
	return files, rep.String(), nil
}

// checkSchemas is a generation-time cross-check, not a source of terms. Each
// pinned Smart Data Models schema is named sdm-schema-<Model> in the lock, and its
// "$id" (.../dataModel.<Domain>/.../schema.json) says which domain it belongs to;
// <Model> must have come out of that domain's context as a CLASS. A model the
// context does not carry, or one the classifier took for an attribute, stops the
// run. The model comes from the lock name and not from the "$id" path on purpose:
// upstream's own ids are not reliable there (StreetlightFeeder's reads
// "StreetLightFeeder" and StreetlightModel's reads "Streetlight").
func checkSchemas(in map[string]source, built map[string]map[string]term) error {
	re := regexp.MustCompile(`/dataModel\.([A-Za-z]+)/[A-Za-z0-9]+/schema\.json$`)
	var names []string
	for n := range in {
		if strings.HasPrefix(n, schemaPrefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("no %s* inputs: the pinned schemas are the cross-check on the context classification", schemaPrefix)
	}
	for _, n := range names {
		var doc struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(in[n].Data, &doc); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		m := re.FindStringSubmatch(doc.ID)
		if m == nil {
			return fmt.Errorf("%s: $id %q is not a Smart Data Models schema id", n, doc.ID)
		}
		pack, ok := built["sdm-"+strings.ToLower(m[1])]
		if !ok {
			return fmt.Errorf("%s: no pack for domain %s", n, m[1])
		}
		model := strings.TrimPrefix(n, schemaPrefix)
		t, ok := pack[sdmBase+m[1]+"/"+model]
		if !ok || t.Kind != kindClass {
			return fmt.Errorf("%s: model %s is not a CLASS in pack sdm-%s (present: %v)", n, model, strings.ToLower(m[1]), ok)
		}
	}
	return nil
}

func emitPack(sp packSpec, version string, primary source, notice string, terms []term) ([]byte, error) {
	var b bytes.Buffer
	writeHeader(&b, "packs", notice)
	b.WriteString("import \"github.com/devicechain-io/dc-microservice/semantic\"\n\n")
	fmt.Fprintf(&b, "// %s is the %s pack, version %s.\n", sp.GoName, sp.ID, version)
	fmt.Fprintf(&b, "var %s = &semantic.Pack{\n", sp.GoName)
	fmt.Fprintf(&b, "\tID: %q,\n\tPrefix: %q,\n\tNamespace: %q,\n\tVersion: %q,\n\tLicence: %q,\n\tSourceURL: %q,\n\tSourceSHA256: %q,\n",
		sp.ID, sp.Prefix, sp.Namespace, version, sp.Licence, primary.URL, primary.SHA256)
	b.WriteString("\tTerms: map[semantic.IRI]semantic.Term{\n")
	sort.Slice(terms, func(i, j int) bool { return terms[i].IRI < terms[j].IRI })
	for _, t := range terms {
		fmt.Fprintf(&b, "\t\t%q: {IRI: %q, Label: %q, Kind: semantic.%s", t.IRI, t.IRI, t.Label, kindConst(t.Kind))
		if len(t.Parents) > 0 {
			b.WriteString(", Parents: []semantic.IRI{")
			for i, p := range t.Parents {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%q", p)
			}
			b.WriteString("}")
		}
		if t.Inverse != "" {
			fmt.Fprintf(&b, ", Inverse: iri(%q)", t.Inverse)
		}
		if t.Deprecated {
			b.WriteString(", Deprecated: true")
		}
		if t.ReplacedBy != "" {
			fmt.Fprintf(&b, ", ReplacedBy: %q", t.ReplacedBy)
		}
		b.WriteString("},\n")
	}
	b.WriteString("\t},\n}\n\n")
	return format.Source(b.Bytes())
}

func kindConst(k string) string {
	switch k {
	case kindClass:
		return "KindClass"
	case kindPredicate:
		return "KindPredicate"
	}
	return "KindObservableProperty"
}

func emitUnits(rows []unit, version string, src source) ([]byte, error) {
	var b bytes.Buffer
	writeHeader(&b, "packs", unitNotice)
	b.WriteString("import \"github.com/devicechain-io/dc-microservice/semantic\"\n\n")
	b.WriteString("// Unit is the UCUM to UN/CEFACT common code table.\n")
	b.WriteString("var Unit = &semantic.UnitPack{\n")
	fmt.Fprintf(&b, "\tID: \"unit\",\n\tVersion: %q,\n\tLicence: \"CC-BY-4.0\",\n\tSourceURL: %q,\n\tSourceSHA256: %q,\n", version, src.URL, src.SHA256)
	b.WriteString("\tUnits: []semantic.Unit{\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "\t\t{UCUM: %q, CommonCode: %q, Label: %q, IRI: %q},\n", r.UCUM, r.CommonCode, r.Label, r.IRI)
	}
	b.WriteString("\t},\n}\n")
	return format.Source(b.Bytes())
}

func emitContextIndex(pinned []pinnedContext, in map[string]source) ([]byte, error) {
	var b bytes.Buffer
	writeHeader(&b, "semantic", "Each context is copied byte for byte from a hash-verified upstream file; the\nlicence of each is listed beside it and its notice is in the repository NOTICE.")
	b.WriteString("var pinnedContexts = []PinnedContextInfo{\n")
	for _, pc := range pinned {
		fmt.Fprintf(&b, "\t{URL: %q, File: %q, SHA256: %q, Licence: %q},\n", pc.URL, pc.File, in[pc.Input].SHA256, pc.Licence)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

func writeHeader(b *bytes.Buffer, pkg, notice string) {
	b.WriteString("// Copyright The DeviceChain Authors\n// SPDX-License-Identifier: Apache-2.0\n\n")
	b.WriteString("// Code generated by backend/tools/vocabgen from pinned upstream files. DO NOT EDIT.\n//\n")
	for _, line := range strings.Split(notice, "\n") {
		if line == "" {
			b.WriteString("//\n")
		} else {
			b.WriteString("// " + line + "\n")
		}
	}
	b.WriteString("\npackage " + pkg + "\n\n")
}
