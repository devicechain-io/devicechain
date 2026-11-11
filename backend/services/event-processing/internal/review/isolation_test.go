// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package review

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Inspect executable package files, including code behind build constraints. A
// dependency walk would also include evaluate's recorder/dispatcher dependencies;
// the review itself must never acquire a production sink or writer constructor.
func TestReviewIsolationImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			path, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{"dc-event-processing/internal/processor", "dc-microservice/svcclient", "dc-event-processing/internal/react"} {
				if strings.Contains(path, bad) {
					t.Fatalf("%s: forbidden import %s", entry.Name(), path)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && strings.Contains(id.Name, "MessageWriter") {
				t.Fatalf("%s: forbidden MessageWriter identifier %s", entry.Name(), id.Name)
			}
			return true
		})
	}
}
