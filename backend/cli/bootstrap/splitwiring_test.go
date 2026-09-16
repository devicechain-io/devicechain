// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	assets "github.com/devicechain-io/dc-deploy"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// Four defects a review of the split found that neither the tests nor two green live
// round-trips could see. Each test below is written to fail on the code as it was.

// ---------------------------------------------------------------------------
// 1. Outputs read from a root that does not declare them.
// ---------------------------------------------------------------------------

var outputDeclRe = regexp.MustCompile(`(?m)^output\s+"([^"]+)"\s*\{`)

// rootDeclaredOutputs parses a root's outputs.tf, with the same floor as
// rootDeclaredVars: a parser that finds nothing makes every assertion below vacuous.
func rootDeclaredOutputs(t *testing.T, root fs.FS, name string) map[string]bool {
	t.Helper()
	b, err := fs.ReadFile(root, "outputs.tf")
	if err != nil {
		t.Fatalf("reading the %s root's outputs.tf: %v", name, err)
	}
	out := map[string]bool{}
	for _, m := range outputDeclRe.FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatalf("the %s root's outputs.tf parsed to zero outputs; the parser is broken, "+
			"and every check below would pass against nothing", name)
	}
	return out
}

// outputKeysReadIn returns every string literal used as an index into a variable named
// `outputs` inside the named function.
func outputKeysReadIn(t *testing.T, file, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatalf("%s no longer declares %s", file, fn)
	}
	var keys []string
	ast.Inspect(decl, func(n ast.Node) bool {
		ix, ok := n.(*ast.IndexExpr)
		if !ok {
			return true
		}
		if id, ok := ix.X.(*ast.Ident); !ok || id.Name != "outputs" {
			return true
		}
		if lit, ok := ix.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			k, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k)
		}
		return true
	})
	sort.Strings(keys)
	return keys
}

// 🔴🔴 THE CLASS, NOT THE FOUR INSTANCES. Every output read is `if meta, ok :=
// outputs[...]; ok`, so reading a key the root does not export is a silent no-op
// forever. The split moved cnpg_namespace, grafana_service, grafana_namespace and
// database_backup_survives_cluster_loss to the cluster root and left their reads on
// the instance root's outputs — and every fresh install lost the CNPG operator's
// PodMonitor and control-plane alerts, with nothing failing.
//
// So this holds each decoder against the outputs.tf of the root it reads, and it
// fails naming the key and the root. It does not care which root a value "belongs"
// in; it cares that the read can ever succeed.
func TestEveryOutputDcctlReadsIsDeclaredByTheRootItIsReadFrom(t *testing.T) {
	for _, tc := range []struct {
		file, fn, rootName string
		root               fs.FS
		floor              int
	}{
		{"tofu.go", "applyInstanceInfra", "instance", assets.OpenTofuInstance(), 5},
		{"clusterprereqs.go", "recordClusterOutputs", "cluster", assets.OpenTofuCluster(), 4},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			declared := rootDeclaredOutputs(t, tc.root, tc.rootName)
			keys := outputKeysReadIn(t, tc.file, tc.fn)
			// The AST walk's own floor: a refactor that renamed the map would make
			// this find nothing and pass.
			if len(keys) < tc.floor {
				t.Fatalf("found only %d output reads in %s (%v), want at least %d; the walk "+
					"no longer sees them", len(keys), tc.fn, keys, tc.floor)
			}
			for _, k := range keys {
				if !declared[k] {
					t.Errorf("%s reads output %q, which the %s root does not declare — the "+
						"read can never succeed, and it fails silently", tc.fn, k, tc.rootName)
				}
			}
		})
	}
}

// ...and the archive decoder, whose keys are a table rather than index expressions:
// driven with exactly the outputs the cluster root declares, it must decode.
func TestTheArchiveDecoderReadsOnlyWhatTheClusterRootDeclares(t *testing.T) {
	declared := rootDeclaredOutputs(t, assets.OpenTofuCluster(), "cluster")
	outputs := map[string]tfexec.OutputMeta{}
	for k := range declared {
		outputs[k] = tfexec.OutputMeta{Value: []byte(`""`)}
	}
	if _, err := archiveFromOutputs(outputs); err != nil {
		t.Errorf("the archive decoder needs an output the cluster root does not declare: %v", err)
	}
}

// The behaviour the four reads exist for, end to end through the decoder.
func TestTheClusterRootsMonitoringAndReportOutputsReachTheState(t *testing.T) {
	st := &State{Values: map[string]string{cnpgNamespaceKey: "stale"}}
	recordClusterOutputs(st, map[string]tfexec.OutputMeta{
		"cnpg_namespace":                        {Value: []byte(`"cnpg-system"`)},
		"grafana_service":                       {Value: []byte(`"kube-prometheus-stack-grafana"`)},
		"grafana_namespace":                     {Value: []byte(`"monitoring"`)},
		"database_backup_survives_cluster_loss": {Value: []byte(`true`)},
	})
	for key, want := range map[string]string{
		cnpgNamespaceKey:         "cnpg-system",
		"grafanaService":         "kube-prometheus-stack-grafana",
		"grafanaNamespace":       "monitoring",
		databaseBackupOffsiteKey: "true",
	} {
		if got := st.Values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// The clearing half: a cluster with no operator reports null, and a value an
	// earlier apply wrote must not survive it.
	st = &State{Values: map[string]string{cnpgNamespaceKey: "cnpg-system"}}
	recordClusterOutputs(st, map[string]tfexec.OutputMeta{"cnpg_namespace": {Value: []byte(`null`)}})
	if got := st.Values[cnpgNamespaceKey]; got != "" {
		t.Errorf("a null cnpg_namespace left %q behind; the PodMonitor would select an "+
			"operator that is not there", got)
	}
}

// ---------------------------------------------------------------------------
// 2. The CNPG admission retry.
// ---------------------------------------------------------------------------

type scriptedApplier struct {
	results []error
	calls   int
}

func (s *scriptedApplier) Apply(context.Context, ...tfexec.ApplyOption) error {
	i := s.calls
	s.calls++
	if i < len(s.results) {
		return s.results[i]
	}
	return nil
}

func TestTheAdmissionRaceIsRetriedOnceAfterTheWebhookAnswers(t *testing.T) {
	webhookDown := errors.New(realConnectionRefused)

	t.Run("race, then success", func(t *testing.T) {
		a := &scriptedApplier{results: []error{webhookDown, nil}}
		waited := 0
		err := applyWithCNPGAdmissionRetry(context.Background(), a, nil, "tofu apply",
			func(context.Context) error { waited++; return nil })
		if err != nil {
			t.Fatalf("a race the probe then cleared was reported as a failure: %v", err)
		}
		if a.calls != 2 || waited != 1 {
			t.Errorf("applied %d times and waited %d times, want 2 and 1", a.calls, waited)
		}
	})

	t.Run("any other failure is returned untouched", func(t *testing.T) {
		a := &scriptedApplier{results: []error{errors.New("Error: cannot re-use a name that is still in use")}}
		waited := 0
		err := applyWithCNPGAdmissionRetry(context.Background(), a, nil, "tofu apply",
			func(context.Context) error { waited++; return nil })
		if err == nil || a.calls != 1 || waited != 0 {
			t.Errorf("a non-webhook failure was retried or swallowed: err=%v calls=%d waited=%d",
				err, a.calls, waited)
		}
	})

	t.Run("a second race is not retried again", func(t *testing.T) {
		a := &scriptedApplier{results: []error{webhookDown, webhookDown, nil}}
		err := applyWithCNPGAdmissionRetry(context.Background(), a, nil, "tofu apply",
			func(context.Context) error { return nil })
		if err == nil || a.calls != 2 {
			t.Errorf("err=%v after %d applies; the retry is once, not a loop", err, a.calls)
		}
	})

	t.Run("a webhook that never recovers stops the run", func(t *testing.T) {
		a := &scriptedApplier{results: []error{webhookDown}}
		err := applyWithCNPGAdmissionRetry(context.Background(), a, nil, "tofu apply",
			func(context.Context) error { return errors.New("timed out") })
		if err == nil || a.calls != 1 {
			t.Errorf("err=%v after %d applies; a failed wait must not re-apply", err, a.calls)
		}
	})
}

// 🔴 AND IT WRAPS THE ROOT THE RACE ACTUALLY LIVES IN. The cluster root installs the
// operator and creates the shared relational store in one graph; the retry sat on the
// instance root only, under a comment claiming the cluster root could not race. Both
// applies reach a real tofu binary, so this reads the source: neither may call Apply
// directly.
func TestEveryApplyThatCanCreateADatabaseClusterRetriesTheAdmissionRace(t *testing.T) {
	for _, tc := range []struct{ file, fn string }{
		{"clusterprereqs.go", "applyClusterPrereqs"},
		{"tofu.go", "applyInstanceInfra"},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			var decl *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == tc.fn {
					decl = fd
				}
			}
			if decl == nil {
				t.Fatalf("%s no longer declares %s", tc.file, tc.fn)
			}
			retries, direct := false, false
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "applyWithCNPGAdmissionRetry" {
						retries = true
					}
				case *ast.SelectorExpr:
					if fun.Sel.Name == "Apply" {
						direct = true
					}
				}
				return true
			})
			if !retries {
				t.Errorf("%s does not apply through applyWithCNPGAdmissionRetry; a cold "+
					"bootstrap fails on the admission race one run in two", tc.fn)
			}
			if direct {
				t.Errorf("%s calls Apply directly, bypassing the admission retry", tc.fn)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. One root per state directory.
// ---------------------------------------------------------------------------

// 🔴 A root's configuration with no state beside it plans to CREATE everything it
// declares against the live cluster. Extracting the whole tree into each state
// directory put exactly that next to every real root.
func TestAStateDirectoryHoldsOnlyTheRootAppliedFromIt(t *testing.T) {
	for _, tc := range []struct{ root, other string }{
		{assets.ClusterRootDir, assets.InstanceRootDir},
		{assets.InstanceRootDir, assets.ClusterRootDir},
	} {
		t.Run(tc.root, func(t *testing.T) {
			dir := t.TempDir()
			if err := extractRoot(assets.OpenTofu(), tc.root, dir); err != nil {
				t.Fatalf("extractRoot: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.root, "main.tf")); err != nil {
				t.Errorf("the %s root itself was not extracted: %v", tc.root, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "modules")); err != nil {
				t.Errorf("the shared modules were not extracted, so ../modules/<x> resolves "+
					"to nothing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.other)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the %s root was extracted into the %s root's state directory (%v); "+
					"a plan there creates everything it declares", tc.other, tc.root, err)
			}
			// Nothing else at the top level either — a future third root must not ride
			// along by default.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			want := []string{"modules", tc.root}
			sort.Strings(want)
			if strings.Join(names, ",") != strings.Join(want, ",") {
				t.Errorf("extracted top level = %v, want exactly %v", names, want)
			}
		})
	}
}

func TestAMissingRootIsAnErrorNotAnEmptyDirectory(t *testing.T) {
	if err := extractRoot(assets.OpenTofu(), "no-such-root", t.TempDir()); err == nil {
		t.Error("extracting a root the tree does not contain succeeded; init would fail " +
			"later on providers instead of on the missing root")
	}
}

// 🔴 CONSTRUCTED CORRECTLY, CONNECTED TO NOTHING. The decoder test above calls
// recordClusterOutputs directly, so it passes just as well if the apply never does —
// and the apply needs a tofu binary, so no test can run it. Read the source.
func TestTheClusterApplyRecordsItsOutputs(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "clusterprereqs.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "applyClusterPrereqs" {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recordClusterOutputs" {
					found = true
				}
			}
			return true
		})
	}
	if !found {
		t.Error("applyClusterPrereqs never calls recordClusterOutputs; the CNPG operator's " +
			"PodMonitor and control-plane alerts would stop rendering on every install")
	}
}

// ...and each state directory is actually filled through extractRoot, naming its OWN
// root. The test above proves extractRoot is right; both call sites need a tofu binary
// and a cluster, and a mutation round showed either reverting to the whole-tree
// extractFS survived every other test.
func TestEachApplyExtractsOnlyItsOwnRoot(t *testing.T) {
	for _, tc := range []struct{ file, fn, root string }{
		{"tofu.go", "openInstanceRoot", "InstanceRootDir"},
		{"clusterprereqs.go", "applyClusterPrereqs", "ClusterRootDir"},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, tc.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			var decl *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == tc.fn {
					decl = fd
				}
			}
			if decl == nil {
				t.Fatalf("%s no longer declares %s", tc.file, tc.fn)
			}
			ownRoot, wholeTree := false, false
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				switch id.Name {
				case "extractFS":
					wholeTree = true
				case "extractRoot":
					if len(call.Args) >= 2 {
						if sel, ok := call.Args[1].(*ast.SelectorExpr); ok && sel.Sel.Name == tc.root {
							ownRoot = true
						}
					}
				}
				return true
			})
			if wholeTree {
				t.Errorf("%s extracts the whole tree; the other root lands beside its state with "+
					"none of its own, and a plan there creates everything it declares", tc.fn)
			}
			if !ownRoot {
				t.Errorf("%s does not extract its own root (%s) through extractRoot", tc.fn, tc.root)
			}
		})
	}
}

// The relational store's contract decodes from exactly what the cluster root declares —
// and a missing or empty value is an error, never a store at an empty address.
func TestTheRelationalStoreContractReadsOnlyWhatTheClusterRootDeclares(t *testing.T) {
	declared := rootDeclaredOutputs(t, assets.OpenTofuCluster(), "cluster")
	full := map[string]tfexec.OutputMeta{}
	for k := range declared {
		full[k] = tfexec.OutputMeta{Value: []byte(`"x"`)}
	}
	got, err := rdbFromOutputs(full)
	if err != nil {
		t.Fatalf("the relational store decoder needs an output the cluster root does not declare: %v", err)
	}
	if got != (ClusterRdb{Namespace: "x", ClusterName: "x", ProvisionerSecret: rdbProvisionerSecretName}) {
		t.Errorf("decoded %+v", got)
	}
	for _, k := range []string{"namespace", "postgres_cluster_name"} {
		missing := map[string]tfexec.OutputMeta{}
		empty := map[string]tfexec.OutputMeta{}
		for kk, v := range full {
			empty[kk] = v
			if kk != k {
				missing[kk] = v
			}
		}
		empty[k] = tfexec.OutputMeta{Value: []byte(`""`)}
		if _, err := rdbFromOutputs(missing); err == nil {
			t.Errorf("a cluster root that stopped exporting %q decoded as a store", k)
		}
		if _, err := rdbFromOutputs(empty); err == nil {
			t.Errorf("an empty %q decoded as a store", k)
		}
	}
}
