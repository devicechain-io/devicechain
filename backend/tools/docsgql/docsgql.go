// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command docsgql checks that every GraphQL example in the published documentation is a
// valid document against the schemas the services serve.
//
// WHY. A docs example is a STRING no compiler reads, validated by a schema in another
// module. When a field is renamed or an argument changes shape, the page that teaches it
// keeps building and keeps being wrong, and a reader who pastes it gets
// `Cannot query field ...` from the very product the page describes. This asks the schema,
// with the SERVER'S OWN VALIDATOR (the same library and the same fork the services parse
// with), the way backend/services/mcp and backend/sims/dc-simulator already do for the
// documents they send.
//
// TWO KINDS OF EXAMPLE.
//
//   - fenced graphql blocks, validated as bare documents (no variable VALUES exist, so the
//     one rule dropped is VariablesOfCorrectType);
//   - curl payloads, `-d '{"query":...,"variables":...}'` inside shell fences. These are
//     the only examples that carry variables, which is the case the pinned graphql-go
//     fork exists for, so each is validated WITH its variables and nothing filtered, and
//     is attributed to the schema its URL addresses.
//
// ATTRIBUTION of a graphql block, both ways visible to a writer:
//
//   - by default it must validate against AT LEAST ONE served schema that declares the
//     root type for every operation kind the block uses (graphql-go skips validation of
//     an operation whose root type is missing, so a schema without a subscription root
//     would accept anything under `subscription`); the report names every schema it
//     matched;
//   - a leading comment pins it: `# schema: device-management` (tenant mount) or
//     `# schema: user-management/admin` (mounts: graphql, admin, settings). A pin naming
//     a schema that is not served fails.
//
// SKIPS. A block that is no executable document (SDL, a list of field signatures, bare
// field selections) is not validated, and it must be on allowedSkips (file, reason, start
// of text). Every skip is printed, an unlisted skip is a failure, and an allowlist entry
// nothing matches is a failure: a check that skips silently reports CLEAN over typos.
// Anything that is neither an operation, a definition nor one of those two shapes fails.
//
// COVERAGE. The English, es and zh-CN trees must carry the same number of blocks and
// payloads, so a dropped locale cannot validate less and still pass.
//
// RUN WITH -count=1 AFTER TOUCHING A SCHEMA OR A DOC: both live outside this module and
// Go's test cache does not track them.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
)

// Block is one fenced graphql code block.
type Block struct {
	File string // relative to the repo root
	Line int    // 1-based line of the opening fence
	Text string // the block body
}

// Named is one served schema, addressed as "area" (tenant mount) or "area/mount-name".
type Named struct {
	Name   string
	Area   string
	Mount  string
	Schema *graphql.Schema
}

// Failure is a block that does not validate.
type Failure struct {
	Block  Block
	Detail string
}

// Skip is a block that was deliberately not validated.
type Skip struct {
	Block  Block
	Reason string
}

// Match is the schemas a block or payload validated against.
type Match struct {
	Block   Block
	Schemas []string
}

// Report is the outcome of a run.
type Report struct {
	Checked  int // graphql blocks validated
	Payloads int // curl payloads validated
	Matches  []Match
	Failures []Failure
	Skips    []Skip
}

// Fence is one fenced code block of any language.
type Fence struct {
	File string
	Line int // 1-based line of the opening fence
	Lang string
	Body string
}

var fenceRe = regexp.MustCompile("^(\\s*)(`{3,}|~{3,})\\s*([A-Za-z0-9_+-]*)")

// ExtractFences returns every fenced block. The info string is read up to the first
// character that cannot belong to a language name, so ```graphql{1} and
// ```graphql title="x" are both "graphql".
func ExtractFences(file string, data []byte) []Fence {
	var out []Fence
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		m := fenceRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		fence, lang := m[2], strings.ToLower(m[3])
		start := i
		var body []string
		for i++; i < len(lines); i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, fence) && strings.Trim(t, string(fence[0])) == "" {
				break
			}
			body = append(body, lines[i])
		}
		out = append(out, Fence{File: file, Line: start + 1, Lang: lang, Body: strings.Join(body, "\n")})
	}
	return out
}

// ExtractBlocks returns every fenced block whose language is graphql or gql.
func ExtractBlocks(file string, data []byte) []Block {
	var out []Block
	for _, f := range ExtractFences(file, data) {
		if f.Lang == "graphql" || f.Lang == "gql" {
			out = append(out, Block{File: f.File, Line: f.Line, Text: f.Body})
		}
	}
	return out
}

// Payload is a GraphQL request body found in a curl example.
type Payload struct {
	File      string
	Line      int
	Endpoint  string // the URL as written
	Schema    string // the schema name its path addresses, "" if unparseable
	Query     string
	Variables map[string]any
	Err       string // set when the payload could not be extracted
}

var (
	urlRe      = regexp.MustCompile(`https?://\S+`)
	endpointRe = regexp.MustCompile(`/api/([a-z][a-z0-9-]*)/((?:admin/|settings/)?graphql)\b`)
)

// ExtractPayloads returns every `-d '{"query":...}'` payload in shell-ish fences.
func ExtractPayloads(file string, data []byte) []Payload {
	var out []Payload
	for _, f := range ExtractFences(file, data) {
		switch f.Lang {
		case "bash", "sh", "shell", "zsh", "console", "curl":
		default:
			continue
		}
		body := f.Body
		url := ""
		for pos := 0; pos < len(body); {
			rest := body[pos:]
			d := strings.Index(rest, "-d '")
			u := urlRe.FindStringIndex(rest)
			if d < 0 && u == nil {
				break
			}
			if u != nil && (d < 0 || u[0] < d) {
				url = strings.TrimRight(rest[u[0]:u[1]], `\'"`)
				pos += u[1]
				continue
			}
			open := pos + d + len("-d '")
			end := strings.Index(body[open:], "'")
			line := f.Line + 1 + strings.Count(body[:pos+d], "\n")
			if end < 0 {
				out = append(out, Payload{File: file, Line: line, Endpoint: url, Err: "unterminated -d '...' payload"})
				break
			}
			raw := body[open : open+end]
			pos = open + end + 1
			if !strings.Contains(raw, `"query"`) {
				continue
			}
			p := Payload{File: file, Line: line, Endpoint: url}
			if m := endpointRe.FindStringSubmatch(url); m != nil {
				p.Schema = m[1]
				if m[2] != "graphql" {
					p.Schema += "/" + strings.TrimSuffix(strings.TrimSuffix(m[2], "graphql"), "/")
				}
			}
			var req struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				p.Err = "payload is not a valid GraphQL request body: " + err.Error()
			}
			p.Query, p.Variables = req.Query, req.Variables
			out = append(out, p)
		}
	}
	return out
}

var pinRe = regexp.MustCompile(`^#\s*schema:\s*(\S+)\s*$`)

var mountNames = map[string]string{
	"graphql":  schemaplane.MountTenant,
	"admin":    schemaplane.MountAdmin,
	"settings": schemaplane.MountSettings,
}

func pinOf(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if m := pinRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return m[1]
		}
	}
	return ""
}

// stripComments removes # comments outside strings, line by line.
func stripComments(text string) string {
	var b strings.Builder
	inBlock := false
	for _, l := range strings.Split(text, "\n") {
		inStr := false
		for i := 0; i < len(l); i++ {
			if strings.HasPrefix(l[i:], `"""`) {
				inBlock = !inBlock
				b.WriteString(`"""`)
				i += 2
				continue
			}
			c := l[i]
			if !inBlock {
				if c == '\\' && inStr && i+1 < len(l) {
					b.WriteByte(c)
					i++
					b.WriteByte(l[i])
					continue
				}
				if c == '"' {
					inStr = !inStr
				}
				if c == '#' && !inStr {
					break
				}
			}
			b.WriteByte(c)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

var sdlHeads = map[string]bool{"type": true, "input": true, "enum": true, "interface": true,
	"union": true, "scalar": true, "extend": true, "schema": true, "directive": true}
var opHeads = map[string]bool{"query": true, "mutation": true, "subscription": true, "fragment": true}

// segment is one top-level definition of a block.
type segment struct {
	head string // query, mutation, type, ... or "{" for an anonymous query
	text string
}

// splitTopLevel splits comment-stripped text into top-level definitions. A definition
// starts at a line, at nesting depth zero, whose first word is a definition keyword (or
// which begins with `{`). Text before the first one is returned as junk.
func splitTopLevel(text string) (junk string, segs []segment) {
	depth := 0
	inBlock := false
	started := false
	var junkB strings.Builder
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if depth == 0 && !inBlock && t != "" {
			head := ""
			if strings.HasPrefix(t, "{") {
				head = "{"
			} else if f := strings.FieldsFunc(t, func(r rune) bool { return strings.ContainsRune(" \t({@=:", r) }); len(f) > 0 && (opHeads[f[0]] || sdlHeads[f[0]]) {
				head = f[0]
			}
			if head != "" {
				segs = append(segs, segment{head: head})
				started = true
			}
		}
		if started {
			segs[len(segs)-1].text += l + "\n"
		} else {
			junkB.WriteString(l + "\n")
		}
		inStr := false
		for i := 0; i < len(l); i++ {
			if strings.HasPrefix(l[i:], `"""`) {
				inBlock = !inBlock
				i += 2
				continue
			}
			if inBlock {
				continue
			}
			switch c := l[i]; {
			case c == '\\' && inStr:
				i++
			case c == '"':
				inStr = !inStr
			case inStr:
			case c == '{' || c == '(' || c == '[':
				depth++
			case c == '}' || c == ')' || c == ']':
				depth--
			}
		}
	}
	return strings.TrimSpace(junkB.String()), segs
}

var (
	signatureLineRe = regexp.MustCompile(`^[A-Za-z_]\w*\s*\([^)]*\)\s*:\s*[\[\]A-Za-z_!]+$`)
	selectionRe     = regexp.MustCompile(`^[A-Za-z_]\w*\s*(\([^{}]*\))?\s*\{[\s\S]*\}$`)
)

// fragmentShape names the two shapes the docs use for a block that is deliberately no
// executable document: a list of field signatures, or bare field selections.
func fragmentShape(junk string) string {
	sig := true
	for _, l := range strings.Split(junk, "\n") {
		if t := strings.TrimSpace(l); t != "" && !signatureLineRe.MatchString(t) {
			sig = false
		}
	}
	if sig {
		return "signature"
	}
	if selectionRe.MatchString(junk) {
		return "selection"
	}
	return ""
}

// validate runs the served validator over text. The one rule dropped is
// VariablesOfCorrectType: graphql-go raises it for a required variable given no value,
// which is always the case for a bare block. It is the ONLY rule filtered (undeclared
// variables, unknown fields and mistyped arguments all survive).
func validate(s *graphql.Schema, text string) []*gqlerrors.QueryError {
	var kept []*gqlerrors.QueryError
	for _, e := range s.Validate(text) {
		if e.Rule == "VariablesOfCorrectType" {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

func describe(errs []*gqlerrors.QueryError) string {
	var parts []string
	for _, e := range errs {
		loc := ""
		if len(e.Locations) > 0 {
			loc = fmt.Sprintf(" (line %d)", e.Locations[0].Line)
		}
		parts = append(parts, fmt.Sprintf("%s [%s]%s", e.Message, e.Rule, loc))
	}
	return strings.Join(parts, "; ")
}

// hasRoot reports whether the schema declares the root type for an operation kind.
// graphql-go skips validation of an operation whose root type is missing, so a schema
// without one accepts ANYTHING under that keyword.
func hasRoot(s *graphql.Schema, kind string) bool {
	_, ok := s.AST().RootOperationTypes[kind]
	return ok
}

func kindOf(head string) string {
	switch head {
	case "mutation", "subscription":
		return head
	}
	return "query"
}

// AllowedSkip names one block that may be skipped: its file (relative to the repo root),
// the skip reason, and the start of its comment-stripped, whitespace-normalized text.
type AllowedSkip struct {
	File   string
	Reason string
	Prefix string
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (r *Report) fail(b Block, format string, a ...any) {
	r.Failures = append(r.Failures, Failure{b, fmt.Sprintf(format, a...)})
}

func (r *Report) skip(b Block, reason, text string, allow []AllowedSkip, used map[int]bool) {
	r.Skips = append(r.Skips, Skip{b, reason})
	n := normalize(text)
	for i, a := range allow {
		if a.File == b.File && a.Reason == reason && strings.HasPrefix(n, a.Prefix) {
			used[i] = true
			return
		}
	}
	r.fail(b, "block skipped as %q is not on the allowlist of skipped blocks; fix it if it is meant to be an operation, else add it to allowedSkips (starts %q)", reason, truncate(n, 60))
}

// Check validates every block and payload against the schemas. A skipped block must be
// on allow, and an allow entry nothing matched is itself a failure.
func Check(blocks []Block, payloads []Payload, schemas []Named, allow []AllowedSkip) Report {
	var r Report
	used := map[int]bool{}
	for _, b := range blocks {
		r.checkBlock(b, schemas, allow, used)
	}
	for _, p := range payloads {
		r.checkPayload(p, schemas)
	}
	for i, a := range allow {
		if !used[i] {
			r.fail(Block{File: a.File}, "stale allowlist entry: no %q block starting %q is skipped there any more", a.Reason, a.Prefix)
		}
	}
	return r
}

func (r *Report) checkBlock(b Block, schemas []Named, allow []AllowedSkip, used map[int]bool) {
	junk, segs := splitTopLevel(stripComments(b.Text))
	if junk == "" && len(segs) == 0 {
		r.Checked++
		r.fail(b, "empty block")
		return
	}
	if junk != "" {
		if len(segs) == 0 {
			if shape := fragmentShape(junk); shape != "" {
				r.skip(b, shape, junk, allow, used)
				return
			}
		}
		r.Checked++
		r.fail(b, "not recognizable as an operation, a definition or a documented fragment shape (a typo'd keyword lands here); starts %q", truncate(normalize(junk), 60))
		return
	}
	var exec []string
	kinds := map[string]bool{}
	sdl := false
	for _, s := range segs {
		if sdlHeads[s.head] {
			sdl = true
			continue
		}
		exec = append(exec, s.text)
		if s.head != "fragment" {
			kinds[kindOf(s.head)] = true
		}
	}
	if sdl {
		for _, s := range segs {
			if sdlHeads[s.head] {
				r.skip(b, "sdl", s.text, allow, used)
				break
			}
		}
	}
	if len(exec) == 0 {
		return
	}
	r.Checked++
	doc := strings.Join(exec, "\n")
	cands := schemas
	if pin := pinOf(b.Text); pin != "" {
		cands = nil
		for _, s := range schemas {
			if s.Name == pin {
				cands = append(cands, s)
			}
		}
		if len(cands) == 0 {
			r.fail(b, "`# schema: %s` names no served schema (have %s)", pin, names(schemas))
			return
		}
	}
	var withRoots []Named
	for _, s := range cands {
		ok := true
		for k := range kinds {
			ok = ok && hasRoot(s.Schema, k)
		}
		if ok {
			withRoots = append(withRoots, s)
		}
	}
	if len(withRoots) == 0 {
		var ks []string
		for k := range kinds {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		r.fail(b, "no candidate schema declares a %s root, so nothing could validate this block", strings.Join(ks, "+"))
		return
	}
	var ok []string
	var best []*gqlerrors.QueryError
	var tied []string
	for _, s := range withRoots {
		errs := validate(s.Schema, doc)
		if len(errs) == 0 {
			ok = append(ok, s.Name)
			continue
		}
		switch {
		case best == nil || len(errs) < len(best):
			best, tied = errs, []string{s.Name}
		case len(errs) == len(best):
			tied = append(tied, s.Name)
		}
	}
	if len(ok) > 0 {
		r.Matches = append(r.Matches, Match{b, ok})
		return
	}
	who := strings.Join(tied, ", ")
	if len(tied) == len(withRoots) && len(tied) > 3 {
		who = fmt.Sprintf("all %d candidate schemas", len(tied))
	}
	r.fail(b, "valid against no served schema; fewest errors (%d) from %s, which report: %s", len(best), who, describe(best))
}

func (r *Report) checkPayload(p Payload, schemas []Named) {
	b := Block{File: p.File, Line: p.Line}
	r.Payloads++
	if p.Err != "" {
		r.fail(b, "%s", p.Err)
		return
	}
	var target *Named
	for i := range schemas {
		if schemas[i].Name == p.Schema {
			target = &schemas[i]
		}
	}
	if target == nil {
		r.fail(b, "curl endpoint %q addresses no served schema (have %s)", p.Endpoint, names(schemas))
		return
	}
	// Variables are validated WITH their values and nothing is filtered: an input-object
	// field the schema does not define, reaching the server through a variable, is the
	// defect the pinned fork exists to reject.
	if errs := target.Schema.ValidateWithVariables(p.Query, p.Variables); len(errs) > 0 {
		r.fail(b, "curl payload invalid against %s: %s", target.Name, describe(errs))
		return
	}
	r.Matches = append(r.Matches, Match{b, []string{target.Name}})
}

func names(ss []Named) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name)
	}
	return strings.Join(n, ", ")
}

// Walk reads every .md and .mdx file under root/rel and returns their graphql blocks and
// curl payloads, with file names relative to root.
func Walk(root string, rels ...string) ([]Block, []Payload, error) {
	var blocks []Block
	var payloads []Payload
	for _, rel := range rels {
		err := filepath.WalkDir(filepath.Join(root, rel), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if ext := filepath.Ext(p); ext != ".md" && ext != ".mdx" {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			r, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(r)
			blocks = append(blocks, ExtractBlocks(name, data)...)
			payloads = append(payloads, ExtractPayloads(name, data)...)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return blocks, payloads, nil
}

// Locales lists the translated trees: the directories under docs/i18n.
func Locales(root string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(root, "docs", "i18n"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// locale is the docs tree a file belongs to: "en" for docs/docs, else the i18n locale.
func locale(file string) string {
	if rest, ok := strings.CutPrefix(file, "docs/i18n/"); ok {
		loc, _, _ := strings.Cut(rest, "/")
		return loc
	}
	return "en"
}

// Parity returns the per-locale [blocks, payloads] counts and an error unless every
// locale carries as many as English. A run that lost a locale (or whose translators
// dropped an example) would otherwise validate fewer examples and still report clean.
func Parity(locales []string, blocks []Block, payloads []Payload) (map[string][2]int, error) {
	counts := map[string][2]int{"en": {}}
	for _, l := range locales {
		counts[l] = [2]int{}
	}
	for _, b := range blocks {
		c := counts[locale(b.File)]
		c[0]++
		counts[locale(b.File)] = c
	}
	for _, p := range payloads {
		c := counts[locale(p.File)]
		c[1]++
		counts[locale(p.File)] = c
	}
	en := counts["en"]
	if en[0] == 0 {
		return counts, fmt.Errorf("no English graphql blocks found; a scan of nothing finds nothing")
	}
	if len(locales) == 0 {
		return counts, fmt.Errorf("no translated docs were found under docs/i18n")
	}
	var locs []string
	for l := range counts {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	for _, l := range locs {
		if counts[l] != en {
			return counts, fmt.Errorf("locale %s has %d graphql blocks and %d curl payloads, English has %d and %d", l, counts[l][0], counts[l][1], en[0], en[1])
		}
	}
	return counts, nil
}

func mountName(mount string) string {
	for n, m := range mountNames {
		if m == mount {
			return n
		}
	}
	return mount
}

// LoadSchemas parses every served schema under servicesDir/<area>/graphql.
func LoadSchemas(servicesDir string) ([]Named, error) {
	areas, err := os.ReadDir(servicesDir)
	if err != nil {
		return nil, err
	}
	var out []Named
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
			parsed, err := graphql.ParseSchema(string(sdl), nil, graphql.UseFieldResolvers())
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", sc.Path, err)
			}
			name := a.Name()
			if sc.Mount != schemaplane.MountTenant {
				name += "/" + mountName(sc.Mount)
			}
			out = append(out, Named{Name: name, Area: a.Name(), Mount: sc.Mount, Schema: parsed})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no served schema under %s; a check that parsed nothing would fail every block", servicesDir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Print writes the report: every skip, every attribution, every failure.
func (r Report) Print(w io.Writer) {
	for _, s := range r.Skips {
		fmt.Fprintf(w, "skipped (%s): %s:%d\n", s.Reason, s.Block.File, s.Block.Line)
	}
	for _, m := range r.Matches {
		who := strings.Join(m.Schemas, ", ")
		if len(m.Schemas) > 3 {
			who = fmt.Sprintf("%d schemas (%s, ...)", len(m.Schemas), m.Schemas[0])
		}
		fmt.Fprintf(w, "ok %s:%d -> %s\n", m.Block.File, m.Block.Line, who)
	}
	for _, f := range r.Failures {
		fmt.Fprintf(w, "FAIL %s:%d: %s\n", f.Block.File, f.Block.Line, f.Detail)
	}
	fmt.Fprintf(w, "docsgql: %d graphql blocks validated, %d curl payloads checked, %d failed, %d skipped\n",
		r.Checked, r.Payloads, len(r.Failures), len(r.Skips))
}

// allowedSkips is the complete set of blocks the docs may contain that are no executable
// document. Everything else is validated, and a new skip fails until it is listed here.
var allowedSkips = func() []AllowedSkip {
	var out []AllowedSkip
	for _, tree := range []string{
		"docs/docs/",
		"docs/i18n/es/docusaurus-plugin-content-docs/current/",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/",
	} {
		// The v0.18 upgrade note shows a list call before and after; "before" is the
		// retired shape, so the block is deliberately not valid against any schema.
		out = append(out, AllowedSkip{tree + "deployment/releases-and-upgrades.md", "selection", "tenantDeletions(completed: false"})
		// The rename mutations are quoted as signatures to show one contract.
		out = append(out, AllowedSkip{tree + "reference/graphql-api.md", "signature", "renameDeviceProfile(token: String!"})
	}
	return out
}()

// run is the CLI: it returns the process exit status (0 clean, 1 findings, 2 unable to run).
func run(root string, allow []AllowedSkip, stdout, stderr io.Writer) int {
	schemas, err := LoadSchemas(filepath.Join(root, "backend", "services"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	blocks, payloads, err := Walk(root, filepath.Join("docs", "docs"), filepath.Join("docs", "i18n"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	locales, err := Locales(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	counts, perr := Parity(locales, blocks, payloads)
	r := Check(blocks, payloads, schemas, allow)
	r.Print(stdout)
	fmt.Fprintf(stdout, "docsgql: per-locale [graphql blocks, curl payloads]: %v\n", counts)
	if perr != nil {
		fmt.Fprintln(stderr, "docsgql: "+perr.Error())
		return 1
	}
	if len(r.Failures) > 0 {
		return 1
	}
	return 0
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	os.Exit(run(root, allowedSkips, os.Stdout, os.Stderr))
}
