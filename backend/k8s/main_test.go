// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

type recordingRegistrar struct {
	names []string
	fail  string
}

func TestMainPassesExecutorModeToRegistration(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "registerComponents" {
				return true
			}
			if len(call.Args) != 2 {
				t.Fatal("wrong registration arguments")
			}
			arg, ok := call.Args[0].(*ast.StarExpr)
			if !ok {
				t.Fatal("startup must pass the executor flag")
			}
			flag, ok := arg.X.(*ast.Ident)
			if !ok || flag.Name != "executorMode" {
				t.Fatal("startup passed the wrong mode")
			}
			found = true
			return true
		})
	}
	if !found {
		t.Fatal("main never registers components")
	}
}

func (r *recordingRegistrar) Register(name string) error {
	r.names = append(r.names, name)
	if name == r.fail {
		return fmt.Errorf("registration failed: %s", name)
	}
	return nil
}

func TestRegisterComponents(t *testing.T) {
	for _, tc := range []struct {
		mode bool
		want []string
	}{
		{true, []string{"executor"}},
		{false, []string{"instance", "snapshot-retention"}},
	} {
		r := &recordingRegistrar{}
		if err := registerComponents(tc.mode, r); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.names, tc.want) {
			t.Fatalf("mode %v registered %v, want %v", tc.mode, r.names, tc.want)
		}
	}
	r := &recordingRegistrar{fail: "instance"}
	if err := registerComponents(false, r); err == nil || len(r.names) != 1 {
		t.Fatalf("registration must stop on error: %v, %v", r.names, err)
	}
}
