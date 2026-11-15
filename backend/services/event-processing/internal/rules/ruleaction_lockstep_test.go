// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/ruleaction"
)

// Discover constants from source, rather than a hand-maintained list that could
// forget the very new action this guard is meant to catch.
func TestRuleActionVocabularyLockstep(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range tree.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			actionType := false
			for _, spec := range group.Specs {
				value := spec.(*ast.ValueSpec)
				if value.Type != nil {
					id, ok := value.Type.(*ast.Ident)
					actionType = ok && id.Name == "ActionType"
				} else if len(value.Values) > 0 {
					actionType = false
				}
				namedAction := false
				for _, name := range value.Names {
					namedAction = namedAction || strings.HasPrefix(name.Name, "Action")
				}
				if !actionType && !namedAction {
					continue
				}
				if len(value.Values) != len(value.Names) {
					t.Fatal("new action constants need explicit classification")
				}
				for _, expr := range value.Values {
					lit, ok := expr.(*ast.BasicLit)
					if !ok {
						t.Fatalf("action constant needs classification: %s", file)
					}
					kind, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !ruleaction.Classified(kind) {
						t.Errorf("engine action %q is unclassified", kind)
					}
					set, err := ruleaction.Kinds([]byte(`{"actions":[{"type":` + strconv.Quote(kind) + `}]}`))
					if err != nil || set.Unknown || !set.Types[kind] {
						t.Fatalf("known action %q: %+v %v", kind, set, err)
					}
					count++
				}
			}
		}
	}
	if count < 4 {
		t.Fatalf("found only %d action constants", count)
	}
	if ruleaction.Classified("futureAction") {
		t.Fatal("unknown negative control must fail closed")
	}
}

func TestRuleActionScanSupersetOfEngine(t *testing.T) {
	corpus := [][]byte{
		[]byte(`{"Actions":[{"type":"sendCommand"}]}`),
		[]byte(`{"ACTION\u017f":[{"TYPE":"sendCommand"}]}`),
		[]byte(`{"actions":[{"type":"sendCommand"}],"actions":[{"type":"publish"}]}`),
		[]byte(`{"actions":[{"type":"sendCommand","type":"httpCall"}]}`),
		[]byte(`{"actions":[{"type":"raiseAlarm","sendCommand":{}}]}`),
		[]byte(`{"actions":[{"type":"raiseAlarm","publish":{},"httpCall":{}}]}`),
	}
	fixtures := 0
	err := filepath.WalkDir("testdata", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		corpus = append(corpus, raw)
		fixtures++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixtures == 0 {
		t.Fatal("no engine JSON fixtures tested")
	}
	for i, raw := range corpus {
		rule, err := Decode(raw)
		if err != nil {
			t.Fatalf("fixture %d must decode: %v", i, err)
		}
		set, err := ruleaction.Kinds(raw)
		if err != nil {
			t.Fatalf("scan fixture %d: %v", i, err)
		}
		for _, action := range rule.Actions {
			for _, kind := range append([]ActionType{action.Type}, action.populatedVariants()...) {
				if !set.Types[string(kind)] {
					t.Errorf("fixture %d misses executable %q: %s", i, kind, raw)
				}
			}
		}
	}
}
