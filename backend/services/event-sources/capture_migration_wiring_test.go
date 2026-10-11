// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// A capture stream created before it became a work queue is only recreated when the service
// asks for it, and only a running source can drain it. Nothing exercises main.go, so this
// reads it: afterMicroserviceStarted must start every source and THEN call
// MigratePendingRetention, and must not swallow its error.
func TestStartupMigratesTheCaptureStreamAfterTheSourcesStart(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err)

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "afterMicroserviceStarted" {
			fn = f
		}
	}
	require.NotNil(t, fn, "afterMicroserviceStarted not found")

	var startPos, migratePos token.Pos
	var migrateChecked bool
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Start":
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "source" {
						startPos = x.Pos()
					}
				case "MigratePendingRetention":
					migratePos = x.Pos()
				}
			}
		case *ast.IfStmt:
			if init, ok := x.Init.(*ast.AssignStmt); ok && len(init.Rhs) == 1 {
				if call, ok := init.Rhs[0].(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "MigratePendingRetention" {
						for _, s := range x.Body.List {
							if _, ok := s.(*ast.ReturnStmt); ok {
								migrateChecked = true
							}
						}
					}
				}
			}
		}
		return true
	})
	require.NotZero(t, startPos, "the sources are no longer started in afterMicroserviceStarted")
	require.NotZero(t, migratePos, "afterMicroserviceStarted no longer calls MigratePendingRetention")
	require.Greater(t, int(migratePos), int(startPos), "the migration must run after the sources start: they are what drains the stream")
	require.True(t, migrateChecked, "the migration error must stop the service")
}
