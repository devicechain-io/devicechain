// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const docsAnalyticsSource = "backend/services/event-management/model/analytics.go"

// Read the declaration, not the service module or a second copy of its schema.
// Unexpected declaration shapes fail the gate rather than silently losing columns.
func readDocsAnalytics(repo string) (map[string]map[string]bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repo, filepath.FromSlash(docsAnalyticsSource)), nil, 0)
	if err != nil {
		return nil, err
	}
	views := map[string]map[string]bool{}
	literal := func(e ast.Expr) (string, bool) {
		v, ok := e.(*ast.BasicLit)
		if !ok || v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil && s != ""
	}
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, spec := range g.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != "analyticsViews" || len(v.Values) != 1 {
				continue
			}
			list, ok := v.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("analyticsViews must be a literal projection")
			}
			for _, entry := range list.Elts {
				row, ok := entry.(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("analyticsViews entry must be a literal")
				}
				name := ""
				columns := map[string]bool{}
				for _, field := range row.Elts {
					kv, ok := field.(*ast.KeyValueExpr)
					if !ok {
						return nil, fmt.Errorf("analyticsViews fields must be keyed")
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						return nil, fmt.Errorf("analyticsViews field must be named")
					}
					switch key.Name {
					case "name":
						name, ok = literal(kv.Value)
						if !ok {
							return nil, fmt.Errorf("analyticsViews name must be literal")
						}
					case "columns":
						cols, ok := kv.Value.(*ast.CompositeLit)
						if !ok {
							return nil, fmt.Errorf("analyticsViews columns must be literal")
						}
						for _, e := range cols.Elts {
							col, ok := literal(e)
							if !ok || columns[col] {
								return nil, fmt.Errorf("analyticsViews column must be a unique literal")
							}
							columns[col] = true
						}
					}
				}
				if name == "" || len(columns) == 0 || views[name] != nil {
					return nil, fmt.Errorf("analyticsViews has missing or duplicate name/columns")
				}
				views[name] = columns
			}
		}
	}
	if len(views) == 0 {
		return nil, fmt.Errorf("analyticsViews literal declaration not found")
	}
	return views, nil
}

type docsSQLToken struct{ kind, text string }

// Only SQL single-quoted strings (including doubled quotes), bare identifiers,
// numbers and the operators below are understood. Comments, quoted identifiers,
// interpolation, casts and unfamiliar syntax stay outside the validated subset.
func docsSQLTokens(sql string) ([]docsSQLToken, bool) {
	if strings.ContainsAny(sql, "$`") || strings.Contains(sql, substPlaceholder) || strings.Contains(sql, "--") || strings.Contains(sql, "/*") {
		return nil, false
	}
	var out []docsSQLToken
	for i := 0; i < len(sql); {
		c := sql[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		start := i
		kind := "symbol"
		switch {
		case c == '\'':
			kind = "string"
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '\\' {
					return nil, false
				}
				if sql[i] == '\'' {
					i++
					if i < len(sql) && sql[i] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false
			}
		case c >= '0' && c <= '9':
			kind = "number"
			for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
				i++
			}
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_':
			kind = "id"
			for i < len(sql) && (sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] >= '0' && sql[i] <= '9' || sql[i] == '_') {
				i++
			}
		case strings.ContainsRune(",.()+-*/=<>;", rune(c)):
			i++
			if i < len(sql) && strings.Contains("<>", string(c)) && sql[i] == '=' {
				i++
			}
		default:
			return nil, false
		}
		out = append(out, docsSQLToken{kind, strings.ToLower(sql[start:i])})
	}
	return out, true
}

type docsSQLParser struct {
	tokens  []docsSQLToken
	i       int
	columns []string
}

func (p *docsSQLParser) take(s string) bool {
	if p.i < len(p.tokens) && p.tokens[p.i].text == s {
		p.i++
		return true
	}
	return false
}

func (p *docsSQLParser) identifier() string {
	if p.i == len(p.tokens) || p.tokens[p.i].kind != "id" {
		return ""
	}
	s := p.tokens[p.i].text
	// Keywords cannot disguise an unsupported clause as an alias or a column.
	if strings.Contains(" select from where order by asc desc limit as interval and or join left right inner outer full cross on group having union distinct offset null case when end over with ", " "+s+" ") {
		return ""
	}
	p.i++
	return s
}

func (p *docsSQLParser) atom() bool {
	if p.take("+") || p.take("-") {
		return p.atom()
	}
	if p.take("(") {
		return p.expr() && p.take(")")
	}
	if p.take("now") {
		return p.take("(") && p.take(")")
	}
	interval := p.take("interval")
	if p.i < len(p.tokens) && (p.tokens[p.i].kind == "string" || !interval && p.tokens[p.i].kind == "number") {
		p.i++
		return true
	}
	if interval {
		return false
	}
	if col := p.identifier(); col != "" {
		p.columns = append(p.columns, col)
		return true
	}
	return false
}

func (p *docsSQLParser) expr() bool {
	if !p.atom() {
		return false
	}
	for p.i < len(p.tokens) {
		op := p.tokens[p.i].text
		if !strings.Contains(" + - * / = < > <= >= and or ", " "+op+" ") {
			break
		}
		p.i++
		if !p.atom() {
			return false
		}
	}
	return true
}

// This recognizes syntax only to collect column references. It deliberately makes
// no claim about SQL types, arithmetic safety, credentials or query execution.
func parseDocsSELECT(sql string) (view string, columns []string, ok bool) {
	tokens, ok := docsSQLTokens(sql)
	if !ok {
		return "", nil, false
	}
	p := &docsSQLParser{tokens: tokens}
	if !p.take("select") {
		return "", nil, false
	}
	aliases := map[string]bool{}
	for {
		if !p.expr() {
			return "", nil, false
		}
		explicit := p.take("as")
		alias := p.identifier()
		if explicit && alias == "" || alias != "" && aliases[alias] {
			return "", nil, false
		}
		if alias != "" {
			aliases[alias] = true
		}
		if !p.take(",") {
			break
		}
	}
	if !p.take("from") || !p.take("analytics") || !p.take(".") {
		return "", nil, false
	}
	view = p.identifier()
	if view == "" {
		return "", nil, false
	}
	if p.take("where") && !p.expr() {
		return "", nil, false
	}
	// ORDER BY may refer to SELECT output aliases, which are not view columns.
	if p.take("order") {
		if !p.take("by") {
			return "", nil, false
		}
		for {
			start := len(p.columns)
			before := p.i
			if !p.expr() {
				return "", nil, false
			}
			if p.i == before+1 && len(p.columns) == start+1 && aliases[p.columns[start]] {
				p.columns = p.columns[:start]
			}
			if !p.take("asc") {
				p.take("desc")
			}
			if !p.take(",") {
				break
			}
		}
	}
	if p.take("limit") {
		if p.i == len(tokens) || tokens[p.i].kind != "number" {
			return "", nil, false
		}
		p.i++
	}
	p.take(";")
	return view, p.columns, p.i == len(tokens)
}

func (s *docsScan) scanPsql(words []string, where string) {
	invocation := strings.Join(words, " ")
	skip := func(reason string) { s.sqlSkipped = append(s.sqlSkipped, where+": "+invocation+" ("+reason+")") }
	if strings.Contains("/"+filepath.ToSlash(where), "/deployment/releases-and-upgrades.md:") {
		skip("excluded releases-and-upgrades page")
		return
	}
	query := ""
	for i := 1; i < len(words); i++ {
		arg := words[i]
		flag, value, inline := strings.Cut(arg, "=")
		switch flag {
		case "-c", "--command", "-h", "--host", "-p", "--port", "-U", "--username", "-d", "--dbname":
			if !inline {
				if i+1 == len(words) {
					skip("missing psql option value")
					return
				}
				i++
				value = words[i]
			}
			if flag == "-c" || flag == "--command" {
				if query != "" {
					skip("multiple SQL commands")
					return
				}
				query = value
			}
		default:
			if strings.HasPrefix(arg, "-") {
				skip("unsupported psql option")
				return
			}
		}
	}
	view, columns, ok := parseDocsSELECT(query)
	if !ok {
		skip("unsupported or dynamic SQL; expected one analytics SELECT")
		return
	}
	views, err := readDocsAnalytics(s.sqlRepo)
	if err != nil {
		s.findings = append(s.findings, docsFinding{where, invocation, "read analytics projection: " + err.Error()})
		return
	}
	s.sqlChecked++
	if views[view] == nil {
		s.findings = append(s.findings, docsFinding{where, invocation, "unknown analytics view analytics." + view})
		return
	}
	seen := map[string]bool{}
	for _, col := range columns {
		if !views[view][col] && !seen[col] {
			s.findings = append(s.findings, docsFinding{where, invocation, fmt.Sprintf("unknown column %q in analytics.%s", col, view)})
			seen[col] = true
		}
	}
}

func TestDocsSQLRealExampleAndProjection(t *testing.T) {
	md, err := os.ReadFile(filepath.Join(repoRoot, "docs/docs/guides/sql-and-bi-access.md"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(docsAnalyticsSource)))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, filepath.FromSlash(docsAnalyticsSource))
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(repo, "page.md")
	run := func(page, projection []byte) *docsScan {
		t.Helper()
		if err := os.WriteFile(sourcePath, projection, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pagePath, page, 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(pagePath)
		if err != nil {
			t.Fatal(err)
		}
		s := newDocsScan()
		s.sqlRepo = repo
		scanDocsText(rootCmd, "page.md", string(b), func(string) bool { return true }, s)
		return s
	}
	clean := run(md, source)
	if clean.sqlChecked != 1 || len(clean.sqlSkipped) != 0 || len(clean.findings) != 0 || clean.uncovered["psql"] != 0 {
		t.Fatalf("real SELECT: checked=%d skipped=%v findings=%s uncovered=%v", clean.sqlChecked, clean.sqlSkipped, findingTexts(clean), clean.uncovered)
	}
	bad := run([]byte(strings.Replace(string(md), "sum_value / count_value", "sum_valeu / count_value", 1)), source)
	if len(bad.findings) != 1 || !strings.Contains(findingTexts(bad), `unknown column "sum_valeu" in analytics.measurement_rollups`) || !strings.Contains(findingTexts(bad), "page.md:") {
		t.Fatalf("typo must produce one located column/view finding: %s", findingTexts(bad))
	}
	missing := []byte(strings.Replace(string(source), `"count_value",`, "", 1))
	if string(missing) == string(source) {
		t.Fatal("projection mutation did not apply")
	}
	bad = run(md, missing)
	if len(bad.findings) != 1 || !strings.Contains(findingTexts(bad), `unknown column "count_value" in analytics.measurement_rollups`) {
		t.Fatalf("source projection was not read: %s", findingTexts(bad))
	}
}

// Uses the old scanner API so this negative control also runs on main, where
// psql is merely counted as uncovered. Suppressing unknown-column findings must
// make this test red.
func TestDocsPsqlUnknownColumn(t *testing.T) {
	s := scanOne(t, "```bash\npsql -c 'SELECT sum_valeu FROM analytics.measurement_rollups;'\n```\n")
	if len(s.findings) != 1 || !strings.Contains(findingTexts(s), `unknown column "sum_valeu" in analytics.measurement_rollups`) {
		t.Fatalf("SQL typo must fail: %s; uncovered=%v", findingTexts(s), s.uncovered)
	}
}

func TestDocsSQLSubsetAndVisibleSkips(t *testing.T) {
	cases := []struct {
		query             string
		checked, findings int
	}{
		{"SELECT sum_value / count_value AS avg FROM analytics.measurement_rollups ORDER BY avg DESC LIMIT 20;", 1, 0},
		{"SELECT (sum_value + 2) / count_value avg FROM analytics.measurement_rollups WHERE name = 'it''s a FROM clause' ORDER BY bucket, avg ASC;", 1, 0},
		{"SELECT sum_value AS sum_valeu FROM analytics.measurement_rollups ORDER BY sum_valeu;", 1, 0},
		{"SELECT sum_value AS avg FROM analytics.measurement_rollups WHERE sum_valeu > 0;", 1, 1},
		{"SELECT bucket FROM analytics.measurement_rollups ORDER BY buckte;", 1, 1},
		{"SELECT bucket FROM analytics.no_such_view;", 1, 1},
		{"SELECT * FROM analytics.measurement_rollups;", 0, 0},
		{"SELECT m.bucket FROM analytics.measurement_rollups m;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups, analytics.events;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups JOIN analytics.events ON bucket = bucket;", 0, 0},
		{"SELECT sum(sum_value) FROM analytics.measurement_rollups;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups GROUP BY bucket;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups; SELECT bucket FROM analytics.measurement_rollups;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups WHERE name = '$name';", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups -- comment", 0, 0},
		{"SELECT bucket::text FROM analytics.measurement_rollups;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups WHERE bucket > mystery();", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups ORDER BY;", 0, 0},
		{"SELECT bucket FROM analytics.measurement_rollups LIMIT nope;", 0, 0},
		{"SELECT 'unterminated FROM analytics.measurement_rollups;", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			s := newDocsScan()
			s.scanPsql([]string{"psql", "-c", c.query}, "fixture.md:7")
			if s.sqlChecked != c.checked || len(s.findings) != c.findings || len(s.sqlSkipped) != 1-c.checked {
				t.Fatalf("checked=%d findings=%s skips=%v", s.sqlChecked, findingTexts(s), s.sqlSkipped)
			}
			if c.checked == 0 && !strings.Contains(s.sqlSkipped[0], "fixture.md:7:") {
				t.Fatal("skip lost location")
			}
		})
	}
	for _, file := range []string{
		"docs/docs/deployment/releases-and-upgrades.md",
		"docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md",
		"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md",
	} {
		s := newDocsScan()
		scanDocsText(rootCmd, file, "```bash\npsql -c 'SELECT sum_valeu FROM analytics.measurement_rollups;'\n```\n", func(string) bool { return true }, s)
		if s.sqlChecked != 0 || len(s.findings) != 0 || len(s.sqlSkipped) != 1 || !strings.Contains(s.sqlSkipped[0], file+":2:") || !strings.Contains(s.sqlSkipped[0], "excluded releases-and-upgrades page") {
			t.Fatalf("release history exclusion: checked=%d findings=%s skips=%v", s.sqlChecked, findingTexts(s), s.sqlSkipped)
		}
	}
}

func TestDocsSQLProjectionReadErrors(t *testing.T) {
	repo := t.TempDir()
	p := filepath.Join(repo, filepath.FromSlash(docsAnalyticsSource))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"package model\nvar analyticsViews = buildViews()",
		"package model\nvar analyticsViews = []view{{name: dynamic, columns: []string{\"bucket\"}}}",
		"package model\nvar analyticsViews = []view{{name: \"rollups\", columns: columns}}",
		"package model\nvar unrelated = 1",
		"invalid Go",
	} {
		if err := os.WriteFile(p, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newDocsScan()
		s.sqlRepo = repo
		s.scanPsql([]string{"psql", "-c", "SELECT bucket FROM analytics.measurement_rollups"}, "fixture.md:2")
		if len(s.findings) != 1 || !strings.Contains(findingTexts(s), "read analytics projection:") || s.sqlChecked != 0 {
			t.Fatalf("projection failure must fail gate: checked=%d findings=%s", s.sqlChecked, findingTexts(s))
		}
	}
}
