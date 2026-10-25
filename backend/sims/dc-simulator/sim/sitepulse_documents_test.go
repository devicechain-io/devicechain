// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// THE SITEPULSE PLAYER'S GRAPHQL DOCUMENTS, READ BY THE SERVER'S OWN VALIDATOR.
//
// The player is C# in a Unity project, so nothing in CI compiles it, let alone runs it
// against a schema: its documents are strings whose only reader used to be a live
// cluster with a Unity Editor in front of it. This file reads them from the C# source,
// as written, and validates each against the schema of the area it is sent to — the
// same check the sim's own documents get above.
//
// The credential documents are validated WITH VARIABLES, built from the C# VarsJson
// writers that really produce them. That matters more than for any other document
// here: an input-object key the schema does not declare is refused by graphql-go's
// unknown-field validation (v1.10.3 and later) only when it is LOOKED AT, and only the variables carry those
// keys. A create request that said `device` where the schema says `deviceToken` would
// validate without its variables. What the validator does NOT check is a nested input
// field's scalar VALUE (a string where a Boolean belongs passes here and fails at
// execution), so the variables carry placeholder values and the test is about the key
// set and the required fields, which it does check.
//
// 🔴 The C# and the SDL both live outside this module; run with -count=1.

var sitepulsePlatformDir = filepath.Join("..", "..", "..", "..", "demos", "sitepulse-unity", "Assets", "Sitepulse", "Scripts", "Platform")

// sitepulseDocumentAreas says where each document is sent, by operation name (or, for an
// anonymous subscription, by its root field). A document found in the source and not
// listed here fails the test: the area is a fact about the call site, not something to
// guess from which schema happens to accept it.
var sitepulseDocumentAreas = map[string]string{
	"Resolve":           "device-management",
	"Creds":             "device-management",
	"CreateCred":        "device-management",
	"RotateCred":        "device-management",
	"Alarms":            "device-management",
	"Zones":             "device-management",
	"alarmStream":       "device-management",
	"Locations":         "device-state",
	"Presence":          "device-state",
	"Cancel":            "command-delivery",
	"Commands":          "command-delivery",
	"measurementStream": "event-management",
}

// Documents assembled at run time, one aliased field per device, so there is no single
// text to validate. They must still be FOUND, so an entry here that no longer names
// anything fails rather than exempting nothing.
var sitepulseDynamicDocuments = map[string]string{
	"Last":     "LastStateQuery.Build: one aliased latestMeasurements per device",
	"Snapshot": "ObserverQueries.MeasurementsSnapshotQuery: one aliased latestMeasurements per device",
}

type csDocument struct {
	file, key, text string
}

func TestSitepulseDocumentsValidateAgainstTheServedSchemas(t *testing.T) {
	docs, dynamic := sitepulseDocuments(t)

	// Floor: the three credential documents are named because they are what a wrong
	// field would break first, and silently — a device that never gets a token.
	byKey := map[string]csDocument{}
	for _, d := range docs {
		byKey[d.key] = d
	}
	for _, want := range []string{"Resolve", "Creds", "CreateCred", "RotateCred"} {
		if _, ok := byKey[want]; !ok {
			t.Fatalf("no %s document found in %s (found %v)", want, sitepulsePlatformDir, documentKeys(byKey))
		}
	}
	for name := range sitepulseDynamicDocuments {
		if !dynamic[name] {
			t.Errorf("dynamic-document exemption %q matches nothing in the source; remove it", name)
		}
	}
	for name := range dynamic {
		if _, ok := sitepulseDynamicDocuments[name]; !ok {
			t.Errorf("document %q is built at run time and is not validated; list it in sitepulseDynamicDocuments with the reason", name)
		}
	}
	seen := map[string]bool{}
	for _, d := range docs {
		seen[d.key] = true
	}
	for key := range sitepulseDocumentAreas {
		if !seen[key] {
			t.Errorf("sitepulseDocumentAreas lists %q but no document has that name; remove it", key)
		}
	}

	schemas := map[string]*graphql.Schema{}
	for _, d := range docs {
		area, ok := sitepulseDocumentAreas[d.key]
		if !ok {
			t.Errorf("%s: document %q has no area in sitepulseDocumentAreas", d.file, d.key)
			continue
		}
		s, ok := schemas[area]
		if !ok {
			s = servedSchema(t, area)
			schemas[area] = s
		}
		for _, e := range withoutMissingVariableValues(s.Validate(d.text)) {
			t.Errorf("%s: %s (sent to %s): %s [%s]", d.file, d.key, area, e.Message, e.Rule)
		}
	}
}

// The credential documents with the variables the C# writes for them. Which VarsJson
// writer feeds which document is read from the call site in DeviceCredentials.cs, and
// the writer's keys from its body in OperatorQueries.cs — so renaming a key on either
// side, or pointing a call at another writer, is what this test sees.
func TestSitepulseCredentialDocumentsValidateWithTheirVariables(t *testing.T) {
	docs, _ := sitepulseDocuments(t)
	byKey := map[string]csDocument{}
	for _, d := range docs {
		byKey[d.key] = d
	}
	creds := readPlatformFile(t, "DeviceCredentials.cs")
	writers := readPlatformFile(t, "OperatorQueries.cs")
	schema := servedSchema(t, "device-management")

	cases := []struct{ doc, constant string }{
		{"Creds", "ExistsQuery"},
		{"CreateCred", "CreateMutation"},
		{"RotateCred", "RotateMutation"},
	}
	for _, c := range cases {
		t.Run(c.doc, func(t *testing.T) {
			vars := sitepulseVariables(t, creds, writers, c.constant)
			d, ok := byKey[c.doc]
			if !ok {
				t.Fatalf("no %s document", c.doc)
			}
			if errs := schema.ValidateWithVariables(d.text, vars); len(errs) > 0 {
				t.Fatalf("%s with variables %v does not validate: %v", c.doc, vars, errs)
			}
		})
	}

	// 🔴 THE NEGATIVE CONTROL, asserting what the refusal is ABOUT: the create request
	// with the field misnamed must be refused for naming a field the input does not
	// declare. Any other error (a null variable, a parse failure) would mean the passes
	// above were produced by a validator that never read the request.
	t.Run("control/a misnamed create field is refused for that field", func(t *testing.T) {
		vars := sitepulseVariables(t, creds, writers, "CreateMutation")
		r, ok := vars["r"].(map[string]any)
		if !ok || r["deviceToken"] == nil {
			t.Fatalf("the create variables carry no r.deviceToken to misname: %v", vars)
		}
		r["device"] = r["deviceToken"]
		delete(r, "deviceToken")
		errs := schema.ValidateWithVariables(byKey["CreateCred"].text, vars)
		var joined strings.Builder
		for _, e := range errs {
			joined.WriteString(e.Message + "\n")
		}
		if !strings.Contains(joined.String(), `"device"`) || !strings.Contains(joined.String(), "DeviceCredentialCreateRequest") {
			t.Fatalf("want a refusal naming field \"device\" on DeviceCredentialCreateRequest, got:\n%s", joined.String())
		}
	})
}

// sitepulseVariables returns the variables the C# sends with the document held in the
// named constant: it finds `q(<constant>, VarsJson.<Writer>(...))` and interprets the
// writer's body.
func sitepulseVariables(t *testing.T, callSites, writers, constant string) map[string]any {
	t.Helper()
	call := regexp.MustCompile(`q\(\s*` + constant + `\s*,\s*VarsJson\.(\w+)\(([^)]*)\)`).FindStringSubmatch(callSites)
	if call == nil {
		t.Fatalf("no q(%s, VarsJson.<writer>(...)) call in DeviceCredentials.cs", constant)
	}
	writer, args := call[1], call[2]
	if writer == "StringList" {
		// StringList is generic over its variable name, which the call site supplies.
		name := regexp.MustCompile(`^\s*"(\w+)"`).FindStringSubmatch(args)
		if name == nil {
			t.Fatalf("StringList call for %s does not name its variable with a literal: %q", constant, args)
		}
		return map[string]any{name[1]: []any{"dev-1-sitepulse"}}
	}
	return interpretVarsWriter(t, writers, writer)
}

// interpretVarsWriter turns a VarsJson writer's body — straight-line Utf8JsonWriter
// calls, nothing else — into the variables map it produces, with placeholder values of
// the written types. A body it cannot follow (a loop, a helper call) fails rather than
// being approximated.
func interpretVarsWriter(t *testing.T, src, method string) map[string]any {
	t.Helper()
	start := regexp.MustCompile(`public static string ` + method + `\([^)]*\)\s*=>\s*Write\(w\s*=>\s*\{`).FindStringIndex(src)
	if start == nil {
		t.Fatalf("no VarsJson.%s(...) => Write(w => { ... }) in OperatorQueries.cs", method)
	}
	end := strings.Index(src[start[1]:], "});")
	if end < 0 {
		t.Fatalf("VarsJson.%s: body not terminated", method)
	}
	body := src[start[1] : start[1]+end]

	call := regexp.MustCompile(`^w\.(\w+)\((.*)\);$`)
	root := map[string]any{}
	stack := []map[string]any{root}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := call.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("VarsJson.%s: cannot interpret %q; extend interpretVarsWriter rather than skip it", method, line)
		}
		op, args := m[1], m[2]
		name := ""
		if n := regexp.MustCompile(`^"(\w+)"`).FindStringSubmatch(args); n != nil {
			name = n[1]
		}
		top := stack[len(stack)-1]
		switch op {
		case "WriteStartObject":
			child := map[string]any{}
			top[name] = child
			stack = append(stack, child)
		case "WriteEndObject":
			if len(stack) == 1 {
				t.Fatalf("VarsJson.%s: unbalanced WriteEndObject", method)
			}
			stack = stack[:len(stack)-1]
		case "WriteString":
			v := "x"
			if strings.Contains(args, "CredentialResolver.AccessToken") {
				v = "ACCESS_TOKEN"
			}
			top[name] = v
		case "WriteBoolean":
			top[name] = strings.Contains(args, "true")
		case "WriteNumber":
			top[name] = float64(1)
		default:
			t.Fatalf("VarsJson.%s: cannot interpret %s; extend interpretVarsWriter rather than skip it", method, op)
		}
		if name == "" && op != "WriteEndObject" {
			t.Fatalf("VarsJson.%s: %s with a non-literal name: %q", method, op, line)
		}
	}
	if len(stack) != 1 {
		t.Fatalf("VarsJson.%s: unbalanced objects", method)
	}
	if len(root) == 0 {
		t.Fatalf("VarsJson.%s wrote no variables", method)
	}
	// round-trip through JSON so the shapes are exactly what a decoded request carries
	raw, _ := json.Marshal(root)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// withoutMissingVariableValues drops the one error that validating with no variable
// values always raises — a required variable given none — and nothing else.
func withoutMissingVariableValues(errs []*gqlerrors.QueryError) []*gqlerrors.QueryError {
	var kept []*gqlerrors.QueryError
	for _, e := range errs {
		if e.Rule != "VariablesOfCorrectType" {
			kept = append(kept, e)
		}
	}
	return kept
}

func readPlatformFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sitepulsePlatformDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sitepulseDocuments extracts every GraphQL document from the Platform C#: each run of
// string literals and same-file `const string`s joined by `+` whose value starts with
// query, mutation or subscription. A run that reaches a non-constant is a document built
// at run time and is returned by operation name in the second result.
func sitepulseDocuments(t *testing.T) ([]csDocument, map[string]bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(sitepulsePlatformDir, "*.cs"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no C# under %s (%v); a scan of nothing validates nothing", sitepulsePlatformDir, err)
	}
	var docs []csDocument
	dynamic := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		toks := csTokens(t, filepath.Base(f), string(b))
		consts := csConstants(toks)
		for _, run := range csRuns(toks) {
			text, complete := "", true
			for _, tk := range run {
				switch {
				case tk.kind == tkPlus:
				case tk.kind == tkString:
					text += tk.text
				case consts[tk.text] != "":
					text += consts[tk.text]
				default:
					complete = false
				}
				if !complete {
					break
				}
			}
			kw := strings.SplitN(strings.TrimSpace(text), " ", 2)[0]
			kw = strings.SplitN(kw, "(", 2)[0]
			if kw != "query" && kw != "mutation" && kw != "subscription" {
				continue
			}
			key := documentKey(text)
			if !complete || !strings.HasSuffix(strings.TrimSpace(text), "}") {
				dynamic[key] = true
				continue
			}
			docs = append(docs, csDocument{file: filepath.Base(f), key: key, text: text})
		}
	}
	return docs, dynamic
}

// documentKey is the operation name, or for an anonymous operation its first root field.
func documentKey(text string) string {
	rest := strings.TrimSpace(text)
	for _, kw := range []string{"query", "mutation", "subscription"} {
		if strings.HasPrefix(rest, kw) {
			rest = strings.TrimSpace(rest[len(kw):])
			break
		}
	}
	if strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "(") {
		rest = rest[strings.Index(rest, "{")+1:]
	}
	return strings.FieldsFunc(rest, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })[0]
}

const (
	tkString = iota
	tkIdent
	tkPlus
	tkOther
)

type csToken struct {
	kind int
	text string
}

// csTokens is a tokenizer for exactly what this scan needs: string literals (regular and
// verbatim, with escapes decoded), identifiers (dotted names joined), `+`, and everything
// else as one opaque token per character. Comments, char literals and interpolated
// strings are skipped, so a quote inside them cannot start a phantom literal.
func csTokens(t *testing.T, file, src string) []csToken {
	var out []csToken
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == '/' && i+1 < len(r) && r[i+1] == '/':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(r) && r[i+1] == '*':
			j := strings.Index(string(r[i+2:]), "*/")
			if j < 0 {
				t.Fatalf("%s: unterminated block comment", file)
			}
			i += 2 + len([]rune(string(r[i+2:])[:j])) + 2
		case c == '\'':
			i++
			for i < len(r) && r[i] != '\'' {
				if r[i] == '\\' {
					i++
				}
				i++
			}
			i++
			out = append(out, csToken{tkOther, "'"})
		case c == '$' || (c == '@' && i+1 < len(r) && r[i+1] == '$'):
			// an interpolated string: skip it whole (it may nest quotes inside holes)
			for i < len(r) && r[i] != '"' {
				i++
			}
			i++
			depth := 0
			for i < len(r) {
				if r[i] == '\\' && depth == 0 {
					i += 2
					continue
				}
				if r[i] == '{' {
					depth++
				} else if r[i] == '}' && depth > 0 {
					depth--
				} else if r[i] == '"' && depth == 0 {
					break
				}
				i++
			}
			i++
			out = append(out, csToken{tkOther, "$\""})
		case c == '@' && i+1 < len(r) && r[i+1] == '"':
			var sb strings.Builder
			i += 2
			for i < len(r) {
				if r[i] == '"' {
					if i+1 < len(r) && r[i+1] == '"' {
						sb.WriteRune('"')
						i += 2
						continue
					}
					break
				}
				sb.WriteRune(r[i])
				i++
			}
			i++
			out = append(out, csToken{tkString, sb.String()})
		case c == '"':
			var sb strings.Builder
			i++
			for i < len(r) && r[i] != '"' {
				if r[i] == '\\' && i+1 < len(r) {
					switch r[i+1] {
					case 'n':
						sb.WriteRune('\n')
					case 't':
						sb.WriteRune('\t')
					default:
						sb.WriteRune(r[i+1])
					}
					i += 2
					continue
				}
				sb.WriteRune(r[i])
				i++
			}
			i++
			out = append(out, csToken{tkString, sb.String()})
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(r) && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '_' || r[j] == '.') {
				j++
			}
			out = append(out, csToken{tkIdent, string(r[i:j])})
			i = j
		case c == '+':
			out = append(out, csToken{tkPlus, "+"})
			i++
		case unicode.IsSpace(c):
			i++
		default:
			out = append(out, csToken{tkOther, string(c)})
			i++
		}
	}
	return out
}

// csRuns returns each maximal `a + b + c` run whose operands are strings or identifiers
// and which contains at least one string literal.
func csRuns(toks []csToken) [][]csToken {
	var runs [][]csToken
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != tkString {
			continue
		}
		// extend left over `ident +` / `"..." +`
		s := i
		for s >= 2 && toks[s-1].kind == tkPlus && (toks[s-2].kind == tkString || toks[s-2].kind == tkIdent) {
			s -= 2
		}
		e := i
		for e+2 < len(toks) && toks[e+1].kind == tkPlus && (toks[e+2].kind == tkString || toks[e+2].kind == tkIdent) {
			e += 2
		}
		runs = append(runs, toks[s:e+1])
		i = e
	}
	return runs
}

// csConstants evaluates `const string NAME = <run>;` declarations, resolving references
// among them.
func csConstants(toks []csToken) map[string]string {
	type decl struct {
		name string
		run  []csToken
	}
	var decls []decl
	for i := 0; i+3 < len(toks); i++ {
		if toks[i].text == "const" && toks[i+1].text == "string" && toks[i+2].kind == tkIdent && toks[i+3].text == "=" {
			j := i + 4
			var run []csToken
			for j < len(toks) && toks[j].text != ";" {
				if toks[j].kind != tkPlus {
					run = append(run, toks[j])
				}
				j++
			}
			decls = append(decls, decl{toks[i+2].text, run})
		}
	}
	out := map[string]string{}
	for pass := 0; pass <= len(decls); pass++ {
		for _, d := range decls {
			if _, done := out[d.name]; done {
				continue
			}
			var sb strings.Builder
			ok := true
			for _, tk := range d.run {
				switch {
				case tk.kind == tkString:
					sb.WriteString(tk.text)
				case tk.kind == tkIdent && out[tk.text] != "":
					sb.WriteString(out[tk.text])
				default:
					ok = false
				}
			}
			if ok {
				out[d.name] = sb.String()
			}
		}
	}
	return out
}

func documentKeys(m map[string]csDocument) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
