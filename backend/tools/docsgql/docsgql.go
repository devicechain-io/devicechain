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
// ATTRIBUTION. A block is attributed to a schema in one of two ways, both visible to a
// writer:
//
//   - by default it must validate against AT LEAST ONE served schema (every area's tenant
//     schema and the identity-plane admin/settings schemas); a block no schema accepts
//     fails, and the report names the schema it came closest to.
//   - a leading comment pins it: `# schema: device-management` (tenant mount) or
//     `# schema: user-management/admin` (mounts: graphql, admin, settings). A pin naming
//     an area or mount that is not served is itself a failure, never a skip.
//
// SKIPS. A block that is not an executable document is not validated, and every one is
// listed in the report with its reason and location: an exemption nothing reports is a
// hole. The reasons are "sdl" (type definitions) and "fragment" (a field selection or a
// field signature, which is no document on its own).
//
// RUN WITH -count=1 AFTER TOUCHING A SCHEMA OR A DOC: both live outside this module and
// Go's test cache does not track them.
package main

import (
	"fmt"
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
	File string // as walked
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

// Report is the outcome of a run.
type Report struct {
	Checked  int
	Failures []Failure
	Skips    []Skip
}

var fenceRe = regexp.MustCompile("^(\\s*)(`{3,}|~{3,})\\s*(\\S*)")

// ExtractBlocks returns every fenced block whose info string is graphql or gql.
func ExtractBlocks(file string, data []byte) []Block {
	var out []Block
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
		if lang == "graphql" || lang == "gql" {
			out = append(out, Block{File: file, Line: start + 1, Text: strings.Join(body, "\n")})
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

// headOf returns the first significant token of a block (comments and blank lines
// removed) and the pin, if a leading comment line carries one.
func headOf(text string) (head, pin string) {
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			if m := pinRe.FindStringSubmatch(t); m != nil && pin == "" {
				pin = m[1]
			}
			continue
		}
		if strings.HasPrefix(t, "{") {
			return "{", pin
		}
		f := strings.FieldsFunc(t, func(r rune) bool { return strings.ContainsRune(" \t({@", r) })
		if len(f) > 0 {
			return f[0], pin
		}
		return t, pin
	}
	return "", pin
}

var sdlHeads = map[string]bool{"type": true, "input": true, "enum": true, "interface": true,
	"union": true, "scalar": true, "extend": true, "schema": true, "directive": true}
var opHeads = map[string]bool{"query": true, "mutation": true, "subscription": true,
	"fragment": true, "{": true}

// validate runs the served validator over text. The one rule dropped is
// VariablesOfCorrectType: graphql-go raises it for a required variable given no value,
// which is always the case here. It is the ONLY rule filtered (undeclared variables,
// unknown fields and mistyped arguments all survive).
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
			loc = fmt.Sprintf(" (block line %d)", e.Locations[0].Line)
		}
		parts = append(parts, fmt.Sprintf("%s [%s]%s", e.Message, e.Rule, loc))
	}
	return strings.Join(parts, "; ")
}

// Check validates every block against the schemas.
func Check(blocks []Block, schemas []Named) Report {
	var r Report
	for _, b := range blocks {
		head, pin := headOf(b.Text)
		switch {
		case sdlHeads[head]:
			r.Skips = append(r.Skips, Skip{b, "sdl"})
			continue
		case !opHeads[head]:
			r.Skips = append(r.Skips, Skip{b, "fragment"})
			continue
		}
		cands := schemas
		if pin != "" {
			cands = nil
			for _, s := range schemas {
				if s.Name == pin {
					cands = append(cands, s)
				}
			}
			if len(cands) == 0 {
				r.Checked++
				r.Failures = append(r.Failures, Failure{b, fmt.Sprintf("`# schema: %s` names no served schema (have %s)", pin, names(schemas))})
				continue
			}
		}
		r.Checked++
		var best []*gqlerrors.QueryError
		bestName := ""
		ok := false
		for _, s := range cands {
			errs := validate(s.Schema, b.Text)
			if len(errs) == 0 {
				ok = true
				break
			}
			if bestName == "" || len(errs) < len(best) {
				best, bestName = errs, s.Name
			}
		}
		if !ok {
			r.Failures = append(r.Failures, Failure{b, fmt.Sprintf("valid against no served schema; closest is %s: %s", bestName, describe(best))})
		}
	}
	return r
}

func names(ss []Named) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name)
	}
	return strings.Join(n, ", ")
}

// Walk reads every .md and .mdx file under roots and returns their graphql blocks.
func Walk(roots ...string) ([]Block, error) {
	var out []Block
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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
			out = append(out, ExtractBlocks(filepath.ToSlash(p), data)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
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

// Print writes the report; skips are always listed.
func (r Report) Print(w *os.File) {
	for _, s := range r.Skips {
		fmt.Fprintf(w, "skipped (%s): %s:%d\n", s.Reason, s.Block.File, s.Block.Line)
	}
	for _, f := range r.Failures {
		fmt.Fprintf(w, "FAIL %s:%d: %s\n", f.Block.File, f.Block.Line, f.Detail)
	}
	fmt.Fprintf(w, "docsgql: %d validated, %d failed, %d skipped\n", r.Checked, len(r.Failures), len(r.Skips))
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	schemas, err := LoadSchemas(filepath.Join(root, "backend", "services"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	blocks, err := Walk(filepath.Join(root, "docs", "docs"), filepath.Join(root, "docs", "i18n"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if len(blocks) == 0 {
		fmt.Fprintln(os.Stderr, "docsgql: found no graphql blocks; a scan of nothing finds nothing")
		os.Exit(2)
	}
	r := Check(blocks, schemas)
	r.Print(os.Stdout)
	if len(r.Failures) > 0 {
		os.Exit(1)
	}
}
