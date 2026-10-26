// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 🔴 THE ONE-TIME COMPARES ARE CONSTANT-TIME BY CONSTRUCTION, AND THIS TEST KEEPS THEM SO.
//
// hack/check-credential-compare.sh polices the rest of the tree and exempts this package,
// because this package IS the primitive. That leaves nothing watching the primitive's own
// code-compare, and a rewrite of VerifyTOTP to `if want == code` is the natural
// "simplification" that compiles, passes every functional test, and leaks how many leading
// digits of a guess were right. So the rule is pinned here, by parsing, in the shape of
// that script: inside the one-time compare functions, the only comparison is a call of
// constantTimeCompare, and no ==, != or ordinary string/bytes compare touches a code.

// oneTimeCompareFuncs are the functions that compare a presented one-time secret.
var oneTimeCompareFuncs = []string{"VerifyTOTP", "MatchRecoveryDigest"}

// secretNames are the identifiers those functions hold secrets in.
var secretNames = map[string]bool{"code": true, "want": true, "presented": true, "d": true, "stored": true, "digest": true}

// forbiddenCompares are calls that compare in variable time (or bypass the primitive's
// own, observable compare).
var forbiddenCompares = map[string]bool{
	"bytes.Equal": true, "bytes.Compare": true, "strings.EqualFold": true, "strings.Compare": true,
	"reflect.DeepEqual": true, "hmac.Equal": true, "subtle.ConstantTimeCompare": true, "slices.Equal": true,
}

// oneTimeCompareFindings returns what is wrong with src's one-time compare functions.
func oneTimeCompareFindings(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "totp.go", src, 0)
	require.NoError(t, err)
	var findings []string
	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !contains(oneTimeCompareFuncs, fn.Name.Name) {
			continue
		}
		found[fn.Name.Name] = true
		calls := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				switch f := x.Fun.(type) {
				case *ast.Ident:
					if f.Name == "constantTimeCompare" {
						calls++
					}
				case *ast.SelectorExpr:
					if id, ok := f.X.(*ast.Ident); ok && forbiddenCompares[id.Name+"."+f.Sel.Name] {
						findings = append(findings, fset.Position(x.Pos()).String()+": "+fn.Name.Name+" calls "+id.Name+"."+f.Sel.Name)
					}
				}
			case *ast.BinaryExpr:
				if x.Op == token.EQL || x.Op == token.NEQ {
					// `constantTimeCompare(a, b) == 1` is the compare itself, not a second one.
					if isConstantTimeCall(x.X) || isConstantTimeCall(x.Y) {
						return true
					}
					if touchesSecret(x.X) || touchesSecret(x.Y) {
						findings = append(findings, fset.Position(x.Pos()).String()+": "+fn.Name.Name+" compares a code with "+x.Op.String())
					}
				}
			}
			return true
		})
		if calls == 0 {
			findings = append(findings, fn.Name.Name+" never calls constantTimeCompare")
		}
	}
	for _, name := range oneTimeCompareFuncs {
		if !found[name] {
			findings = append(findings, name+" not found: the guard would be checking nothing")
		}
	}
	return findings
}

// touchesSecret reports whether e is, or indexes or converts, a secret-named identifier.
func touchesSecret(e ast.Expr) bool {
	hit := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && secretNames[id.Name] {
			hit = true
		}
		return !hit
	})
	return hit
}

// isConstantTimeCall reports whether e is a call of constantTimeCompare.
func isConstantTimeCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "constantTimeCompare"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestOneTimeComparesUseOnlyTheConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("totp.go")
	require.NoError(t, err)
	require.Empty(t, oneTimeCompareFindings(t, string(src)))
}

// The guard can fail: each planted rewrite of the compare is caught, so a clean result
// above means something.
func TestOneTimeCompareGuardCatchesPlantedRewrites(t *testing.T) {
	src, err := os.ReadFile("totp.go")
	require.NoError(t, err)
	clean := string(src)
	require.Empty(t, oneTimeCompareFindings(t, clean), "control: the real file is clean")

	mutate := func(from, to string) string {
		require.Contains(t, clean, from, "the mutation anchor moved; update the guard test")
		return strings.Replace(clean, from, to, 1)
	}
	for name, mutated := range map[string]string{
		"== on a code": mutate("m := constantTimeCompare([]byte(want), []byte(code))", "m := 0; if want == code { m = 1 }"),
		"bytes.Equal":  mutate("m := constantTimeCompare([]byte(want), []byte(code))", "m := 0; if bytes.Equal([]byte(want), []byte(code)) { m = 1 }"),
		"digest ==":    mutate("if constantTimeCompare([]byte(d), []byte(presented)) == 1 {", "if d == presented {"),
	} {
		require.NotEmpty(t, oneTimeCompareFindings(t, mutated), name)
	}
}
