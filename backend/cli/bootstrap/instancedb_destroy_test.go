// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func stubRemoveInstanceDatabase(t *testing.T, fn func(ctx context.Context, kubeContext, instance string, rdb ClusterRdb) error) {
	t.Helper()
	orig := removeInstanceDatabase
	removeInstanceDatabase = fn
	t.Cleanup(func() { removeInstanceDatabase = orig })
}

func anInstanceLoginSecret() *corev1.Secret {
	return mintedSecret("acme", "dci-acme-rdb-credentials", testUID,
		map[string]string{"username": "acme", "password": "pw"})
}

// An instance destroy drops its database and login through the store the install
// recorded, and removes the login's Secret only once the drop succeeded.
func TestAnInstanceDestroyDropsItsLoginThroughTheRecordedStore(t *testing.T) {
	c := fake.NewSimpleClientset(kubeSystem(testClusterUID), anInstanceLoginSecret())
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	var got ClusterRdb
	var gotInstance string
	stubRemoveInstanceDatabase(t, func(_ context.Context, _, instance string, rdb ClusterRdb) error {
		got, gotInstance = rdb, instance
		return nil
	})
	if left, err := removeInstanceRelationalLogin(context.Background(), c, "kind-x", "acme"); err != nil || left != "" {
		t.Fatalf("removing the instance's login: left=%q err=%v", left, err)
	}
	if gotInstance != "acme" || got != aRelationalStore() {
		t.Errorf("dropped %q through %+v, want acme through the recorded store", gotInstance, got)
	}
	if _, err := c.CoreV1().Secrets("acme").Get(context.Background(), "dci-acme-rdb-credentials",
		metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the login's Secret survived a successful drop: %v", err)
	}
}

// 🔴 A FAILED DROP KEEPS THE SECRET: it is what lets a re-run find the login it is removing.
func TestAFailedDropKeepsTheLoginsSecret(t *testing.T) {
	c := fake.NewSimpleClientset(kubeSystem(testClusterUID), anInstanceLoginSecret())
	if err := writeInstalled(context.Background(), c, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	stubRemoveInstanceDatabase(t, func(context.Context, string, string, ClusterRdb) error {
		return errors.New("store unreachable")
	})
	if _, err := removeInstanceRelationalLogin(context.Background(), c, "kind-x", "acme"); err == nil {
		t.Fatal("a failed drop was reported as success")
	}
	if _, err := c.CoreV1().Secrets("acme").Get(context.Background(), "dci-acme-rdb-credentials",
		metav1.GetOptions{}); err != nil {
		t.Errorf("the login's Secret was removed although its login was not: %v", err)
	}
}

// 🔴 THE STORE IS ASKED WHETHER THERE IS ANYTHING TO DROP, NOT THE SECRET. The Secret is
// in the instance's namespace, which the uninstall before this deletes — so a destroy
// re-run after a failed drop finds no Secret, and must still drop. And a cluster whose
// install record this dcctl cannot read is said and skipped, not guessed at.
func TestTheDropAsksTheStoreAndNeedsARecord(t *testing.T) {
	called := false
	stubRemoveInstanceDatabase(t, func(context.Context, string, string, ClusterRdb) error {
		called = true
		return nil
	})
	withRecord := fake.NewSimpleClientset(kubeSystem(testClusterUID)) // no login Secret
	if err := writeInstalled(context.Background(), withRecord, aCompleteInstall(), installClock); err != nil {
		t.Fatal(err)
	}
	if left, err := removeInstanceRelationalLogin(context.Background(), withRecord, "kind-x", "acme"); err != nil || !called || left != "" {
		t.Errorf("an instance whose login Secret is already gone: left=%q err=%v, store asked=%t", left, err, called)
	}

	called = false
	left, err := removeInstanceRelationalLogin(context.Background(),
		fake.NewSimpleClientset(kubeSystem(testClusterUID), anInstanceLoginSecret()), "kind-x", "acme")
	if err != nil || called || left == "" {
		t.Errorf("a cluster with no install record: left=%q err=%v, store touched=%t — the skip must be reported, "+
			"so the destroy does not close by saying the database is gone", left, err, called)
	}
}

// An upgrade of an instance built before per-instance logins is told to rebuild, not to
// restore a Secret that never existed.
func TestAnUpgradeOfAnInstanceWithNoLoginSaysRebuildNotRestore(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	writeInstallThenBootstrapSecrets(t, c, st)
	if err := c.CoreV1().Secrets(instanceNamespace(st.Instance)).Delete(context.Background(),
		instanceRdbSecretName(st.Instance), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	_, err := readInstanceCredentials(context.Background(), c, st)
	if err == nil || !strings.Contains(err.Error(), "dcctl destroy") || strings.Contains(err.Error(), "backup") {
		t.Errorf("want a rebuild instruction and no restore advice; got %v", err)
	}
}

// 🔴 A DESTROY THAT STOPPED CALLING THE DROP WOULD LEAVE EVERY INSTANCE'S DATABASE ON THE
// SHARED STORE, and nothing a unit test drives reaches uninstallInstance — it needs a
// cluster and a Helm release. So the call, and its place after the uninstall that ends
// the services' sessions, is held by the source.
func TestAnInstanceDestroyDropsItsDatabaseAfterUninstalling(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "destroy.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "uninstallInstance" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("destroy.go no longer declares uninstallInstance")
	}
	pos := map[string]token.Pos{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				if _, seen := pos[id.Name]; !seen {
					pos[id.Name] = call.Pos()
				}
			}
		}
		return true
	})
	for _, name := range []string{"helmUninstall", "removeInstanceRelationalLogin", "removeInstanceNamespace"} {
		if _, ok := pos[name]; !ok {
			t.Fatalf("uninstallInstance no longer calls %s; an instance destroy would leave its "+
				"database and login on the shared store", name)
		}
	}
	if pos["removeInstanceRelationalLogin"] < pos["helmUninstall"] {
		t.Error("the database is dropped before the services that hold sessions on it are uninstalled")
	}
	if pos["removeInstanceNamespace"] < pos["removeInstanceRelationalLogin"] {
		t.Error("the namespace is deleted before the drop, taking the login's Secret with it before the " +
			"drop can report whether it succeeded")
	}
}

// 🔴 AND THE LOCAL STATE GOES ONLY AFTER THE UNINSTALL, for the same reason: nothing a unit
// test drives gets an uninstall to succeed. Removing the state first would throw away the
// tfstate of an instance still deployed whenever the uninstall then failed; not removing
// it at all is how orphaned instance directories accumulate.
func TestDestroyClearsLocalStateOnlyAfterUninstalling(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "destroy.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Destroy" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("destroy.go no longer declares Destroy")
	}
	var uninstall token.Pos
	var removals []token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "uninstallInstance":
					uninstall = call.Pos()
				case "removeInstanceState":
					removals = append(removals, call.Pos())
				}
			}
		}
		return true
	})
	if uninstall == token.NoPos {
		t.Fatal("Destroy no longer calls uninstallInstance")
	}
	after := false
	for _, p := range removals {
		after = after || p > uninstall
	}
	if !after {
		t.Error("Destroy never removes the instance's local state after uninstalling it")
	}
}

// The event store's database is named after the instance, and it reaches the root that
// builds the event store — nothing else creates it.
func TestTheEventStoreDatabaseIsNamedAfterTheInstance(t *testing.T) {
	st := &State{Instance: "acme", KubeContext: "kind-acme", Values: map[string]string{}}
	_, instanceVars, err := splitVars(infraVars(st))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(instanceVars, "timescale_database=acme") {
		t.Errorf("the instance root is not told to create database %q: %v", "acme", instanceVars)
	}
}

// 🔴 psql ECHOES THE FAILING STATEMENT, and the provisioner's statement carries its
// password verifier. Only the verdict lines may reach an error message.
func TestAProvisionerFailureDoesNotEchoTheStatement(t *testing.T) {
	sql, err := provisionerSQL("pw-for-the-test")
	if err != nil {
		t.Fatal(err)
	}
	stderr := "psql:<stdin>:9: ERROR:  role \"x\" is reserved\nLINE 4:     CREATE ROLE \"dc_provisioner\" " + sql + "\n" +
		"CONTEXT:  PL/pgSQL function inline_code_block\nFATAL:  terminating connection\n"
	got := psqlVerdict(stderr)
	if strings.Contains(got, "SCRAM-SHA-256") || strings.Contains(got, "CREATE ROLE") {
		t.Errorf("the statement reached the error message: %q", got)
	}
	if !strings.Contains(got, `ERROR:  role "x" is reserved`) || !strings.Contains(got, "FATAL:  terminating connection") {
		t.Errorf("the verdict lines were lost: %q", got)
	}
}

// An instance whose login Secret is in the SHARED namespace was built before namespaces,
// not before logins — and is told so, not that its data belongs to the shared owner.
func TestAnUpgradeOfAnInstanceStillInTheSharedNamespaceSaysSo(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	writeInstallThenBootstrapSecrets(t, c, st)
	if err := c.CoreV1().Secrets(instanceNamespace(st.Instance)).Delete(context.Background(),
		instanceRdbSecretName(st.Instance), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CoreV1().Secrets(infraNamespace).Create(context.Background(),
		anInstanceLoginSecretIn(infraNamespace), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	_, err := readInstanceCredentials(context.Background(), c, st)
	if err == nil || !strings.Contains(err.Error(), "namespace of its own") || strings.Contains(err.Error(), "shared owner") {
		t.Errorf("want the pre-namespace diagnosis, not the pre-login one; got %v", err)
	}
}

func anInstanceLoginSecretIn(ns string) *corev1.Secret {
	return mintedSecret(ns, "dci-acme-rdb-credentials", testUID,
		map[string]string{"username": "acme", "password": "pw"})
}
