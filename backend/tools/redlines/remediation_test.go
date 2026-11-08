// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package redlines

import (
	"strings"
	"testing"
)

const remMod = "module example.com/m\n\ngo 1.26\n"

func remScan(t *testing.T, files map[string]string) *RemResult {
	t.Helper()
	files["go.mod"] = remMod
	tree(t, files)
	res, err := ScanRemediation(".")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func wantRem(t *testing.T, res *RemResult, file string, line int, reason string) {
	t.Helper()
	for _, f := range res.Findings {
		if f.File == file && f.Line == line && strings.Contains(f.Reason, reason) {
			return
		}
	}
	t.Fatalf("no finding %s:%d containing %q; got %v", file, line, reason, res.Findings)
}

func lineOf(t *testing.T, src, at string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, at) {
			return i + 1
		}
	}
	t.Fatalf("source has no line containing %q", at)
	return 0
}

func TestRemediationRefusedShapes(t *testing.T) {
	const hdr = "package p\n\nimport (\n\t\"fmt\"\n\t\"example.com/m/agenttext\"\n)\n\nvar _ = fmt.Sprint\nvar _ agenttext.Remediation\n\n"
	cases := []struct {
		name, body, reason, at string
	}{
		{"sprintf in keyed field", "type E struct{ Remediation agenttext.Remediation }\n\nfunc f(id string) E {\n\treturn E{Remediation: agenttext.Remediation(fmt.Sprintf(\"fix %s\", id))}\n}\n", "a call to Sprintf", `Remediation: agenttext.Remediation(fmt`},
		{"variable in conversion", "func f(s string) agenttext.Remediation {\n\treturn agenttext.Remediation(s)\n}\n", "identifier s is not a constant", `return agenttext.Remediation(s)`},
		{"concat with variable", "func f(s string) agenttext.Remediation {\n\treturn agenttext.Remediation(\"retry \" + s)\n}\n", "identifier s is not a constant", `"retry " + s`},
		{"assignment to field", "type E struct{ Remediation string }\n\nfunc f(e *E, s string) {\n\te.Remediation = s\n}\n", "assignment to .Remediation", `e.Remediation = s`},
		{"assignment from call", "type E struct{ Remediation string }\n\nfunc f(e *E, s string) {\n\te.Remediation = strings(s)\n}\nfunc strings(s string) string { return s }\n", "a call to strings", `e.Remediation = strings(s)`},
		{"setter", "type E struct{}\n\nfunc (E) SetRemediation(string) {}\nfunc f(e E, s string) {\n\te.SetRemediation(s)\n}\n", "argument of SetRemediation", `e.SetRemediation(s)`},
		{"WithRemediation", "func WithRemediation(string) {}\nfunc f(s string) { WithRemediation(s) }\n", "argument of WithRemediation", `WithRemediation(s) }`},
		{"var declaration", "var s = \"x\"\nvar Remediation = s\n", "declaration of Remediation", `var Remediation = s`},
		{"typed var", "func f(s string) {\n\tvar r agenttext.Remediation = agenttext.Remediation(s)\n\t_ = r\n}\n", "identifier s", `var r agenttext.Remediation =`},
		{"function value", "func f() {\n\tc := agenttext.Remediation\n\t_ = c\n}\n", "bare reference to Remediation", `c := agenttext.Remediation`},
		{"method on local value", "type E struct{ Remediation string }\n\nfunc f(e *E, o E) {\n\te.Remediation = o.Remediation\n}\n", "reads a field or method of a local value", `e.Remediation = o.Remediation`},
		{"index expr", "type E struct{ Remediation string }\n\nfunc f(m []string) E {\n\treturn E{Remediation: m[0]}\n}\n", "IndexExpr", `m[0]`},
		{"unkeyed literal", "type E struct {\n\tCode string\n\tHint agenttext.Remediation\n}\n\nfunc f(s string) E {\n\treturn E{\"c\", agenttext.Remediation(s)}\n}\n", "identifier s", `return E{"c"`},
		{"local var shadowing a package const", "const msg = \"x\"\n\nfunc f(s string) E2 {\n\tmsg := s\n\treturn E2{Remediation: msg}\n}\ntype E2 struct{ Remediation string }\n", "identifier msg is not a constant", `return E2{Remediation: msg}`},
		{"minus", "type E struct{ Remediation string }\n\nvar _ = E{Remediation: \"a\" - \"b\"}\n", "operator -", `"a" - "b"`},
		{"multi value assign", "type E struct{ Remediation string }\n\nfunc g() (string, int) { return \"\", 0 }\nfunc f(e *E) {\n\tvar n int\n\te.Remediation, n = g()\n\t_ = n\n}\n", "multi-value", `e.Remediation, n = g()`},
		{"var const name not const", "var v = \"x\"\n\nfunc f() agenttext.Remediation { return agenttext.Remediation(v) }\n", "identifier v is not a constant", `agenttext.Remediation(v)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := remScan(t, map[string]string{"x.go": hdr + c.body, "agenttext/a.go": "package agenttext\n\ntype Remediation string\n"})
			wantRem(t, res, "x.go", lineOf(t, hdr+c.body, c.at), c.reason)
		})
	}
}

func TestRemediationAcceptedShapes(t *testing.T) {
	const hdr = "package p\n\nimport \"example.com/m/agenttext\"\n\n"
	cases := map[string]string{
		"literal":           "var R = agenttext.Remediation(\"Retry later.\")\n",
		"const ident":       "const hint = \"Retry later.\"\n\nvar R = agenttext.Remediation(hint)\n",
		"local const":       "func f() agenttext.Remediation {\n\tconst h = \"x\"\n\treturn agenttext.Remediation(h)\n}\n",
		"const concat":      "const a = \"x \"\nconst b = \"y\"\n\nvar R = agenttext.Remediation(a + b + \"z\")\n",
		"parens":            "var R = agenttext.Remediation((\"x\"))\n",
		"keyed literal":     "type E struct{ Remediation agenttext.Remediation }\n\nvar _ = E{Remediation: \"Retry later.\"}\n",
		"keyed const":       "const h = \"x\"\n\ntype E struct{ Remediation string }\n\nvar _ = E{Remediation: h}\n",
		"cross package":     "import \"example.com/m/texts\"\n\nvar R = agenttext.Remediation(texts.Retry)\n",
		"assign literal":    "type E struct{ Remediation string }\n\nfunc f(e *E) { e.Remediation = \"x\" }\n",
		"string conversion": "const h = \"x\"\n\ntype E struct{ Remediation string }\n\nvar _ = E{Remediation: string(h)}\n",
		"unkeyed constant":  "type E struct {\n\tCode string\n\tHint agenttext.Remediation\n}\n\nvar _ = E{\"c\", \"fix it\"}\n",
		"reading the field": "type E struct{ Remediation string }\n\nfunc f(e E) string { return e.Remediation }\n",
		"type positions":    "type E struct{ R agenttext.Remediation }\n\nfunc f(r agenttext.Remediation, rs []agenttext.Remediation, m map[string]agenttext.Remediation) *agenttext.Remediation { return &r }\n",
		"method named":      "type E struct{}\n\nfunc (E) Remediation() string { return \"x\" }\n\nfunc g(e E) string { return e.Remediation() }\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			src := hdr + body
			if strings.HasPrefix(body, "import ") {
				src = "package p\n\nimport (\n\t\"example.com/m/agenttext\"\n\t\"example.com/m/texts\"\n)\n\n" + strings.TrimPrefix(body, "import \"example.com/m/texts\"\n\n")
			}
			res := remScan(t, map[string]string{
				"x.go":           src,
				"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
				"texts/t.go":     "package texts\n\nconst Retry = \"Retry later.\"\n",
			})
			if len(res.Findings) != 0 {
				t.Fatalf("false positive: %v", res.Findings)
			}
		})
	}
}

func TestRemediationCrossPackageNonConstIsRefused(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport (\n\t\"example.com/m/agenttext\"\n\t\"example.com/m/texts\"\n)\n\nvar R = agenttext.Remediation(texts.Dynamic)\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
		"texts/t.go":     "package texts\n\nvar Dynamic = \"x\"\n",
	})
	wantRem(t, res, "x.go", 8, "not a package-level const")

	res = remScan(t, map[string]string{
		"x.go":           "package p\n\nimport (\n\t\"example.com/m/agenttext\"\n\t\"os\"\n)\n\nvar R = agenttext.Remediation(os.Args[0])\nvar S = agenttext.Remediation(os.PathSeparator)\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
	})
	if len(res.Findings) == 0 {
		t.Fatal("a stdlib value must not be accepted: only constants of packages in the tree are resolvable")
	}
}

func TestRemediationUnkeyedLiteralAcrossPackages(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport (\n\t\"example.com/m/agenttext\"\n\t\"example.com/m/errs\"\n)\n\nfunc f(s string) errs.E {\n\treturn errs.E{\"c\", agenttext.Remediation(s)}\n}\n",
		"errs/e.go":      "package errs\n\nimport \"example.com/m/agenttext\"\n\ntype E struct {\n\tCode string\n\tHint agenttext.Remediation\n}\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
	})
	wantRem(t, res, "x.go", 9, "identifier s")
}

func TestRemediationNonStringLiteralRefused(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go": "package p\n\ntype E struct{ Remediation string }\n\nvar _ = E{Remediation: 5}\n",
	})
	wantRem(t, res, "x.go", 5, "a non-string literal")
}

// An import path that merely starts with a module path is another module, and must not be
// resolved to a directory inside the first.
func TestRemediationModulePrefixLookalikeIsNotResolved(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport (\n\t\"example.com/m/agenttext\"\n\t\"example.com/mx/texts\"\n)\n\nvar R = agenttext.Remediation(texts.Retry)\n",
		"x/texts/t.go":   "package texts\n\nconst Retry = \"Retry later.\"\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
	})
	wantRem(t, res, "x.go", 8, "not a constant of a package in this repository")
}

// A name that is a const in one build configuration and a var in another is not accepted:
// the guard cannot tell which one the build uses, so it fails closed.
func TestRemediationConstAndVarOfTheSameNameRefused(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\nvar R = agenttext.Remediation(hint)\n",
		"hint_a.go":      "//go:build a\n\npackage p\n\nconst hint = \"x\"\n",
		"hint_b.go":      "//go:build !a\n\npackage p\n\nvar hint = \"x\"\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
	})
	wantRem(t, res, "x.go", 5, "does not resolve to a constant")
}

func TestRemediationAliasConversionIsWatched(t *testing.T) {
	const at = "package agenttext\n\ntype Remediation string\n"
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\ntype Hint = agenttext.Remediation\n\nfunc hint(s string) Hint { return Hint(s) }\n",
		"agenttext/a.go": at,
	})
	wantRem(t, res, "x.go", 7, "identifier s")

	// An alias of an alias, declared in another package, is the same type.
	res = remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/hints\"\n\nfunc hint(s string) hints.Second { return hints.Second(s) }\n",
		"hints/h.go":     "package hints\n\nimport \"example.com/m/agenttext\"\n\ntype First = agenttext.Remediation\ntype Second = First\n",
		"agenttext/a.go": at,
	})
	wantRem(t, res, "x.go", 5, "identifier s")

	// A constant through the alias is fine.
	res = remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\ntype Hint = agenttext.Remediation\n\nvar H = Hint(\"Retry later.\")\n",
		"agenttext/a.go": at,
	})
	if len(res.Findings) != 0 {
		t.Fatalf("false positive: %v", res.Findings)
	}
}

func TestRemediationLocalAliasAndPointerConversionAreWatched(t *testing.T) {
	const at = "package agenttext\n\ntype Remediation string\n"
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\nfunc f(s string) agenttext.Remediation {\n\ttype Hint = agenttext.Remediation\n\treturn Hint(s)\n}\n",
		"agenttext/a.go": at,
	})
	wantRem(t, res, "x.go", 7, "identifier s")

	res = remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\nfunc f(s string) agenttext.Remediation {\n\treturn *(*agenttext.Remediation)(&s)\n}\n",
		"agenttext/a.go": at,
	})
	wantRem(t, res, "x.go", 6, "UnaryExpr")

	res = remScan(t, map[string]string{
		"x.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\nfunc f(s []string) []agenttext.Remediation {\n\treturn []agenttext.Remediation(s)\n}\n",
		"agenttext/a.go": at,
	})
	wantRem(t, res, "x.go", 6, "identifier s")
}

func TestRemediationNamedAndElidedMapLiteralsAreNotFields(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":      "package p\n\nimport \"example.com/m/maps\"\n\nconst Remediation = \"remediation\"\n\ntype Labels map[string]string\n\nfunc a(v string) Labels { return Labels{Remediation: v} }\n\nfunc b(v string) map[string]map[string]string {\n\treturn map[string]map[string]string{\"k\": {Remediation: v}}\n}\n\nfunc c(v string) []map[string]string { return []map[string]string{{Remediation: v}} }\n\nfunc d(v string) maps.Labels { return maps.Labels{Remediation: v} }\n",
		"maps/m.go": "package maps\n\ntype Labels map[string]string\n",
	})
	if len(res.Findings) != 0 {
		t.Fatalf("false positive on a map literal: %v", res.Findings)
	}
	// The same shapes of a struct are still watched.
	res = remScan(t, map[string]string{
		"x.go": "package p\n\ntype E struct{ Remediation string }\n\nfunc a(v string) []E { return []E{{Remediation: v}} }\n",
	})
	wantRem(t, res, "x.go", 5, "field Remediation")
}

func TestRemediationMapLiteralKeyIsNotAField(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go": "package p\n\nconst Remediation = \"remediation\"\n\nfunc f(v string) map[string]string {\n\treturn map[string]string{Remediation: v}\n}\n",
	})
	if len(res.Findings) != 0 {
		t.Fatalf("false positive on a map literal: %v", res.Findings)
	}
}

func TestRemediationDotImportRefused(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":           "package p\n\nimport . \"example.com/m/agenttext\"\n\nvar R = Remediation(\"x\")\n",
		"agenttext/a.go": "package agenttext\n\ntype Remediation string\n",
	})
	wantRem(t, res, "x.go", 3, "dot-import")
}

func TestRemediationSkipsTestsFixturesAndCountsSites(t *testing.T) {
	res := remScan(t, map[string]string{
		"x.go":                "package p\n\nimport \"example.com/m/agenttext\"\n\nvar A = agenttext.Remediation(\"a\")\nvar B = agenttext.Remediation(\"b\")\n",
		"x_test.go":           "package p\n\nimport \"example.com/m/agenttext\"\n\nfunc f(s string) { _ = agenttext.Remediation(s) }\n",
		"testdata/fixture.go": "package p\n\nfunc f(s string) { _ = Remediation(s) }\n",
		"_legacy/old.go":      "package p\n\nfunc f(s string) { _ = Remediation(s) }\n",
		"agenttext/a.go":      "package agenttext\n\ntype Remediation string\n",
	})
	if len(res.Findings) != 0 {
		t.Fatalf("findings = %v", res.Findings)
	}
	if res.Sites != 2 {
		t.Fatalf("sites = %d, want 2", res.Sites)
	}
	if res.GoFiles != 2 {
		t.Fatalf("go files = %d, want 2 (x.go and agenttext/a.go)", res.GoFiles)
	}
}

func TestRemediationUnparseableIsAnError(t *testing.T) {
	tree(t, map[string]string{"go.mod": remMod, "x.go": "package p\nfunc (\n"})
	if _, err := ScanRemediation("."); err == nil {
		t.Fatal("an unparseable file must be an error")
	}
	if _, err := ScanRemediation("nowhere"); err == nil {
		t.Fatal("a missing root must be an error")
	}
}

func TestImportName(t *testing.T) {
	for in, want := range map[string]string{
		"example.com/m/agenttext": "agenttext",
		"example.com/m/foo/v2":    "foo",
		"gopkg.in/yaml.v3":        "yaml",
		"example.com/m/go-thing":  "go_thing",
		"fmt":                     "fmt",
	} {
		if got := importName(in); got != want {
			t.Errorf("importName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleOf(t *testing.T) {
	tree(t, map[string]string{"a/go.mod": "// c\nmodule   \"example.com/q\" // trailing\n\ngo 1.26\n", "b/go.mod": "go 1.26\n"})
	if got := moduleOf("a/go.mod"); got != "example.com/q" {
		t.Fatalf("got %q", got)
	}
	if got := moduleOf("b/go.mod"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := moduleOf("missing/go.mod"); got != "" {
		t.Fatalf("got %q", got)
	}
}
