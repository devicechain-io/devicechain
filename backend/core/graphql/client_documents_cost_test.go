// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
)

// THE COST AND ALIAS CEILINGS MUST NOT REFUSE A DOCUMENT A FIRST-PARTY CLIENT SENDS,
// and this is the test that makes that a standing fact rather than a number in a
// comment. It finds the GraphQL documents the repository sends:
//
//   - the console's documents, as codegen writes them out whole
//     (frontend/apps/console/src/gql/*/graphql.ts),
//   - the hand-written documents of the dashboard viewer and the npm packages
//     (frontend/apps/dashboard, frontend/packages/*),
//   - the C# and Unity SDKs (sdks/),
//   - every Go string expression in backend/ that is a whole document, constants
//     resolved: the MCP server's tools, dcctl, the load test and the simulators, and the
//     services' clients of each other,
//   - the genqlient operation files (backend/**/genqlient-*.graphql),
//
// finds each served schema it is valid against, and costs it there with NO variables:
// every page whose size comes from a variable counts at maxPageSize, the most rows any
// page holds, and every lookup list from a variable at maxPageSize keys. That is the
// largest cost the document can have at any variables the client may send, so passing
// here means passing at every page size.
//
// The exception is clientVariables: a document whose worst case is above the ceiling
// is costed with the variables its client actually sends, at their largest, each
// with the code that bounds them. A new document over the ceiling fails here until it
// is either made cheaper or argued into that table.
//
// 🔴 IT READS FILES OUTSIDE THIS MODULE, which Go's test cache does not track. Run it
// with -count=1 (CI does) or a cached PASS survives a client edit that must fail.
//
// A whole document valid against no served schema FAILS the test, and so does finding
// fewer documents than a floor: an extraction that quietly found nothing would make
// this a gate that cannot fail.

const repoRoot = "../../.."

// clientVariables are the largest variables a client sends with a document whose
// worst case is over the cost ceiling, by operation name.
var clientVariables = map[string]map[string]any{
	// frontend/apps/console/src/lib/api/browse.ts resolveGroupMembers: always one
	// group token; the page size is the caller's, counted here at the largest page.
	"GroupMembers": {
		"tokens":     []any{"group"},
		"pagination": map[string]any{"pageNumber": 1, "pageSize": maxPageSize},
	},
	// frontend/apps/console/src/lib/api/device-management.ts getDeviceGateway: always
	// one device token; the page size is the caller's, counted at the largest page.
	"DeviceGateway": {
		"tokens":     []any{"device"},
		"pagination": map[string]any{"pageNumber": 1, "pageSize": maxPageSize},
	},
}

// servedSchemaUnderTest is one served schema file, parsed as a service parses it.
type servedSchemaUnderTest struct {
	path   string
	schema *Schema
}

func servedSchemas(t *testing.T) []servedSchemaUnderTest {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(repoRoot, "backend", "services", "*", "graphql"))
	if err != nil {
		t.Fatal(err)
	}
	var out []servedSchemaUnderTest
	for _, dir := range dirs {
		schemas, err := schemaplane.Dir(dir)
		if err != nil {
			t.Fatalf("classify %s: %v", dir, err)
		}
		for _, s := range schemas {
			sdl, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, servedSchemaUnderTest{path: filepath.ToSlash(s.Path), schema: MustParseSchema(string(sdl), nil)})
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d served schemas; the sweep would check against almost nothing", len(out))
	}
	return out
}

// clientDocument is one document a client sends, and where it was found.
type clientDocument struct {
	where, text string
}

var (
	docStart     = regexp.MustCompile(`^\s*(query|mutation|subscription|fragment)\b[\s\S]*\{`)
	hasOperation = regexp.MustCompile(`(^|[\s}])(query|mutation|subscription)\b`)
)

// jsLiteral is a TypeScript or C# string literal: backquoted, single- or double-quoted.
var (
	jsLiteral = regexp.MustCompile("`(?:[^`\\\\]|\\\\.)*`|'(?:[^'\\\\\\n]|\\\\.)*'|\"(?:[^\"\\\\\\n]|\\\\.)*\"")
	concatGap = regexp.MustCompile(`^\s*\+\s*$`)
)

// jsStringLiterals finds the string literals of a TypeScript or C# source, joining
// literals concatenated with + into one, which is how both write a long document.
func jsStringLiterals(src string) []string {
	locs := jsLiteral.FindAllStringIndex(src, -1)
	var out []string
	var cur strings.Builder
	for i, loc := range locs {
		lit := src[loc[0]:loc[1]]
		body := lit[1 : len(lit)-1]
		if lit[0] != '`' {
			body = strings.NewReplacer(`\"`, `"`, `\'`, `'`, `\n`, "\n").Replace(body)
		}
		cur.WriteString(body)
		if i+1 < len(locs) && concatGap.MatchString(src[loc[1]:locs[i+1][0]]) {
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	return out
}

func walkFiles(t *testing.T, root string, keep func(path string) bool, visit func(path, src string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "dist", "_legacy", ".git", "bin", "obj", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !keep(path) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func isTest(path string) bool {
	base := filepath.Base(path)
	slash := filepath.ToSlash(path)
	return strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") || strings.Contains(slash, "/test/") || strings.Contains(slash, "/tests/")
}

// goDocuments finds the string expressions of one Go package's (non-test) sources that
// are documents: literals, and concatenations of literals and of the package's own
// string constants and variables, which is how the services and dcctl assemble a
// document from a shared field list. A document built at run time (fmt.Sprintf, a
// strings.Builder) is not whole here; the document sweeps beside those packages check
// it with CheckWork.
func goDocuments(files []*ast.File) []string {
	decls := map[string]ast.Expr{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.CONST && g.Tok != token.VAR) {
				continue
			}
			for _, spec := range g.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, n := range vs.Names {
					decls[n.Name] = vs.Values[i]
				}
			}
		}
	}
	resolving := map[string]bool{}
	var eval func(ast.Expr) (string, bool)
	eval = func(e ast.Expr) (string, bool) {
		switch e := e.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				return "", false
			}
			l, ok1 := eval(e.X)
			r, ok2 := eval(e.Y)
			return l + r, ok1 && ok2
		case *ast.ParenExpr:
			return eval(e.X)
		case *ast.Ident:
			d, ok := decls[e.Name]
			if !ok || resolving[e.Name] {
				return "", false
			}
			resolving[e.Name] = true
			defer delete(resolving, e.Name)
			return eval(d)
		}
		return "", false
	}
	var out []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if s, ok := eval(e); ok {
				out = append(out, s)
				return false
			}
			return true
		})
	}
	return out
}

func clientDocuments(t *testing.T) []clientDocument {
	t.Helper()
	var docs []clientDocument
	seen := map[string]bool{}
	add := func(where, text string) {
		// A document of fragments alone is never sent; it is spread into the documents
		// that are, and costed there.
		if !docStart.MatchString(text) || !hasOperation.MatchString(text) || seen[text] {
			return
		}
		seen[text] = true
		docs = append(docs, clientDocument{where: filepath.ToSlash(where), text: text})
	}
	ts := func(path string) bool {
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") || isTest(path) {
			return false
		}
		// The console's sources write each document apart from its fragments, and
		// codegen joins them: the generated graphql.ts holds the document as sent.
		slash := filepath.ToSlash(path)
		if strings.Contains(slash, "/apps/console/") {
			return strings.Contains(slash, "/src/gql/") && filepath.Base(path) == "graphql.ts"
		}
		return true
	}
	literals := func(path, src string) {
		for _, lit := range jsStringLiterals(src) {
			add(path, lit)
		}
	}
	for _, dir := range []string{"frontend/apps", "frontend/packages"} {
		walkFiles(t, filepath.Join(repoRoot, dir), ts, literals)
	}
	walkFiles(t, filepath.Join(repoRoot, "sdks"), func(p string) bool {
		return strings.HasSuffix(p, ".cs") && !isTest(p)
	}, literals)

	pkgs := map[string][]*ast.File{}
	fset := token.NewFileSet()
	walkFiles(t, filepath.Join(repoRoot, "backend"), func(p string) bool {
		return strings.HasSuffix(p, ".go") && !isTest(p)
	}, func(path, src string) {
		if f, err := parser.ParseFile(fset, path, src, 0); err == nil {
			dir := filepath.Dir(path)
			pkgs[dir] = append(pkgs[dir], f)
		}
	})
	for dir, files := range pkgs {
		for _, d := range goDocuments(files) {
			add(dir, d)
		}
	}

	walkFiles(t, filepath.Join(repoRoot, "backend"), func(p string) bool {
		return strings.HasPrefix(filepath.Base(p), "genqlient-") && strings.HasSuffix(p, ".graphql")
	}, func(path, src string) {
		// A genqlient file is one document of fragments and operations; genqlient
		// sends each operation with the fragments it spreads, and costing the file
		// whole costs each operation with them.
		add(path, src)
	})
	return docs
}

// validationErrors validates a document against a schema, leaving out the one rule that
// needs variable values this sweep does not have.
func validationErrors(s *Schema, doc string) []string {
	var out []string
	for _, e := range s.inner.Validate(doc) {
		// Both rules are about variable VALUES and DEFAULTS, which cost nothing to run.
		if e.Rule != "VariablesOfCorrectType" && e.Rule != "DefaultValuesOfCorrectType" {
			out = append(out, fmt.Sprintf("%s [%s]", e.Message, e.Rule))
		}
	}
	return out
}

// measured is one document's cost and alias count against one schema it is valid on.
type measured struct {
	doc     clientDocument
	schema  servedSchemaUnderTest
	vars    map[string]any
	cost    int64
	aliases int
}

func measureClientDocuments(t *testing.T) (all []measured, unmatched []string, partial int) {
	t.Helper()
	schemas := servedSchemas(t)
	for _, d := range clientDocuments(t) {
		ops, fragments, qerr := readDocument(d.text, DefaultGraphQLMaxQueryLength)
		if qerr != nil {
			// The piece of a document a program completes at run time (a prefix whose
			// field list code appends): not one this sweep can cost.
			partial++
			continue
		}
		var fewest []string
		found := false
		for _, s := range schemas {
			if errs := validationErrors(s.schema, d.text); len(errs) > 0 {
				if fewest == nil || len(errs) < len(fewest) {
					fewest = errs
				}
				continue
			}
			found = true
			m := measured{doc: d, schema: s}
			for _, op := range ops {
				vars := clientVariables[op.name]
				if vars != nil {
					m.vars = vars
				}
				m.cost = max(m.cost, operationCost(op, fragments, s.schema.model, vars))
				m.aliases = max(m.aliases, maxFieldAliases(op, fragments, s.schema.model, vars))
			}
			all = append(all, m)
		}
		if !found {
			unmatched = append(unmatched, fmt.Sprintf("%s: valid against no served schema: %v\n%s", d.where, fewest, d.text))
		}
	}
	return all, unmatched, partial
}

func TestRealClientDocumentsAreWithinTheCostCeiling(t *testing.T) {
	all, unmatched, partial := measureClientDocuments(t)
	if len(all) < 500 {
		t.Fatalf("measured only %d client documents; the extraction has stopped finding them", len(all))
	}
	for _, u := range unmatched {
		t.Error(u)
	}

	sort.Slice(all, func(i, j int) bool { return all[i].cost > all[j].cost })
	for i := 0; i < 10 && i < len(all); i++ {
		t.Logf("cost %6d  aliases %2d  %s (%s)", all[i].cost, all[i].aliases, opName(all[i].doc.text), all[i].doc.where)
	}
	worstAliases := 0
	for _, m := range all {
		worstAliases = max(worstAliases, m.aliases)
	}
	t.Logf("%d documents measured (%d run-time pieces skipped); highest cost %d (ceiling %d); most names for one field at one level %d (ceiling %d)",
		len(all), partial, all[0].cost, DefaultGraphQLMaxQueryCost, worstAliases, DefaultGraphQLMaxFieldAliases)

	used := map[string]bool{}
	for _, m := range all {
		if m.vars != nil {
			used[opName(m.doc.text)] = true
		}
		// Through the serving path's own check, not only the measurement above.
		if err := m.schema.schema.CheckWork(m.doc.text, m.vars); err != nil {
			t.Errorf("%s (%s) is refused against %s: %v", opName(m.doc.text), m.doc.where, m.schema.path, err)
		}
	}
	// A clientVariables entry no document uses any more is an exemption kept for
	// nothing; it goes when its document does.
	for name := range clientVariables {
		if !used[name] {
			t.Errorf("clientVariables names %s, which no client document is any more", name)
		}
	}
}

func opName(doc string) string {
	f := strings.Fields(doc)
	for i, w := range f {
		if (w == "query" || w == "mutation" || w == "subscription") && i+1 < len(f) {
			name, _, _ := strings.Cut(f[i+1], "(")
			name, _, _ = strings.Cut(name, "{")
			return name
		}
	}
	return "(fragments)"
}
