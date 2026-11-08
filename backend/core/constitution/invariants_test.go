// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package constitution

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests read files OUTSIDE this module (the cited tests, docs/versions.json), which
// Go's test cache does not track, so run them with -count=1 (CI does).

// repoRoot walks up from the package directory to the directory holding go.work.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above the package directory; the index needs the repository root")
		}
		dir = parent
	}
}

// isTestFunc reports whether fn is something `go test` runs: no receiver, and exactly one
// parameter of type *testing.T.
func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

// testFuncsIn returns the names of the test functions declared in the _test.go files
// directly inside dir that the default build context plus tags selects, so a test moved
// behind a build tag its proof does not declare stops counting. A missing directory is an
// empty set, not an error: a moved package must read as a missing proof.
func testFuncsIn(dir string, tags []string) (map[string]bool, error) {
	ctx := build.Default
	ctx.BuildTags = tags
	names := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if match, err := ctx.MatchFile(dir, e.Name()); err != nil {
			return nil, err
		} else if !match {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && isTestFunc(fn) {
				names[fn.Name.Name] = true
			}
		}
	}
	return names, nil
}

// missingProofs is the verdict on every Exists proof: one line per proof that is not in
// the tree at root. checked counts the Exists proofs examined, so a caller can tell an
// empty verdict from an empty table.
func missingProofs(root string, index []Invariant) (missing []string, checked int, err error) {
	cache := map[string]map[string]bool{}
	for _, inv := range index {
		for _, p := range inv.Proofs {
			if p.Status != Exists {
				continue
			}
			checked++
			full := filepath.Join(root, filepath.FromSlash(p.Path))
			switch p.Kind {
			case GoTest:
				key := full + "|" + strings.Join(p.Tags, ",")
				funcs, ok := cache[key]
				if !ok {
					if funcs, err = testFuncsIn(full, p.Tags); err != nil {
						return nil, 0, err
					}
					cache[key] = funcs
				}
				if !funcs[p.Name] {
					missing = append(missing, fmt.Sprintf("%s: test %s is not declared in a _test.go file in %s", inv.ID, p.Name, p.Path))
				}
			case File:
				if fi, statErr := os.Stat(full); statErr != nil || fi.IsDir() {
					missing = append(missing, fmt.Sprintf("%s: %s (%s) does not exist", inv.ID, p.Name, p.Path))
				}
			default:
				missing = append(missing, fmt.Sprintf("%s: %s has an unknown proof kind %d", inv.ID, p.Name, p.Kind))
			}
		}
	}
	return missing, checked, nil
}

var (
	idPattern      = regexp.MustCompile(`^(CON|CR)-([1-9][0-9]*)$`)
	releasePattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)$`)
)

type release struct{ major, minor int }

func parseRelease(s string) (release, error) {
	m := releasePattern.FindStringSubmatch(s)
	if m == nil {
		return release{}, fmt.Errorf("%q is not MAJOR.MINOR", s)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return release{major, minor}, nil
}

func (r release) atOrBefore(o release) bool {
	return r.major < o.major || (r.major == o.major && r.minor <= o.minor)
}

func (r release) String() string { return fmt.Sprintf("%d.%d", r.major, r.minor) }

// malformed is the verdict on the table's shape: IDs unique and exactly 1..want[doc] per
// document, every invariant titled and proven by something, every proof complete for its
// status.
func malformed(index []Invariant, want map[string]int) []string {
	var out []string
	seen := map[string]bool{}
	numbers := map[string][]int{}
	for _, inv := range index {
		m := idPattern.FindStringSubmatch(inv.ID)
		if m == nil {
			out = append(out, fmt.Sprintf("%q is not a CON-n or CR-n id", inv.ID))
			continue
		}
		if seen[inv.ID] {
			out = append(out, fmt.Sprintf("%s is listed more than once", inv.ID))
			continue
		}
		seen[inv.ID] = true
		n, _ := strconv.Atoi(m[2])
		numbers[m[1]] = append(numbers[m[1]], n)
		if strings.TrimSpace(inv.Title) == "" {
			out = append(out, fmt.Sprintf("%s has no title", inv.ID))
		}
		if len(inv.Proofs) == 0 {
			out = append(out, fmt.Sprintf("%s names no proof, not even a scheduled one", inv.ID))
		}
		for _, p := range inv.Proofs {
			if strings.TrimSpace(p.Name) == "" {
				out = append(out, fmt.Sprintf("%s has a proof with no name", inv.ID))
			}
			if p.Kind != GoTest && p.Kind != File {
				out = append(out, fmt.Sprintf("%s: %s has an unknown proof kind %d", inv.ID, p.Name, p.Kind))
			}
			switch p.Status {
			case Exists:
				if p.Path == "" {
					out = append(out, fmt.Sprintf("%s: %s is marked EXISTS but names no path to check", inv.ID, p.Name))
				}
			case Scheduled:
				if strings.TrimSpace(p.Owner) == "" {
					out = append(out, fmt.Sprintf("%s: scheduled %s names no owner", inv.ID, p.Name))
				}
				if _, err := parseRelease(p.Target); err != nil {
					out = append(out, fmt.Sprintf("%s: scheduled %s has a bad target: %v", inv.ID, p.Name, err))
				}
			default:
				out = append(out, fmt.Sprintf("%s: %s has an unknown status %d", inv.ID, p.Name, p.Status))
			}
		}
	}
	// The expected set comes from the documents, not from the table, so an article dropped
	// from the END of the table is caught as surely as one dropped from the middle.
	for _, doc := range []string{"CON", "CR"} {
		have := map[int]bool{}
		for _, n := range numbers[doc] {
			have[n] = true
			if n > want[doc] {
				out = append(out, fmt.Sprintf("%s-%d is beyond the %d the document declares; update the count with the document", doc, n, want[doc]))
			}
		}
		for i := 1; i <= want[doc]; i++ {
			if !have[i] {
				out = append(out, fmt.Sprintf("%s-%d is missing from the index", doc, i))
			}
		}
	}
	slices.Sort(out)
	return out
}

// overdue is the verdict on every Scheduled proof: one line per proof whose target release
// is at or before the newest shipped one.
func overdue(index []Invariant, shipped release) []string {
	var out []string
	for _, inv := range index {
		for _, p := range inv.Proofs {
			if p.Status != Scheduled {
				continue
			}
			target, err := parseRelease(p.Target)
			if err != nil {
				continue // malformed reports it
			}
			if target.atOrBefore(shipped) {
				out = append(out, fmt.Sprintf("%s: %s was due in %s and %s has shipped; build it, or re-target it here with a reason (owner: %s)",
					inv.ID, p.Name, target, shipped, p.Owner))
			}
		}
	}
	return out
}

// shippedRelease reads the newest frozen docs snapshot. A release freezes its docs
// version as part of shipping, so docs/versions.json is the tree's own record of what has
// shipped; the VERSION file is a build placeholder, not a release number.
func shippedRelease(root string) (release, error) {
	raw, err := os.ReadFile(filepath.Join(root, "docs", "versions.json"))
	if err != nil {
		return release{}, err
	}
	var versions []string
	if err := json.Unmarshal(raw, &versions); err != nil {
		return release{}, fmt.Errorf("docs/versions.json: %w", err)
	}
	if len(versions) == 0 {
		return release{}, fmt.Errorf("docs/versions.json lists no shipped version")
	}
	var newest release
	for i, v := range versions {
		r, err := parseRelease(v)
		if err != nil {
			return release{}, fmt.Errorf("docs/versions.json: %w", err)
		}
		if i == 0 || newest.atOrBefore(r) {
			newest = r
		}
	}
	return newest, nil
}

// withProof returns a deep copy of index with the proof at (inv, proof) replaced by edit's
// result, so a control can plant one defect in the real table and nothing else.
func withProof(index []Invariant, id string, match func(Proof) bool, edit func(Proof) Proof) ([]Invariant, bool) {
	out := make([]Invariant, len(index))
	found := false
	for i, inv := range index {
		out[i] = inv
		out[i].Proofs = slices.Clone(inv.Proofs)
		if inv.ID != id {
			continue
		}
		for j, p := range out[i].Proofs {
			if !found && match(p) {
				out[i].Proofs[j] = edit(p)
				found = true
			}
		}
	}
	return out, found
}

func TestEveryExistingProofIsLive(t *testing.T) {
	root := repoRoot(t)

	// Negative controls first: each plants ONE defect in a copy of the real table, and the
	// verdict must name exactly that one.
	plant := func(t *testing.T, id string, match func(Proof) bool, edit func(Proof) Proof) []string {
		t.Helper()
		planted, ok := withProof(Invariants(), id, match, edit)
		if !ok {
			t.Fatalf("no proof of %s matched the plant; the control is not planting anything", id)
		}
		missing, _, err := missingProofs(root, planted)
		if err != nil {
			t.Fatal(err)
		}
		return missing
	}
	isExistingTest := func(p Proof) bool { return p.Status == Exists && p.Kind == GoTest }

	t.Run("a renamed cited test is reported", func(t *testing.T) {
		missing := plant(t, "CON-10", isExistingTest, func(p Proof) Proof { p.Name += "Renamed"; return p })
		if len(missing) != 1 || !strings.Contains(missing[0], "TestReplayCorrectnessRenamed") {
			t.Fatalf("missing = %v; want exactly the renamed test", missing)
		}
	})
	t.Run("a cited test in the wrong package is reported", func(t *testing.T) {
		missing := plant(t, "CON-10", isExistingTest, func(p Proof) Proof { p.Path = "backend/core/auth"; return p })
		if len(missing) != 1 || !strings.Contains(missing[0], "TestReplayCorrectness") {
			t.Fatalf("missing = %v; want exactly the moved test", missing)
		}
	})
	t.Run("a cited test in a directory that does not exist is reported", func(t *testing.T) {
		missing := plant(t, "CON-6", isExistingTest, func(p Proof) Proof { p.Path += "-gone"; return p })
		if len(missing) != 1 {
			t.Fatalf("missing = %v; want exactly one", missing)
		}
	})
	t.Run("a missing cited file is reported", func(t *testing.T) {
		missing := plant(t, "CON-12", func(p Proof) bool { return p.Kind == File }, func(p Proof) Proof { p.Path += ".gone"; return p })
		if len(missing) != 1 || !strings.Contains(missing[0], "CON-12") {
			t.Fatalf("missing = %v; want exactly the CON-12 guard", missing)
		}
	})
	t.Run("a non-test function or a method of the same name does not count", func(t *testing.T) {
		dir := t.TempDir()
		src := "package p\n\nimport \"testing\"\n\ntype s struct{}\n\nfunc (s) TestMethod(t *testing.T) {}\n\n" +
			"func TestWrongParam(x int) {}\n\nfunc TestTwoParams(t *testing.T, x int) {}\n\nfunc TestBenchShape(b *testing.B) {}\n"
		if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\n\nfunc TestInProd() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		index := []Invariant{{ID: "CON-1", Title: "x", Proofs: []Proof{
			exists(".", "TestMethod"), exists(".", "TestInProd"), exists(".", "TestWrongParam"),
			exists(".", "TestTwoParams"), exists(".", "TestBenchShape"),
		}}}
		missing, checked, err := missingProofs(dir, index)
		if err != nil {
			t.Fatal(err)
		}
		if checked != 5 || len(missing) != 5 {
			t.Fatalf("checked %d, missing = %v; want all five reported", checked, missing)
		}
	})
	t.Run("a test behind a build tag counts only when its proof declares the tag", func(t *testing.T) {
		dir := t.TempDir()
		src := "//go:build integration\n\npackage p\n\nimport \"testing\"\n\nfunc TestTagged(t *testing.T) {}\n\nfunc TestReal(t *testing.T) {}\n"
		if err := os.WriteFile(filepath.Join(dir, "c_test.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "d_test.go"), []byte("package p\n\nimport \"testing\"\n\nfunc TestPlain(t *testing.T) {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		untagged := []Invariant{{ID: "CON-1", Title: "x", Proofs: []Proof{exists(".", "TestTagged"), exists(".", "TestPlain")}}}
		if missing, _, err := missingProofs(dir, untagged); err != nil || len(missing) != 1 || !strings.Contains(missing[0], "TestTagged") {
			t.Fatalf("untagged: missing = %v, %v; want exactly TestTagged", missing, err)
		}
		tagged := []Invariant{{ID: "CON-1", Title: "x", Proofs: []Proof{existsTagged(".", "TestTagged", "integration"), exists(".", "TestPlain")}}}
		if missing, _, err := missingProofs(dir, tagged); err != nil || len(missing) != 0 {
			t.Fatalf("tagged: missing = %v, %v; want none", missing, err)
		}
	})

	t.Run("every EXISTS proof in the real index is in the tree", func(t *testing.T) {
		missing, checked, err := missingProofs(root, Invariants())
		if err != nil {
			t.Fatal(err)
		}
		// Anchor: the index cites at least this many live proofs today. A verdict over an
		// index that cites nothing would pass vacuously.
		if checked < 10 {
			t.Fatalf("only %d EXISTS proofs checked; the index has lost its live proofs", checked)
		}
		for _, m := range missing {
			t.Error(m)
		}
	})
}

func TestEveryInvariantIdIsListedOnce(t *testing.T) {
	good := func() []Invariant {
		return []Invariant{
			{ID: "CON-1", Title: "a", Proofs: []Proof{exists("x", "TestA")}},
			{ID: "CON-2", Title: "b", Proofs: []Proof{scheduled("TestB", "lane", "9.9")}},
			{ID: "CR-1", Title: "c", Proofs: []Proof{existsFile("hack/x.sh", "x")}},
		}
	}
	want := map[string]int{"CON": 2, "CR": 1}
	if got := malformed(good(), want); len(got) != 0 {
		t.Fatalf("a well-formed table is reported: %v", got)
	}
	for _, c := range []struct {
		name  string
		plant func([]Invariant) []Invariant
		want  string
		also  []string // the further findings that plant necessarily causes
	}{
		{"duplicate id", func(ix []Invariant) []Invariant { ix[1].ID = "CON-1"; return ix }, "more than once", []string{"CON-2 is missing"}},
		{"gap in numbering", func(ix []Invariant) []Invariant { ix[1].ID = "CON-3"; return ix }, "CON-3 is beyond", []string{"CON-2 is missing"}},
		{"the last article dropped", func(ix []Invariant) []Invariant { return slices.Delete(ix, 1, 2) }, "CON-2 is missing", nil},
		{"a whole document dropped", func(ix []Invariant) []Invariant { return ix[:2] }, "CR-1 is missing", nil},
		{"bad id", func(ix []Invariant) []Invariant { ix[2].ID = "CR-01"; return ix }, "not a CON-n or CR-n", []string{"CR-1 is missing"}},
		{"no title", func(ix []Invariant) []Invariant { ix[0].Title = " "; return ix }, "no title", nil},
		{"no proof", func(ix []Invariant) []Invariant { ix[0].Proofs = nil; return ix }, "names no proof", nil},
		{"exists without a path", func(ix []Invariant) []Invariant { ix[0].Proofs[0].Path = ""; return ix }, "names no path", nil},
		{"scheduled without an owner", func(ix []Invariant) []Invariant { ix[1].Proofs[0].Owner = ""; return ix }, "names no owner", nil},
		{"scheduled without a target", func(ix []Invariant) []Invariant { ix[1].Proofs[0].Target = "v1.x"; return ix }, "bad target", nil},
		{"unknown status", func(ix []Invariant) []Invariant { ix[0].Proofs[0].Status = 0; return ix }, "unknown status", nil},
		{"unknown kind", func(ix []Invariant) []Invariant { ix[0].Proofs[0].Kind = 0; return ix }, "unknown proof kind", nil},
		{"unnamed proof", func(ix []Invariant) []Invariant { ix[0].Proofs[0].Name = ""; return ix }, "no name", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := malformed(c.plant(good()), want)
			wants := append([]string{c.want}, c.also...)
			if len(got) != len(wants) {
				t.Fatalf("malformed = %v; want exactly %d finding(s): %q", got, len(wants), wants)
			}
			for _, w := range wants {
				if !slices.ContainsFunc(got, func(g string) bool { return strings.Contains(g, w) }) {
					t.Fatalf("malformed = %v; want a finding containing %q", got, w)
				}
			}
		})
	}

	t.Run("the real index", func(t *testing.T) {
		for _, m := range malformed(Invariants(), map[string]int{"CON": ConstitutionArticles, "CR": CitizenRightsRedLines}) {
			t.Error(m)
		}
	})
}

func TestNoScheduledProofIsPastItsRelease(t *testing.T) {
	root := repoRoot(t)
	shipped, err := shippedRelease(root)
	if err != nil {
		t.Fatalf("cannot tell what has shipped: %v", err)
	}
	// Anchor: v1.1 shipped before this index existed, so a reading below it means the
	// source is wrong, not that nothing has shipped.
	if !(release{1, 1}).atOrBefore(shipped) {
		t.Fatalf("docs/versions.json says the newest shipped release is %s; expected at least 1.1", shipped)
	}

	t.Run("a target at or before the shipped release is reported", func(t *testing.T) {
		for _, target := range []string{"1.0", shipped.String()} {
			planted, ok := withProof(Invariants(), "CR-13", func(p Proof) bool { return p.Status == Scheduled },
				func(p Proof) Proof { p.Target = target; return p })
			if !ok {
				t.Fatal("no scheduled proof of CR-13 to plant on")
			}
			got := overdue(planted, shipped)
			if len(got) != 1 || !strings.Contains(got[0], "CR-13") {
				t.Fatalf("target %s: overdue = %v; want exactly CR-13", target, got)
			}
		}
	})
	t.Run("a target after the shipped release is not", func(t *testing.T) {
		next := release{shipped.major, shipped.minor + 1}.String()
		index := []Invariant{{ID: "CON-1", Title: "x", Proofs: []Proof{scheduled("T", "lane", next)}}}
		if got := overdue(index, shipped); len(got) != 0 {
			t.Fatalf("overdue = %v; want none for a target of %s", got, next)
		}
	})
	t.Run("an EXISTS proof is never overdue", func(t *testing.T) {
		index := []Invariant{{ID: "CON-1", Title: "x", Proofs: []Proof{{Kind: GoTest, Path: "x", Name: "T", Status: Exists, Target: "0.1"}}}}
		if got := overdue(index, shipped); len(got) != 0 {
			t.Fatalf("overdue = %v; want none", got)
		}
	})
	t.Run("the shipped release is the newest entry, not the first", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			body string
			want string
		}{
			{`["1.0","1.10","1.9"]`, "1.10"},
			{`["2.0","1.12"]`, "2.0"},
		} {
			if err := os.WriteFile(filepath.Join(dir, "docs", "versions.json"), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := shippedRelease(dir)
			if err != nil || got.String() != c.want {
				t.Fatalf("%s: shippedRelease = %v, %v; want %s", c.body, got, err, c.want)
			}
		}
		for _, bad := range []string{`[]`, `["1.x"]`, `{}`} {
			if err := os.WriteFile(filepath.Join(dir, "docs", "versions.json"), []byte(bad), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := shippedRelease(dir); err == nil {
				t.Fatalf("%s: shippedRelease accepted it; it must fail loudly", bad)
			}
		}
	})

	t.Run("the real index", func(t *testing.T) {
		for _, m := range overdue(Invariants(), shipped) {
			t.Error(m)
		}
	})
}
