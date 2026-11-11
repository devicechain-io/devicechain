// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/graph-gophers/graphql-go"
)

// English, es and zh-CN each currently check 10 literal documents. Keep the
// floor just below that measured count so an extractor regression fails loudly.
const minGraphQLChecksPerTree = 9

var docsGraphQLEndpoint = regexp.MustCompile(`^(?:[^\s]+://[^/]+|[^/\s]+)?/api/([^/?#]+)(/(?:admin/|settings/)?graphql)/?(?:[?#].*)?$`)

type docsGraphQLSchema struct {
	schema *graphql.Schema
	err    error
}

// Schemas are cached on the scan, keyed by absolute repository/file path. Each
// plane is independent: their overlapping type names must never be concatenated.
func (s *docsScan) graphqlSchema(file string) docsGraphQLSchema {
	key, err := filepath.Abs(file)
	if err != nil {
		return docsGraphQLSchema{err: err}
	}
	if s.graphqlSchemas == nil {
		s.graphqlSchemas = map[string]docsGraphQLSchema{}
	}
	if cached, ok := s.graphqlSchemas[key]; ok {
		return cached
	}
	data, err := os.ReadFile(key)
	var schema *graphql.Schema
	if err == nil {
		schema, err = graphql.ParseSchema(string(data), nil)
	}
	cached := docsGraphQLSchema{schema, err}
	s.graphqlSchemas[key] = cached
	return cached
}

// True means this curl belongs to the GraphQL checked/skipped surface. Other
// curls remain uncovered. Shell tokenization marks double-quoted expansions
// with substPlaceholder while preserving single-quoted GraphQL $variables.
func (s *docsScan) scanCurlGraphQL(words []string, where string) bool {
	area, mount, body, bodies := "", "", "", 0
	for i := 1; i < len(words); i++ {
		switch words[i] {
		case "-d", "--data", "--data-raw", "--data-binary":
			bodies++
			if i+1 < len(words) {
				i++
				body = words[i]
			}
		case "-H", "--header", "-X", "--request", "-o", "--output", "-u", "--user":
			if i+1 < len(words) {
				i++
			}
		default:
			attached := false
			for _, prefix := range []string{"--data=", "--data-raw=", "--data-binary=", "-d"} {
				if strings.HasPrefix(words[i], prefix) {
					bodies++
					body = strings.TrimPrefix(words[i], prefix)
					attached = true
					break
				}
			}
			if attached {
				continue
			}
			if m := docsGraphQLEndpoint.FindStringSubmatch(words[i]); m != nil {
				area = m[1]
				mount = m[2]
			}
		}
	}
	if area == "" {
		return false
	}
	invocation := strings.Join(words, " ")
	skip := func(reason string) {
		s.graphqlSkipped = append(s.graphqlSkipped, where+": "+invocation+" ("+reason+")")
	}
	// Only a service basename can resolve; never walk outside services.
	if strings.ContainsAny(area, "<>\\") || area == "." || area == ".." {
		skip("unresolvable endpoint")
		return true
	}
	// Mirrors core/graphql/schemaplane's Mount* constants: one endpoint, one SDL.
	filename := map[string]string{
		"/graphql":          "schema.graphql",
		"/admin/graphql":    "admin_schema.graphql",
		"/settings/graphql": "settings_schema.graphql",
	}[mount]
	file := filepath.Join(s.graphqlRepo, "backend", "services", area, "graphql", filename)
	plane := s.graphqlSchema(file)
	if os.IsNotExist(plane.err) {
		skip("unresolvable endpoint")
		return true
	}
	var payload struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	switch {
	case bodies != 1:
		skip("dynamic body: expected one data value")
		return true
	case strings.Contains(body, substPlaceholder):
		skip("dynamic body: shell substitution")
		return true
	case json.Unmarshal([]byte(body), &payload) != nil:
		skip("dynamic body: non-JSON")
		return true
	case strings.TrimSpace(payload.Query) == "":
		skip("dynamic body: no literal query")
		return true
	}
	s.graphqlChecked++
	var messages []string
	if plane.err != nil {
		messages = append(messages, fmt.Sprintf("parse schema %s: %v", filename, plane.err))
	} else {
		for _, e := range plane.schema.Validate(payload.Query) {
			// Validate delegates to ValidateWithVariables(query, nil) in v1.10.3.
			// Missing runtime values are outside this document-only gate. Keep
			// every structural error, including unknown variable declaration types.
			if e.Rule == "VariablesOfCorrectType" && strings.HasPrefix(e.Message, "Variable \"") && strings.Contains(e.Message, "has invalid value null.\nExpected type") {
				continue
			}
			messages = append(messages, e.Message)
		}
	}
	if len(messages) > 0 {
		s.findings = append(s.findings, docsFinding{where, invocation, "GraphQL query does not validate against " + area + mount + " (" + filename + "): " + strings.Join(messages, "; ")})
	}
	return true
}

func scanGraphQLPage(t *testing.T, text string) *docsScan {
	t.Helper()
	s := newDocsScan()
	s.graphqlRepo = repoRoot
	scanDocsText(rootCmd, "page.md", text, func(string) bool { return true }, s)
	return s
}

func TestDocsGraphQLRealPageAndMutants(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs/docs/quickstart/first-device.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	clean := scanGraphQLPage(t, text)
	if clean.graphqlChecked < 6 || len(clean.findings) != 0 {
		t.Fatalf("real page: checked=%d skips=%v findings=%s", clean.graphqlChecked, clean.graphqlSkipped, findingTexts(clean))
	}
	for _, tc := range []struct{ from, to, want string }{
		{"latestMeasurements(deviceToken", "latestMeasurments(deviceToken", `Cannot query field "latestMeasurments"`},
		{"latestMeasurements(deviceToken", "latestMeasurements(deviceTokeen", `Unknown argument "deviceTokeen"`},
		{"$r:DeviceCreateRequest", "$r:DeviceCreatRequest", `Unknown type "DeviceCreatRequest"`},
	} {
		t.Run(tc.to, func(t *testing.T) {
			if strings.Count(text, tc.from) != 1 {
				t.Fatal("mutation must apply exactly once")
			}
			mutant := strings.Replace(text, tc.from, tc.to, 1)
			at := strings.Index(mutant, tc.to)
			curl := strings.LastIndex(mutant[:at], "curl ")
			where := fmt.Sprintf("page.md:%d", strings.Count(mutant[:curl], "\n")+1)
			bad := scanGraphQLPage(t, mutant)
			if len(bad.findings) != 1 || bad.findings[0].where != where || !strings.Contains(findingTexts(bad), tc.want) {
				t.Fatalf("mutant must produce exactly one located finding at %s: %s", where, findingTexts(bad))
			}
		})
	}
}

func TestDocsGraphQLVisibleSkipsAndUncovered(t *testing.T) {
	for _, tc := range []struct{ command, reason string }{
		{`curl https://<your-host>/api/<area>/graphql -d '{"query":"{__typename}"}'`, "unresolvable endpoint"},
		{`curl localhost/api/no-such-area/graphql -d '{"query":"{__typename}"}'`, "unresolvable endpoint"},
		{`curl /api/device-state/graphql -d '{"variables":{}}'`, "dynamic"},
		{`curl /api/device-state/graphql -d "{\"query\":\"{__typename}\",\"variables\":{\"token\":\"$DC_TOKEN\"}}"`, "dynamic"},
		{`curl /api/device-state/graphql -d 'not JSON'`, "dynamic"},
		{`curl /api/device-state/graphql -d "$(cat body.json)"`, "dynamic"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			s := scanGraphQLPage(t, "```bash\n"+tc.command+"\n```\n")
			if s.graphqlChecked != 0 || len(s.findings) != 0 || len(s.graphqlSkipped) != 1 || !strings.Contains(s.graphqlSkipped[0], "page.md:2:") || !strings.Contains(s.graphqlSkipped[0], tc.reason) || s.uncovered["curl"] != 0 {
				t.Fatalf("checked=%d skips=%v findings=%s uncovered=%v", s.graphqlChecked, s.graphqlSkipped, findingTexts(s), s.uncovered)
			}
		})
	}
	s := scanGraphQLPage(t, "```bash\ncurl https://localhost/download\n```\n")
	if s.uncovered["curl"] != 1 || s.graphqlChecked != 0 || len(s.graphqlSkipped) != 0 {
		t.Fatalf("ordinary curl lost: %+v", s)
	}
}

// Every shipped plane must parse without resolvers, including custom scalars and
// directives. This also exercises schemas that have no curl examples yet.
func TestDocsGraphQLRepoSchemas(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot, "backend/services/*/graphql/*.graphql"))
	if err != nil || len(files) < 10 {
		t.Fatalf("schema inventory: files=%d err=%v", len(files), err)
	}
	s := newDocsScan()
	for _, file := range files {
		if plane := s.graphqlSchema(file); plane.err != nil {
			t.Errorf("%s: %v", file, plane.err)
		}
	}
}

func TestDocsGraphQLSeparatePlanesAndBrokenSchema(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "backend/services/fixture/graphql")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, sdl string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sdl), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("admin_schema.graphql", "type Query { admin: String }")
	write("schema.graphql", "type Query { good: String }")
	write("settings_schema.graphql", "type Query { settings: String }")
	for _, tc := range []struct {
		endpoint string
		query    string
		finding  string
	}{
		{"/api/fixture/graphql", "good", ""},
		{"/api/fixture/graphql", "settings", `Cannot query field "settings"`},
		{"/api/fixture/graphql", "admin", `Cannot query field "admin"`},
		{"/api/fixture/settings/graphql", "settings", ""},
		{"/api/fixture/settings/graphql", "good", `Cannot query field "good"`},
		{"/api/fixture/admin/graphql", "admin", ""},
		{"/api/fixture/admin/graphql", "good", `Cannot query field "good"`},
		{"/api/fixture/admin/graphql", "goood", `Cannot query field "goood"`},
	} {
		t.Run(tc.endpoint+"/"+tc.query, func(t *testing.T) {
			s := newDocsScan()
			s.graphqlRepo = repo
			s.scanCurlGraphQL([]string{"curl", "https://<your-host>" + tc.endpoint, "-d", fmt.Sprintf(`{"query":"{%s}"}`, tc.query)}, "fixture.md:7")
			wantFindings := 0
			if tc.finding != "" {
				wantFindings = 1
			}
			if s.graphqlChecked != 1 || len(s.graphqlSkipped) != 0 || len(s.findings) != wantFindings || !strings.Contains(findingTexts(s), tc.finding) || len(s.graphqlSchemas) != 1 {
				t.Fatalf("planes: checked=%d skips=%v findings=%s cache=%d", s.graphqlChecked, s.graphqlSkipped, findingTexts(s), len(s.graphqlSchemas))
			}
		})
	}
	// Exercise shell-joined words through the page extractor, including attached
	// short data and equals-separated long data options.
	for _, flag := range []string{"-d ", "--data ", "--data-raw ", "--data-binary ", "-d", "--data=", "--data-raw=", "--data-binary="} {
		for _, query := range []string{"good", "goood"} {
			t.Run(flag+query, func(t *testing.T) {
				s := newDocsScan()
				s.graphqlRepo = repo
				text := fmt.Sprintf("```bash\ncurl localhost/api/fixture/graphql %s'{\"query\":\"{%s}\"}'\n```\n", flag, query)
				scanDocsText(rootCmd, "fixture.md", text, func(string) bool { return true }, s)
				wantFindings := 0
				if query == "goood" {
					wantFindings = 1
				}
				if s.graphqlChecked != 1 || len(s.graphqlSkipped) != 0 || len(s.findings) != wantFindings || s.uncovered["curl"] != 0 {
					t.Fatalf("data option: checked=%d skips=%v findings=%s uncovered=%v", s.graphqlChecked, s.graphqlSkipped, findingTexts(s), s.uncovered)
				}
				if wantFindings == 1 && (s.findings[0].where != "fixture.md:2" || !strings.Contains(findingTexts(s), `Cannot query field "goood"`)) {
					t.Fatalf("expected located finding: %s", findingTexts(s))
				}
			})
		}
	}
	for _, tc := range []struct{ mount, file, sdl string }{
		{"/graphql", "schema.graphql", "type Query { good: String }"},
		{"/admin/graphql", "admin_schema.graphql", "type Query { admin: String }"},
		{"/settings/graphql", "settings_schema.graphql", "type Query { settings: String }"},
	} {
		t.Run("missing"+tc.mount, func(t *testing.T) {
			if err := os.Remove(filepath.Join(dir, tc.file)); err != nil {
				t.Fatal(err)
			}
			s := newDocsScan()
			s.graphqlRepo = repo
			if !s.scanCurlGraphQL([]string{"curl", "/api/fixture" + tc.mount, "-d", `{"query":"{__typename}"}`}, "fixture.md:7") {
				t.Fatal("GraphQL curl was not recognized")
			}
			if s.graphqlChecked != 0 || len(s.findings) != 0 || len(s.graphqlSkipped) != 1 || !strings.Contains(s.graphqlSkipped[0], "fixture.md:7:") || !strings.Contains(s.graphqlSkipped[0], "unresolvable endpoint") {
				t.Fatalf("missing plane: checked=%d skips=%v findings=%s", s.graphqlChecked, s.graphqlSkipped, findingTexts(s))
			}
			write(tc.file, tc.sdl)
		})
	}
	write("admin_schema.graphql", "broken SDL")
	s := newDocsScan()
	s.graphqlRepo = repo
	s.scanCurlGraphQL([]string{"curl", "/api/fixture/graphql", "-d", `{"query":"{good}"}`}, "fixture.md:7")
	if s.graphqlChecked != 1 || len(s.findings) != 0 || len(s.graphqlSchemas) != 1 {
		t.Fatalf("unrelated broken plane affected tenant: checked=%d findings=%s cache=%d", s.graphqlChecked, findingTexts(s), len(s.graphqlSchemas))
	}
	s.scanCurlGraphQL([]string{"curl", "/api/fixture/admin/graphql", "-d", `{"query":"{admin}"}`}, "fixture.md:8")
	if s.graphqlChecked != 2 || len(s.findings) != 1 || !strings.Contains(findingTexts(s), "parse schema admin_schema.graphql") || len(s.graphqlSkipped) != 0 {
		t.Fatalf("broken selected plane must fail loudly: %s", findingTexts(s))
	}
}
