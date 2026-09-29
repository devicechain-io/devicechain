// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kubeisolation

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ambientReaders are the packages that pick a cluster out of the process environment. A
// test binary that links any of them can reach whatever cluster the environment names.
//
// Known gap, stated so the exempt set is a claim and not a silence: rest.InClusterConfig
// also reads the environment (KUBERNETES_SERVICE_HOST), but k8s.io/client-go/rest is in
// nearly every client's closure, so it cannot mark a package. Nothing in backend/cli
// calls it today.
//
// KUBERNETES_MASTER is read by clientcmd itself (into clientcmd.ClusterDefaults, the
// cluster Helm's cli.New() falls back to), so it marks the same packages clientcmd does
// and needs no entry of its own.
var ambientReaders = []string{
	"k8s.io/client-go/tools/clientcmd",
	"helm.sh/helm/v3/pkg/cli",
	"github.com/hashicorp/terraform-exec/tfexec",
}

const modulePath = "github.com/devicechain-io/dcctl"

// TestEveryPackageThatCanReachAClusterIsIsolated derives, from the import graph, which of
// this module's test binaries link a reader of the ambient cluster configuration, and
// requires each of them to start from kubeisolation.Run in its TestMain. There is no list
// to keep in step: a new package is covered the moment it imports one of the readers.
//
// Go's test cache does not see the `go list` subprocess. The module's directories are
// walked below so that adding or removing a package changes this test's cache key, but an
// import added to an EXISTING file is not a directory change: a cached PASS can survive
// it under plain `go test`. CI and the repository's sweep pass -count=1.
func TestEveryPackageThatCanReachAClusterIsIsolated(t *testing.T) {
	root := moduleRoot(t)
	walkModule(t, root)

	cmd := exec.Command("go", "list", "-test", "-f", "{{.ImportPath}}\t{{.Dir}}\t{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	// An inherited GOFLAGS (-mod=vendor, -json, ...) would change what is resolved or how it
	// is printed; this reads the module as it is.
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Never a skip: a guard that skips when it cannot look is a silent one.
		t.Fatalf("go list over %s: %v\n%s", root, err, stderr.String())
	}

	demanded := map[string]string{} // import path -> dir
	var exempt []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), "\t", 3)
		// The "<pkg>.test" line is the test binary's main package: its Deps are everything
		// the binary links.
		if len(fields) != 3 || !strings.HasSuffix(fields[0], ".test") {
			continue
		}
		pkg := strings.TrimSuffix(fields[0], ".test")
		deps := strings.Fields(fields[2])
		if slices.ContainsFunc(ambientReaders, func(r string) bool { return slices.Contains(deps, r) }) {
			demanded[pkg] = fields[1]
		} else {
			exempt = append(exempt, pkg)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	demandedNames := make([]string, 0, len(demanded))
	for p := range demanded {
		demandedNames = append(demandedNames, p)
	}
	slices.Sort(demandedNames)
	slices.Sort(exempt)
	t.Logf("test binaries that can reach a cluster (must be isolated): %v", demandedNames)
	t.Logf("test binaries that link no ambient cluster reader (exempt): %v", exempt)

	// A known positive, so the classifier cannot go quiet: bootstrap builds every cluster
	// client dcctl has.
	if _, ok := demanded[modulePath+"/bootstrap"]; !ok {
		t.Fatalf("the classifier is blind: %s/bootstrap links client-go's kubeconfig loader but was not "+
			"found to reach a cluster (demanded: %v)", modulePath, demandedNames)
	}

	if missing := notIsolated(t, demanded); len(missing) > 0 {
		t.Fatalf("these packages' tests can reach a real cluster and do not start from kubeisolation.Run:\n  %s\n"+
			"add to each a main_test.go with\n\tfunc TestMain(m *testing.M) { os.Exit(kubeisolation.Run(m)) }",
			strings.Join(missing, "\n  "))
	}
}

// notIsolated returns, sorted, the packages in demanded (import path -> dir) whose tests
// do not start from kubeisolation.Run.
func notIsolated(t *testing.T, demanded map[string]string) []string {
	t.Helper()
	var missing []string
	for p, dir := range demanded {
		if !isolatesInTestMain(t, dir, p == modulePath+"/internal/kubeisolation") {
			missing = append(missing, p)
		}
	}
	slices.Sort(missing)
	return missing
}

// isolatesInTestMain reports whether a _test.go file in dir declares TestMain(*testing.M)
// with a call to kubeisolation.Run in its body. self is true for this package, where the
// call is the unqualified Run.
func isolatesInTestMain(t *testing.T, dir string, self bool) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	const importPath = modulePath + "/internal/kubeisolation"
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		local := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
				local = "kubeisolation"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" && !self {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := f.X.(*ast.Ident); ok && local != "" && x.Name == local && f.Sel.Name == "Run" {
						found = true
					}
				case *ast.Ident:
					if self && f.Name == "Run" {
						found = true
					}
				}
				return !found
			})
			if found {
				return true
			}
		}
	}
	return false
}

// moduleRoot walks up from the working directory to dcctl's go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for line := range strings.Lines(string(data)) {
				if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
					if f[1] != modulePath {
						t.Fatalf("the nearest go.mod (%s) is module %s, want %s", dir, f[1], modulePath)
					}
					return dir
				}
			}
			t.Fatalf("%s/go.mod declares no module", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// walkModule lists every directory of the module, which is what puts the set of packages
// into this test's cache key.
func walkModule(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The guard's own verdict, shown on fixtures: a TestMain that CALLS kubeisolation.Run
// isolates (under any import name); one that does not, or merely names Run without
// calling it, or calls it from a function that is not TestMain, does not; and exactly
// the packages that do not are reported.
func TestTheGuardReportsAPackageWithoutTheTestMain(t *testing.T) {
	const imp = `"` + modulePath + `/internal/kubeisolation"`
	fixtures := map[string]string{
		"calls": `package p
import ("os"; "testing"; ` + imp + `)
func TestMain(m *testing.M) { os.Exit(kubeisolation.Run(m)) }`,
		"aliased": `package p
import ("os"; "testing"; ki ` + imp + `)
func TestMain(m *testing.M) { os.Exit(ki.Run(m)) }`,
		"plain": `package p
import ("os"; "testing")
func TestMain(m *testing.M) { os.Exit(m.Run()) }`,
		"reference": `package p
import ("os"; "testing"; ` + imp + `)
func TestMain(m *testing.M) { run := kubeisolation.Run; os.Exit(run(m)) }`,
		"elsewhere": `package p
import ("testing"; ` + imp + `)
func setup(m *testing.M) int { return kubeisolation.Run(m) }
func TestMain(m *testing.M) {}`,
		"none": `package p
import "testing"
func TestX(t *testing.T) {}`,
	}
	demanded := map[string]string{}
	for name, src := range fixtures {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		demanded[name] = dir
	}
	got := notIsolated(t, demanded)
	want := []string{"elsewhere", "none", "plain", "reference"}
	if !slices.Equal(got, want) {
		t.Fatalf("the guard reported %v, want %v", got, want)
	}
}
