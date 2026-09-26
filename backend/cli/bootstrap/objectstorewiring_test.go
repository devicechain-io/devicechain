// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	assets "github.com/devicechain-io/dc-deploy"
)

// 🔴 TESTING THE CHECK IS NOT TESTING THAT THE INSTALL RUNS IT. The tests in
// objectstorerollout_test.go drive confirmObjectStoreRolledOut directly, so they pass
// just as well if applyClusterPrereqs never calls it, drops its error, or points it at
// another cluster — and the apply needs a tofu binary and a cluster, so no test can run
// it. A mutation round showed each of those survived every test. Read the source.
func TestTheClusterApplyConfirmsTheObjectStoreRolledOut(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "clusterprereqs.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var decl *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "applyClusterPrereqs" {
			decl = fd
		}
	}
	if decl == nil {
		t.Fatal("clusterprereqs.go no longer declares applyClusterPrereqs")
	}

	var outputAt, outputsDecodedAt token.Pos
	var check *ast.CallExpr
	var guard *ast.IfStmt
	ast.Inspect(decl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			switch fn := n.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "tf" && fn.Sel.Name == "Output" {
					outputAt = n.Pos()
				}
			case *ast.Ident:
				switch fn.Name {
				case "confirmObjectStoreRolledOut":
					check = n
				case "clusterOutputs":
					outputsDecodedAt = n.Pos()
				}
			}
		case *ast.IfStmt:
			if as, ok := n.Init.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "confirmObjectStoreRolledOut" {
						guard = n
					}
				}
			}
		}
		return true
	})

	if check == nil {
		t.Fatal("applyClusterPrereqs never calls confirmObjectStoreRolledOut; an install whose " +
			"object store update never rolled out would be recorded as finished")
	}
	if !outputAt.IsValid() || check.Pos() < outputAt {
		t.Error("confirmObjectStoreRolledOut is not called after tf.Output; it cannot be reading " +
			"the outputs of the apply that just ran")
	}
	if !outputsDecodedAt.IsValid() || outputsDecodedAt < check.Pos() {
		t.Error("the install outputs are decoded before the object store is confirmed; the check " +
			"must stand between the apply and the result it reports")
	}

	// Its error decides the result: `if err := confirm...(...); err != nil { return ..., err }`.
	if guard == nil {
		t.Fatal("confirmObjectStoreRolledOut's result is not tested by an if statement; its " +
			"error is being dropped")
	}
	errName := ""
	if as := guard.Init.(*ast.AssignStmt); len(as.Lhs) == 1 {
		if id, ok := as.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
			errName = id.Name
		}
	}
	if errName == "" {
		t.Fatal("confirmObjectStoreRolledOut's error is assigned to nothing")
	}
	if cond, ok := guard.Cond.(*ast.BinaryExpr); !ok || cond.Op != token.NEQ ||
		!isIdent(cond.X, errName) || !isIdent(cond.Y, "nil") {
		t.Errorf("confirmObjectStoreRolledOut's error is tested by something other than %s != nil", errName)
	}
	returned := false
	for _, st := range guard.Body.List {
		if ret, ok := st.(*ast.ReturnStmt); ok && len(ret.Results) > 0 && mentions(ret.Results[len(ret.Results)-1], errName) {
			returned = true
		}
	}
	if !returned {
		t.Error("applyClusterPrereqs does not return confirmObjectStoreRolledOut's error; a store " +
			"that never rolled out would be reported as installed")
	}

	// Its arguments: the outputs just read, clients for the install's OWN cluster, and
	// the real bound.
	if len(check.Args) != 4 {
		t.Fatalf("confirmObjectStoreRolledOut takes %d arguments here, want 4", len(check.Args))
	}
	if !isIdent(check.Args[1], "outputs") {
		t.Error("confirmObjectStoreRolledOut is not given the outputs tf.Output returned")
	}
	if !isIdent(check.Args[3], "objectStoreRolloutTimeout") {
		t.Error("confirmObjectStoreRolledOut is not bounded by objectStoreRolloutTimeout")
	}
	ownCluster := false
	ast.Inspect(check.Args[2], func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "kubeClients" && len(call.Args) == 1 {
			if sel, ok := call.Args[0].(*ast.SelectorExpr); ok && isIdent(sel.X, "st") && sel.Sel.Name == "KubeContext" {
				ownCluster = true
			}
		}
		return true
	})
	if !ownCluster {
		t.Error("the object store check does not build its clients with kubeClients(st.KubeContext); " +
			"with an explicit --context it would read a different cluster than the one installed into")
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// mentions reports whether name appears anywhere in e, so a wrapped error
// (fmt.Errorf("...: %w", err)) counts as returned as well as a bare one.
func mentions(e ast.Expr, name string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// The bound is not a fail-open in either direction — a smaller one only refuses more
// — but a store legitimately mid-rollout (a rescheduled pod) must get time to finish
// rather than fail the install on the first read.
func TestTheObjectStoreRolloutBoundLeavesTimeToRollOut(t *testing.T) {
	if objectStoreRolloutTimeout < time.Minute {
		t.Errorf("objectStoreRolloutTimeout = %v; a store that is mid-rollout would fail the install "+
			"before a replacement pod could start", objectStoreRolloutTimeout)
	}
}

// 🔴 THE VALUE, NOT JUST THE KEY. splitwiring holds that the cluster root DECLARES
// backup_object_store_deployment; a declaration whose value can be null satisfies
// that and fails every install without an in-cluster store, because a null root
// output is not stored in state and reaches dcctl as a missing one. Hold the whole
// chain from the Deployment to the output, including the explicit "no store" arm.
func TestTheObjectStoreOutputIsTheStoresDeployment(t *testing.T) {
	cluster := assets.OpenTofuCluster()

	body := squash(tfBlockBody(t, readTF(t, cluster, "outputs.tf"), `output "backup_object_store_deployment"`))
	want := `value = length(module.object_store) == 0 ? { in_cluster = false namespace = "" name = "" } : { ` +
		`in_cluster = true namespace = module.object_store[0].deployment.namespace ` +
		`name = module.object_store[0].deployment.name }`
	if !strings.HasSuffix(body, want) {
		t.Errorf("the cluster root's backup_object_store_deployment is\n  %s\nwant its value to be\n  %s\n"+
			"a null value is dropped from state and fails every install without a store; any other "+
			"value can read as \"no in-cluster store\" and skip the rollout check", body, want)
	}

	mod := tfBlockBody(t, readTF(t, cluster, "main.tf"), `module "object_store"`)
	if got := attrValue(mod, "source"); got != `"../modules/object-store"` {
		t.Errorf("module.object_store's source is %s, want \"../modules/object-store\"", got)
	}

	main := readTF(t, assets.OpenTofu(), "modules/object-store/main.tf")
	if n := len(regexp.MustCompile(`(?m)^resource\s+"kubernetes_deployment_v1"`).FindAllString(main, -1)); n != 1 {
		t.Fatalf("the object-store module declares %d Deployments, want exactly 1 (the store)", n)
	}
	out := tfBlockBody(t, main, `output "deployment"`)
	for _, want := range []string{
		"namespace = kubernetes_deployment_v1.this.metadata[0].namespace",
		"name = kubernetes_deployment_v1.this.metadata[0].name",
	} {
		if !strings.Contains(squash(out), want) {
			t.Errorf("the object-store module's deployment output does not carry %q:\n%s", want, out)
		}
	}
}

func readTF(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// tfBlockBody returns the text between the braces of the top-level block that opens
// with header, matching braces so nested blocks stay inside it.
func tfBlockBody(t *testing.T, src, header string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(header) + `\s*\{`)
	loc := re.FindAllStringIndex(src, -1)
	if len(loc) != 1 {
		t.Fatalf("found %d blocks opening %s, want exactly 1", len(loc), header)
	}
	depth := 0
	for i := loc[0][1] - 1; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[0][1]:i]
			}
		}
	}
	t.Fatalf("the block opening %s is never closed", header)
	return ""
}

// attrValue returns a top-level attribute's expression with whitespace squashed, or
// "" when the block does not set it.
func attrValue(body, name string) string {
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=\s*(.+)$`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return squash(m[1])
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
