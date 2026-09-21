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
	"strconv"
	"strings"
)

// unqualifiedOrder is one DefaultOrder implementation whose ORDER BY names a column
// without saying which table it belongs to.
//
// The receiver type is carried rather than a line number for the reason the sibling
// scanners carry function names: a line number turns every unrelated edit above the
// method into a failure here, which teaches people to re-run and paste rather than read.
type unqualifiedOrder struct {
	File   string // absolute path
	Pos    string // file:line:col of the method, for the failure message
	Type   string // the receiver type whose order this is
	Order  string // the clause as written
	Reason string // what is wrong with it
}

// orderMethod is the rdb.Sortable method this guard reads, matched by NAME.
//
// By name, and not by the interface, because the implementations live in modules that
// core does not import — the same constraint that shapes the other repository-wide scans
// here. The limit worth stating is the other side of it: a same-named method on a type
// that is not a Sortable would also be read. Every DefaultOrder in the tree today is one.
const orderMethod = "DefaultOrder"

// unqualifiedOrdersUnder walks root and reports every DefaultOrder whose columns are not
// table-qualified, along with the set of directories the walk descended into.
//
// 🔴 WHY QUALIFICATION IS THE RULE AND NOT A STYLE PREFERENCE. ListOf applies the clause
// with .Order(mdl.DefaultOrder()), and the statement it lands on is not always a single
// table: a filters closure is free to add a join, and several already add a subquery over
// a second table. An unqualified `id DESC` against a joined statement is not a slow query
// or a wrong order — Postgres refuses it outright as an ambiguous column reference, at
// runtime, on whichever read first grew the join.
//
// 🔑 AND THE REASON IT IS A SCAN. When this was written all but ONE implementation in the
// tree already qualified, and that one had been correct by accident for as long as nothing
// joined against it. A convention followed by all but one is not a convention anybody is
// checking, and the next model declares its order by copying whichever neighbour its
// author happens to open.
//
// 🔴 NO COUNT IS WRITTEN DOWN HERE, DELIBERATELY. Two attempts to state one in this comment
// were wrong — the second because it was taken from a grep, which counted nine DefaultOrder
// lines that live inside RAW STRING FIXTURES in this package's own tests and are not
// declarations at all. The parser and the grep disagree, and the parser is the thing that
// runs. So the total is RETURNED and the repository test reports it; a number in prose here
// would only be checkable by writing this scanner a second time.
//
// 🔴 WHAT THIS DOES NOT CHECK: that the qualifier is the RIGHT table. It requires a dot, so
// a clause naming some other table's name passes here and still fails at runtime, with the
// missing-FROM-clause error rather than the ambiguous-column one. Resolving that would mean
// reading each model's TableName() (or gorm's default naming) and comparing — worth doing if
// this ever fires for that reason, and not worth pretending is already covered. Per-query
// .Order() calls inside filters closures are invisible to it for the same reason: they are
// not on the Sortable.
func unqualifiedOrdersUnder(root string) ([]unqualifiedOrder, int, map[string]bool, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, nil, err
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
		// Test files are included on purpose, unlike the read-pacing scan. A fixture model
		// declares an order the same way a real one does, and a fixture that orders by an
		// unqualified column is how the shape gets copied into the next real model.
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, 0, nil, err
	}
	if len(files) == 0 {
		return nil, 0, nil, fmt.Errorf("no .go files found under %s, so the scan asserts nothing", abs)
	}

	var found []unqualifiedOrder
	total := 0
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isDefaultOrderMethod(fn) {
				continue
			}
			total++
			order, literal := returnedStringLiteral(fn)
			pos := fset.Position(fn.Pos()).String()
			if !literal {
				found = append(found, unqualifiedOrder{
					File: file, Pos: pos, Type: receiverTypeName(fn),
					Reason: "its body is not a single returned string literal, so this guard " +
						"cannot read the clause it produces; spell the order out as a literal " +
						"or the qualification rule goes unchecked here",
				})
				continue
			}
			if reason := unqualifiedReason(order); reason != "" {
				found = append(found, unqualifiedOrder{
					File: file, Pos: pos, Type: receiverTypeName(fn),
					Order: order, Reason: reason,
				})
			}
		}
	}
	if total == 0 {
		return nil, 0, nil, fmt.Errorf("no %s methods found under %s: the scan parsed files but "+
			"recognized none of them, which reports clean for the same reason a broken matcher "+
			"would", orderMethod, abs)
	}
	return found, total, visited, nil
}

// isDefaultOrderMethod reports whether fn is a DefaultOrder implementation.
//
// It checks the SIGNATURE, not just the name: no parameters, exactly one string result,
// and a receiver. A package-level helper called DefaultOrder, or a same-named method
// taking arguments, is something else and is not this guard's business.
func isDefaultOrderMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || fn.Name == nil || fn.Name.Name != orderMethod {
		return false
	}
	if fn.Type.Params != nil && len(fn.Type.Params.List) != 0 {
		return false
	}
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	id, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "string"
}

// receiverTypeName returns the name of fn's receiver type, with any pointer stripped.
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "(unknown)"
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return "(unknown)"
}

// returnedStringLiteral returns the single string literal fn returns, and whether the
// body was exactly that. A body doing anything else reports false, which the caller
// records rather than skips.
func returnedStringLiteral(fn *ast.FuncDecl) (string, bool) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	lit, ok := ret.Results[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	// The error branch is UNREACHABLE for a token.STRING literal — go/scanner has already
	// accepted the quoting (and strips a stray CR) by the time an *ast.BasicLit exists with
	// that Kind. Mutation testing flagged it as an equivalent mutant rather than a gap, and
	// it is kept as an error return rather than a discard so a future Kind relaxation above
	// cannot turn a malformed literal into a silently empty clause.
	unquoted, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return unquoted, true
}

// unqualifiedReason reports why an ORDER BY clause fails the qualification rule, or "" if
// every column in it names its table.
//
// 🔴 IT REFUSES A CLAUSE IT CANNOT SPLIT RATHER THAN PASSING IT. The terms are separated
// on commas, so a call expression — COALESCE(a, b) — would be torn in half and its pieces
// examined as if they were columns. Nothing in the tree writes one today. If something
// does, the honest answer is that this guard does not understand the clause, not that the
// clause is fine: a scanner's failure mode is reporting clean.
func unqualifiedReason(order string) string {
	if strings.TrimSpace(order) == "" {
		return "the clause is empty, so ListOf would apply no ORDER BY at all and the " +
			"model's pages could repeat and skip rows"
	}
	if strings.ContainsAny(order, "()") {
		return "the clause contains a call expression, which this guard cannot split on " +
			"commas without tearing it apart; qualification here has to be checked by hand"
	}
	var bad []string
	for _, term := range strings.Split(order, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		column := strings.Fields(term)[0]
		if !strings.Contains(column, ".") {
			bad = append(bad, column)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("names %s without a table, so the clause is ambiguous the moment "+
		"this model is read through a statement carrying a join", strings.Join(bad, " and "))
}
