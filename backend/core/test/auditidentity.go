// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// AnonymousMutation is one statement the scan found: a mutation whose condition names
// a row by PRIMARY KEY while handing the audit callback a zero-value model, so the
// journal records that something changed without recording which row.
//
// The three fields are carried so a failure can be read without opening the file. The
// condition in particular is what distinguishes this from the cases that are allowed to
// record nothing: it is the evidence that the identity was in the caller's hand.
type AnonymousMutation struct {
	File      string // absolute path, carried separately so callers need not parse Pos
	Pos       string // file:line:col, for the failure message
	Model     string // the zero-value model handed to Model()
	Operation string // Updates, Update or Delete
	Condition string // the Where literal that names the primary key
}

// AssertEveryIdentifiedMutationNamesItsRow fails for each mutation under root that
// identifies its row by primary key and still journals an anonymous audit entry.
//
// 🔴 WHY THIS IS A REPOSITORY-WIDE GUARD AND NOT A CODE REVIEW NOTE. The trap it
// covers has been DOCUMENTED IN PROSE THREE SEPARATE TIMES in this tree, by three
// authors, each of whom routed their own call site around it and left it in place for
// the next one:
//
//   - ai-inference/model/api.go, on the branch beside the one that walks into it:
//     "Save the loaded row (its PK + AuditLabel reach the audit journal, unlike a map
//     Updates)"
//   - ai-inference/model/function_api.go: "journals an empty row: no tenant, no
//     function, no PK — on the one arm that CHANGES a tenant's answer"
//   - ai-inference/model/grant_api.go: "That is the same trap SetFunctionModel
//     documents, on an act just as auditable"
//
// A hazard that gets a warning paragraph instead of a check is a hazard that keeps its
// next victim. Three comments did not stop the twenty-second occurrence.
//
// The defect itself is in rdb's audit callback and is invisible at the call site:
// Model(&T{}) leaves Statement.ReflectValue a zero struct, so auditPrimaryKey reads it
// as unset and auditLabel calls AuditLabel() on a zero value. RowsAffected is 1, so the
// row IS written — as "somebody updated some row in commands". The mutation is recorded
// and its subject is not, which for an audit journal is the failure that matters:
// ADR-019's whole claim is capture by construction, and an unattributable entry is the
// same as no entry.
//
// 🔴 WHAT IT DELIBERATELY DOES NOT FLAG, because "" is honest there:
//
//   - a condition-only mutation (Where(where, args...), or a natural-key CAS such as
//     Where("alarm_token = ? AND escalation_level = ?")). No single primary key exists
//     in the caller's hand, so the callback has nothing to record. Several of these DO
//     hold a natural key that would make a good EntityLabel, which is a separate
//     improvement and deliberately not this guard's business.
//   - a model that implements AuditExempt. Those tables are outside the journal
//     entirely, so there is no entry to be anonymous.
//
// Two things to know before this fails on you, both inherited from the guard next door:
//
//   - It reads files outside this module, so `-count=1` is load-bearing. Go's test
//     cache does not track them, and a cached PASS would survive a site added
//     elsewhere.
//   - A site added in a service module is reported by THIS module's test run. The
//     message names the offending file and line.
func AssertEveryIdentifiedMutationNamesItsRow(t *testing.T, root string, mustVisit []string) {
	t.Helper()
	found, visited, err := anonymousMutationsUnder(root)
	if err != nil {
		t.Fatalf("scanning %s for anonymous audited mutations: %v", root, err)
	}
	for _, dir := range mustVisit {
		abs, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		if !visited[abs] {
			t.Errorf("the scan of %s never descended into %s, so whatever it reports about "+
				"that tree it did not look at. A clean scan and a scan that reached nothing "+
				"are the same answer, which is why this is checked separately", root, abs)
		}
	}
	for _, m := range found {
		t.Errorf("%s: %s(&%s{}) is mutated by %s under the condition %q, which names the row "+
			"by primary key — but the zero-value model leaves the audit journal with no "+
			"EntityPK and no EntityLabel, so the entry reads \"somebody changed some row in "+
			"this table\". Hand the statement the identity it already has: pass the loaded "+
			"row (Model(&loaded)), or seed the key into the literal "+
			"(Model(&%s{Model: gorm.Model{ID: id}})), which needs no extra query",
			m.Pos, "Model", m.Model, m.Operation, m.Condition, m.Model)
	}
}

// mutatingMethods are the gorm terminals that produce an audit entry. They are matched
// by NAME, which is what lets this scan run without type information across modules it
// does not import — and is also its main limit: a local helper of the same name would
// be matched, and a differently-named wrapper around Updates would not.
var mutatingMethods = map[string]bool{"Updates": true, "Update": true, "Delete": true}

// primaryKeyCondition matches a SQL fragment that constrains the primary key to ONE
// value, which is the only case where the callback has a single key to record.
//
// Two ways to get this wrong, and the check made both before it was right:
//
//   - It must NOT match "asset_type_id = ?" or "entity_group_id = ?". Those are foreign
//     keys naming a DIFFERENT row than the one being mutated. The leading boundary is
//     what excludes them, and it works only because "_" is a word character, so there is
//     no boundary between "_" and "id".
//   - It must NOT match "id IN ?". That is a BATCH — device-management retires a whole
//     set of credentials that way — and a batch has no single primary key by definition.
//     auditPrimaryKey returns "" for it deliberately, RowsAffected carries the count, and
//     an entry naming one of N rows would be worse than one naming none. Flagging it
//     would have demanded a "fix" that makes the journal less accurate.
var primaryKeyCondition = regexp.MustCompile(`(^|[\s(])id\s*=\s*\?`)

// anonymousMutationsUnder walks root and reports every audited mutation that names its
// row by primary key while handing gorm a zero-value model, along with the set of
// directories the walk descended into.
//
// The scan is two passes because the answer depends on a fact declared in a different
// file from the call site: whether the model opts out of the journal. AuditExempt is
// collected first, keyed by DIRECTORY rather than by bare type name, so that two
// services owning same-named types cannot exempt each other's. Every opt-out in the
// tree today is declared in the same package as the call sites it covers.
//
// It is separate from the assertion above so this package's own tests can drive it
// against fixtures and read what it found. A guard whose only output is a failed test
// cannot be shown to fire, and this one has to be shown to fire on the real shape and
// to stay quiet on each of the two that are allowed to record nothing.
func anonymousMutationsUnder(root string) ([]AnonymousMutation, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	visited := map[string]bool{}
	var files []string

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != abs && skipDirInScan(d.Name()) {
				return fs.SkipDir
			}
			visited[path] = true
			return nil
		}
		// Only production sources. A test may legitimately build a zero-value model to
		// drive the callback itself — including the fixtures this guard is proven on.
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no non-test .go files found under %s, so the scan asserts nothing", abs)
	}

	exempt := map[string]bool{} // "dir\x00TypeName"
	parsed := make(map[string]*ast.File, len(files))
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		parsed[file] = f
		for _, name := range auditExemptReceivers(f) {
			exempt[filepath.Dir(file)+"\x00"+name] = true
		}
	}

	var found []AnonymousMutation
	for _, file := range files {
		found = append(found, anonymousMutationsIn(fset, parsed[file], filepath.Dir(file), exempt)...)
	}
	return found, visited, nil
}

// auditExemptReceivers returns the type names in f that declare an AuditExempt method,
// i.e. the models that opt out of the journal.
func auditExemptReceivers(f *ast.File) []string {
	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "AuditExempt" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		expr := fn.Recv.List[0].Type
		if star, ok := expr.(*ast.StarExpr); ok {
			expr = star.X
		}
		if ident, ok := expr.(*ast.Ident); ok {
			names = append(names, ident.Name)
		}
	}
	return names
}

// anonymousMutationsIn reports the findings in one parsed file.
//
// It works a function at a time because a gorm statement is not always one expression:
// a caller that adds a condition behind an `if` has to park the partial statement in a
// variable, and the mutation then reads `write.Updates(...)` with no Model() anywhere in
// its own chain. Resolving those variables is not a refinement — without it this guard
// reported dashboard-management CLEAN while it held exactly the defect being scanned
// for, which is the same silent success the check exists to make impossible.
func anonymousMutationsIn(fset *token.FileSet, f *ast.File, dir string, exempt map[string]bool) []AnonymousMutation {
	var found []AnonymousMutation
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		found = append(found, anonymousMutationsInFunc(fset, fn, dir, exempt)...)
	}
	return found
}

// anonymousMutationsInFunc scans one function body, carrying the partial statements its
// local variables hold.
//
// The variable map is deliberately shallow: one level of assignment, single-name left
// side, within one function. It covers the shape the tree actually uses (build, extend
// under a condition, terminate) and nothing more. A statement passed to another function
// or stored in a struct field is beyond it — and is reported as nothing rather than as a
// finding, which is the limit worth knowing when reading a clean result.
func anonymousMutationsInFunc(fset *token.FileSet, fn *ast.FuncDecl, dir string, exempt map[string]bool) []AnonymousMutation {
	var found []AnonymousMutation
	held := map[string][]*ast.CallExpr{}

	// resolve returns the full chain behind a call, following the receiver into a local
	// variable when the chain bottoms out at one.
	resolve := func(call *ast.CallExpr) []*ast.CallExpr {
		links, base := chainLinks(call)
		if ident, ok := base.(*ast.Ident); ok {
			links = append(links, held[ident.Name]...)
		}
		return links
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			if len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				if ident, ok := assign.Lhs[0].(*ast.Ident); ok {
					if call, ok := assign.Rhs[0].(*ast.CallExpr); ok {
						// The right side is resolved BEFORE the binding is replaced, so
						// `write = write.Where(...)` extends the chain rather than
						// erasing it.
						held[ident.Name] = resolve(call)
					}
				}
			}
			return true
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !mutatingMethods[sel.Sel.Name] {
			return true
		}
		links := resolve(call)

		model, ok := zeroValueModelArg(links)
		if !ok || exempt[dir+"\x00"+model] {
			return true
		}
		condition, ok := primaryKeyWhere(links)
		if !ok {
			return true
		}
		at := fset.Position(call.Pos())
		found = append(found, AnonymousMutation{
			File:      at.Filename,
			Pos:       at.String(),
			Model:     model,
			Operation: sel.Sel.Name,
			Condition: condition,
		})
		return true
	})
	return found
}

// chainLinks decomposes a method chain into its links, outermost first, and returns the
// expression the chain bottoms out at — which is what tells a caller whether the rest of
// the statement is in a local variable.
//
// A gorm statement is written as one expression but parses as nested calls —
// Updates(Where(Model(DB(ctx)))) — so the receiver of each link is the call before it.
// Walking the selectors is what lets the Model() and Where() of the SAME statement be
// read together; matching them line-by-line instead is what made an earlier, textual
// version of this check count a call written across two lines as two unrelated ones.
func chainLinks(call *ast.CallExpr) ([]*ast.CallExpr, ast.Expr) {
	var links []*ast.CallExpr
	var expr ast.Expr = call
	for {
		c, ok := expr.(*ast.CallExpr)
		if !ok {
			return links, expr
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return links, expr
		}
		links = append(links, c)
		expr = sel.X
	}
}

// zeroValueModelArg returns the type name passed to Model(&T{}) in the chain, if the
// chain has such a link. A Model() given anything else — a loaded row, a pointer
// variable, a literal with fields set — is exactly the correct form and is not matched.
func zeroValueModelArg(links []*ast.CallExpr) (string, bool) {
	for _, link := range links {
		sel, ok := link.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Model" || len(link.Args) != 1 {
			continue
		}
		unary, ok := link.Args[0].(*ast.UnaryExpr)
		if !ok || unary.Op != token.AND {
			continue
		}
		lit, ok := unary.X.(*ast.CompositeLit)
		if !ok || len(lit.Elts) != 0 {
			// A literal with fields set already carries identity; that is the fix.
			continue
		}
		if ident, ok := lit.Type.(*ast.Ident); ok {
			return ident.Name, true
		}
	}
	return "", false
}

// primaryKeyWhere returns the first Where condition in the chain that constrains the
// primary key, if any.
//
// Only a STRING LITERAL is examined. A condition built into a variable
// (Where(where, args...)) is unreadable here, and that is the correct outcome rather
// than a gap: those are the genuinely dynamic bulk updates, where no single row is
// identified and an empty EntityPK is the truthful record.
func primaryKeyWhere(links []*ast.CallExpr) (string, bool) {
	for _, link := range links {
		sel, ok := link.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Where" || len(link.Args) == 0 {
			continue
		}
		lit, ok := link.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		if primaryKeyCondition.MatchString(text) {
			return text, true
		}
	}
	return "", false
}
