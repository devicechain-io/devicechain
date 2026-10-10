// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The chart's values reference: every property of values.schema.json carries a
// public description, and the chart README holds a Key / Type / Default /
// Description table generated from the schema and values.yaml.
//
// WHY THE DESCRIPTIONS LIVE IN THE SCHEMA. values.yaml's comments are maintainer
// prose (decision numbers, incident history); the schema's descriptions are
// written for the reader of the README, which Artifact Hub renders. The table is
// generated so the two cannot drift, and drift-tested so a property added to the
// schema without regenerating fails here rather than shipping an unlisted key.
//
// Regenerate after editing the schema or values.yaml:
//
//	go test -run TestChartReadmeValuesTableIsCurrent -update-values-doc
const (
	chartDir        = "helm/devicechain"
	schemaFile      = chartDir + "/values.schema.json"
	valuesFile      = chartDir + "/values.yaml"
	readmeFile      = chartDir + "/README.md"
	allowlistFile   = "testdata/values-undescribed.txt"
	tableBeginMark  = "<!-- values:begin -->"
	tableEndMark    = "<!-- values:end -->"
	maxUndescribed  = 0 // the ratchet: lower it as the allowlist shrinks, never raise it
	maxDefaultWidth = 60
)

var updateValuesDoc = flag.Bool("update-values-doc", false, "rewrite the chart README values table")

// adrRef matches the private decision-record citations that must never reach a
// published surface (the README is rendered on Artifact Hub).
var adrRef = regexp.MustCompile(`ADR-\d+`)

// valueRow is one schema property.
type valueRow struct {
	Path        string // display key, e.g. functionalAreas.<area>.replicas
	Key         string // allowlist key, e.g. functionalAreas.*.replicas
	Type        string
	Default     string
	Description string
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

// mapGet returns the value node for key in a YAML mapping node, or nil.
func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// schemaType renders the schema's "type" (a string or a list of strings).
func schemaType(n *yaml.Node) string {
	t := mapGet(n, "type")
	switch {
	case t == nil:
		return ""
	case t.Kind == yaml.ScalarNode:
		return t.Value
	}
	var parts []string
	for _, c := range t.Content {
		parts = append(parts, c.Value)
	}
	return strings.Join(parts, " or ")
}

// parseDoc parses a YAML/JSON document into its root mapping node. The schema is
// read through the YAML parser deliberately: it is a JSON superset, and unlike
// encoding/json into a map it preserves the order the schema is written in, which
// is the order the table is read in.
func parseDoc(src []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("not a mapping document")
	}
	return doc.Content[0], nil
}

// collectRows walks the schema in written order. Per-area settings sit under
// additionalProperties (the key is the area name) and array elements under items;
// they are shown as <area> and [] respectively.
func collectRows(schema, values *yaml.Node) []valueRow {
	var rows []valueRow
	var walk func(n *yaml.Node, disp, key string, val *yaml.Node, wild bool)
	walk = func(n *yaml.Node, disp, key string, val *yaml.Node, wild bool) {
		props := mapGet(n, "properties")
		if props != nil {
			for i := 0; i+1 < len(props.Content); i += 2 {
				name, child := props.Content[i].Value, props.Content[i+1]
				d, k := join(disp, name), join(key, name)
				cv := mapGet(val, name)
				row := valueRow{Path: d, Key: k, Type: schemaType(child)}
				if dn := mapGet(child, "description"); dn != nil {
					row.Description = dn.Value
				}
				row.Default = renderDefault(child, cv, wild)
				rows = append(rows, row)
				walk(child, d, k, cv, wild)
			}
		}
		if ap := mapGet(n, "additionalProperties"); ap != nil && ap.Kind == yaml.MappingNode && mapGet(ap, "properties") != nil {
			walk(ap, join(disp, "<area>"), join(key, "*"), nil, true)
		}
		if it := mapGet(n, "items"); it != nil && it.Kind == yaml.MappingNode && mapGet(it, "properties") != nil {
			walk(it, disp+"[]", key+"[]", nil, true)
		}
	}
	walk(schema, "", "", values, false)
	return rows
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

// renderDefault shows a property's default from values.yaml. A group whose members
// are listed on their own rows shows "-", as do per-area and per-element rows,
// which have no single default. A long default points at values.yaml instead of
// flooding the table.
func renderDefault(schemaNode, val *yaml.Node, wild bool) string {
	if wild || val == nil || mapGet(schemaNode, "properties") != nil {
		return "-"
	}
	var v any
	if err := val.Decode(&v); err != nil {
		return "-"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "-"
	}
	s := string(b)
	if len(s) > maxDefaultWidth {
		return "see values.yaml"
	}
	return "`" + s + "`"
}

func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	// A bare <id> or <area> would be read as an HTML tag and vanish on Artifact Hub.
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// renderTable builds the generated block (without the markers).
func renderTable(schemaSrc, valuesSrc []byte) (string, error) {
	schema, err := parseDoc(schemaSrc)
	if err != nil {
		return "", fmt.Errorf("schema: %w", err)
	}
	values, err := parseDoc(valuesSrc)
	if err != nil {
		return "", fmt.Errorf("values: %w", err)
	}
	var b strings.Builder
	b.WriteString("| Key | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	for _, r := range collectRows(schema, values) {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", r.Path, cell(r.Type), r.Default, cell(r.Description))
	}
	return b.String(), nil
}

// spliceTable replaces what sits between the markers.
func spliceTable(readme, table string) (string, error) {
	bi := strings.Index(readme, tableBeginMark)
	ei := strings.Index(readme, tableEndMark)
	if bi < 0 || ei < 0 || ei < bi {
		return "", fmt.Errorf("README is missing the %s / %s markers", tableBeginMark, tableEndMark)
	}
	return readme[:bi+len(tableBeginMark)] + "\n" + table + readme[ei:], nil
}

// undescribed lists the allowlist keys of every property with no description.
func undescribed(schemaSrc, valuesSrc []byte) ([]string, error) {
	schema, err := parseDoc(schemaSrc)
	if err != nil {
		return nil, err
	}
	values, err := parseDoc(valuesSrc)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range collectRows(schema, values) {
		if strings.TrimSpace(r.Description) == "" {
			out = append(out, r.Key)
		}
	}
	return out, nil
}

func loadAllowlist(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(string(readFile(t, allowlistFile)), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// checkDescribed is the guard: every undescribed property must be on the
// allowlist, and every allowlist entry must still be an undescribed property.
func checkDescribed(missing, allow []string) []string {
	var errs []string
	in := map[string]bool{}
	for _, a := range allow {
		in[a] = true
	}
	have := map[string]bool{}
	for _, m := range missing {
		have[m] = true
		if !in[m] {
			errs = append(errs, fmt.Sprintf("%s has no description in values.schema.json (describe it; do not add it to the allowlist)", m))
		}
	}
	for _, a := range allow {
		if !have[a] {
			errs = append(errs, fmt.Sprintf("%s is on the allowlist but is now described or gone: delete the line", a))
		}
	}
	if len(allow) > maxUndescribed {
		errs = append(errs, fmt.Sprintf("allowlist has %d entries, ceiling is %d: it may only shrink", len(allow), maxUndescribed))
	}
	sort.Strings(errs)
	return errs
}

func TestChartValuesAreDescribed(t *testing.T) {
	missing, err := undescribed(readFile(t, schemaFile), readFile(t, valuesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range checkDescribed(missing, loadAllowlist(t)) {
		t.Error(e)
	}
}

// TestChartDescriptionsHaveNoDecisionRefs: the descriptions become README text.
func TestChartDescriptionsHaveNoDecisionRefs(t *testing.T) {
	schema, err := parseDoc(readFile(t, schemaFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range collectRows(schema, nil) {
		if adrRef.MatchString(r.Description) {
			t.Errorf("%s: description cites a decision record; say the thing itself", r.Key)
		}
	}
	if d := mapGet(schema, "description"); d != nil && adrRef.MatchString(d.Value) {
		t.Error("schema root description cites a decision record")
	}
}

func TestChartReadmeValuesTableIsCurrent(t *testing.T) {
	table, err := renderTable(readFile(t, schemaFile), readFile(t, valuesFile))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(readFile(t, readmeFile))
	want, err := spliceTable(readme, table)
	if err != nil {
		t.Fatal(err)
	}
	if *updateValuesDoc {
		if err := os.WriteFile(readmeFile, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if readme != want {
		t.Fatalf("%s values table is stale; regenerate with:\n  go test -run TestChartReadmeValuesTableIsCurrent -update-values-doc", readmeFile)
	}
}

// --- the guard must be able to fail --------------------------------------

const plantedSchema = `{"type":"object","properties":{
  "alpha":{"type":"string","description":"The alpha | value."},
  "beta":{"type":"object","description":"Group.","properties":{"gamma":{"type":"integer","description":"Gamma."}}}
}}`
const plantedValues = "alpha: x\nbeta:\n  gamma: 3\n"

func TestValuesDocTableRendersDefaultsAndEscapes(t *testing.T) {
	table, err := renderTable([]byte(plantedSchema), []byte(plantedValues))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| `alpha` | string | `\"x\"` | The alpha \\| value. |",
		"| `beta` | object | - | Group. |",
		"| `beta.gamma` | integer | `3` | Gamma. |",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
}

// A property planted in the schema but absent from the README must make the
// drift comparison differ.
func TestValuesDocDriftIsDetected(t *testing.T) {
	before, _ := renderTable([]byte(plantedSchema), []byte(plantedValues))
	readme, err := spliceTable("x\n"+tableBeginMark+"\n"+tableEndMark+"\ny\n", before)
	if err != nil {
		t.Fatal(err)
	}
	planted := strings.Replace(plantedSchema, `"alpha"`, `"planted":{"type":"string","description":"New."},"alpha"`, 1)
	after, err := renderTable([]byte(planted), []byte(plantedValues))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := spliceTable(readme, after)
	if readme == want {
		t.Fatal("a planted property did not change the generated table")
	}
	if !strings.Contains(want, "`planted`") {
		t.Fatal("planted property missing from regenerated table")
	}
}

func TestValuesDocMissingMarkersFail(t *testing.T) {
	if _, err := spliceTable("no markers", "t"); err == nil {
		t.Fatal("expected an error for a README without markers")
	}
}

func TestValuesDocUndescribedGuardFires(t *testing.T) {
	planted := strings.Replace(plantedSchema, `"description":"Gamma."`, `"description":" "`, 1)
	missing, err := undescribed([]byte(planted), []byte(plantedValues))
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "beta.gamma" {
		t.Fatalf("missing = %v, want [beta.gamma]", missing)
	}
	if errs := checkDescribed(missing, nil); len(errs) == 0 {
		t.Error("an undescribed property off the allowlist must fail")
	}
	if errs := checkDescribed(nil, []string{"beta.gamma"}); len(errs) == 0 {
		t.Error("a stale allowlist entry must fail")
	}
}
