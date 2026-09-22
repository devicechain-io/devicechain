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
	"strings"
)

// handRolledReadLoop is one function that reads from a message reader itself rather than
// handing the job to messaging.RunConsumer.
type handRolledReadLoop struct {
	File     string // absolute path, carried separately so callers need not parse Pos
	Pos      string // file:line:col of the read itself, for the failure message
	Function string // the enclosing function's name
}

// consumerLoopHome is where the one read loop is allowed to live, as a repo-relative path.
const consumerLoopHome = "backend/core/messaging/consumer.go"

// handRolledReadLoopsUnder walks root and reports every production function that calls the
// reader's read method for itself, along with the set of directories the walk descended into.
//
// 🔴 WHY THIS IS A GUARD AND NOT A NOTE IN THE COMMIT MESSAGE. core.ReadPacer has a TWO-call
// contract — PauseAfterError on the error path and Succeeded after a good read — and the
// pacing guard next door checks only the first, by its own admission: a loop that paces but
// never resets is a different defect with a different fix. A hand-rolled loop is therefore
// free to honour half the contract and pass everything, and the half it is free to omit is
// the one that ends a HEALTHY process, hours later, for faults it recovered from. Nineteen
// copies of that loop existed across ten services and all nineteen happened to be right.
// This is what stops the twentieth from being the one that is not, by leaving nowhere to
// write it.
//
// 🔴 IT SCANS PRODUCTION ONLY. A test may read a message itself — several drive a handler
// one message at a time on purpose, and saying so is the point of those tests.
//
// Two consequences worth knowing before this fails on you, both shared with the guards next
// door:
//
//   - It reads files outside this module, so `-count=1` is load-bearing. Go's test cache
//     does not track them, and a cached PASS would survive a loop added elsewhere.
//   - A loop added in a service module is reported by THIS module's test run. The message
//     names the offending file, line and function.
func handRolledReadLoopsUnder(root string) ([]handRolledReadLoop, map[string]bool, error) {
	var found []handRolledReadLoop
	visited := map[string]bool{}
	parsed := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "_legacy", "node_modules", "vendor", ".git":
				return filepath.SkipDir
			}
			abs, aerr := filepath.Abs(path)
			if aerr != nil {
				return aerr
			}
			visited[abs] = true
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		// Mode 0 parses no comments on purpose. An earlier guard in this tree matched its
		// subject inside a COMMENT that explained why the code did not do the thing, and
		// reported the explanation as the offence.
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parsing %s: %w", path, perr)
		}
		parsed++
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != readMethod {
					return true
				}
				found = append(found, handRolledReadLoop{
					File:     path,
					Pos:      fset.Position(call.Pos()).String(),
					Function: funcName(fd),
				})
				return true
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// 🔴 A SCAN THAT PARSED NOTHING REPORTS NO FINDINGS, WHICH READS EXACTLY LIKE A CLEAN
	// TREE. Silence is this guard's failure mode, so it refuses rather than returning it.
	if parsed == 0 {
		return nil, nil, fmt.Errorf("scanning %s parsed no Go files at all; a scan that read "+
			"nothing cannot report that nothing is wrong", root)
	}
	return found, visited, nil
}

// funcName renders a declaration as Receiver.Name, or Name for a plain function.
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return receiverTypeName(fd) + "." + fd.Name.Name
}
