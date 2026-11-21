// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package assets

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// requiredVersionRe matches a `required_version` setting inside a terraform block.
// Only a directory that declares one is a root: a module has no reason to.
var requiredVersionRe = regexp.MustCompile(`(?m)^\s*required_version\s*=`)

// nonRootSegments are the path segments under which a directory is never a root,
// however many .tf files it holds. This list is deliberately written a second time,
// in Go, rather than shared with hack/tofu-roots.sh: the two are an independent
// pair, so that a discovery that stops walking where the risk is surfaces here.
var nonRootSegments = map[string]bool{"modules": true, "tests": true, "fixtures": true, "premises": true, ".terraform": true}

// walkRoots collects every directory under dir that holds a .tf file declaring a
// terraform required_version and has no non-root path segment. It compares what
// the files SAY, not how many there are: a count would break the day a module
// declares required_version, and would count fixture .tf files.
func walkRoots(t *testing.T, dir string) []string {
	t.Helper()
	found := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && nonRootSegments[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".tf") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if requiredVersionRe.Match(b) {
			rel, err := filepath.Rel(dir, filepath.Dir(path))
			if err != nil {
				return err
			}
			found[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	out := make([]string, 0, len(found))
	for d := range found {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// setDiff returns what is in a and not in b, and what is in b and not in a.
func setDiff(a, b []string) (onlyA, onlyB []string) {
	in := func(s []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range s {
			m[x] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	for _, x := range a {
		if !mb[x] {
			onlyA = append(onlyA, x)
		}
	}
	for _, x := range b {
		if !ma[x] {
			onlyB = append(onlyB, x)
		}
	}
	return onlyA, onlyB
}

func findBash(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	for _, p := range []string{`C:\Program Files\Git\bin\bash.exe`, `C:\Program Files\Git\usr\bin\bash.exe`} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatal("bash is required to run hack/tofu-roots.sh and was not found: this check must not be skipped, " +
		"because a skipped discovery cross-check is a discovery nobody checked")
	return ""
}

// TestTofuRootsDiscoveryMatchesTheTree is the independent cross-check on
// hack/tofu-roots.sh --all: the set of directories that declare a required_version
// is compared with the set the script prints.
func TestTofuRootsDiscoveryMatchesTheTree(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	walked := walkRoots(t, filepath.Join(root, "deploy"))
	if len(walked) == 0 {
		t.Fatal("the independent walk found no root under deploy/: the cross-check would compare nothing")
	}
	for _, want := range []string{"gke", "playground", "opentofu/cluster", "opentofu/instance"} {
		found := false
		for _, w := range walked {
			if w == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the independent walk did not find the known root deploy/%s", want)
		}
	}

	cmd := exec.Command(findBash(t), "hack/tofu-roots.sh", "--all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hack/tofu-roots.sh --all: %v", err)
	}
	var discovered []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(strings.TrimRight(l, "\r"))
		if l == "" {
			continue
		}
		discovered = append(discovered, strings.TrimPrefix(l, "deploy/"))
	}
	sort.Strings(discovered)
	onlyWalk, onlyScript := setDiff(walked, discovered)
	if len(onlyWalk) > 0 || len(onlyScript) > 0 {
		t.Fatalf("hack/tofu-roots.sh --all disagrees with the tree.\n  declared a required_version but not discovered: %v\n  discovered but declares none: %v", onlyWalk, onlyScript)
	}
}

// TestSetDiffSeesADisagreement is the negative control: the comparison above has
// to be able to fail, or a green run says nothing.
func TestSetDiffSeesADisagreement(t *testing.T) {
	a, b := setDiff([]string{"gke", "fleet/gke-gcs"}, []string{"gke"})
	if len(a) != 1 || a[0] != "fleet/gke-gcs" || len(b) != 0 {
		t.Fatalf("a root missing from the script's output was not reported: %v %v", a, b)
	}
	a, b = setDiff([]string{"gke"}, []string{"gke", "stray"})
	if len(a) != 0 || len(b) != 1 {
		t.Fatalf("a stray discovered directory was not reported: %v %v", a, b)
	}
	if a, b = setDiff([]string{"x", "y"}, []string{"y", "x"}); len(a)+len(b) != 0 {
		t.Fatalf("equal sets reported as different: %v %v", a, b)
	}
}

// TestWalkRootsIgnoresNonRoots pins the walk's own exclusions against a planted tree.
func TestWalkRootsIgnoresNonRoots(t *testing.T) {
	d := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(d, filepath.FromSlash(rel), "main.tf")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("terraform {\n  required_version = \">= 1.10\"\n}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []string{"fleet/gke-gcs", "gke", "gke/tests", "gke/fixtures", "fleet/premises", "modules/m", "gke/.terraform/x"} {
		write(r)
	}
	got := walkRoots(t, d)
	if strings.Join(got, ",") != "fleet/gke-gcs,gke" {
		t.Fatalf("walk found %v", got)
	}
}
