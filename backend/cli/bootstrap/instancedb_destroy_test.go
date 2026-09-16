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
	return mintedSecret(infraNamespace, "dci-acme-rdb-credentials", testUID,
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
	if err := removeInstanceRelationalLogin(context.Background(), c, "kind-x", "acme"); err != nil {
		t.Fatalf("removing the instance's login: %v", err)
	}
	if gotInstance != "acme" || got != aRelationalStore() {
		t.Errorf("dropped %q through %+v, want acme through the recorded store", gotInstance, got)
	}
	if _, err := c.CoreV1().Secrets(infraNamespace).Get(context.Background(), "dci-acme-rdb-credentials",
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
	if err := removeInstanceRelationalLogin(context.Background(), c, "kind-x", "acme"); err == nil {
		t.Fatal("a failed drop was reported as success")
	}
	if _, err := c.CoreV1().Secrets(infraNamespace).Get(context.Background(), "dci-acme-rdb-credentials",
		metav1.GetOptions{}); err != nil {
		t.Errorf("the login's Secret was removed although its login was not: %v", err)
	}
}

// No login Secret means no login was ever made; the store is not touched. With a Secret
// but no readable record, the destroy fails rather than guessing where the store is.
func TestTheDropNeedsALoginAndARecord(t *testing.T) {
	called := false
	stubRemoveInstanceDatabase(t, func(context.Context, string, string, ClusterRdb) error {
		called = true
		return nil
	})
	if err := removeInstanceRelationalLogin(context.Background(),
		fake.NewSimpleClientset(kubeSystem(testClusterUID)), "kind-x", "acme"); err != nil || called {
		t.Errorf("an instance with no login: err=%v, store touched=%t", err, called)
	}
	if err := removeInstanceRelationalLogin(context.Background(),
		fake.NewSimpleClientset(kubeSystem(testClusterUID), anInstanceLoginSecret()), "kind-x", "acme"); err == nil || called {
		t.Errorf("a login with no install record: err=%v, store touched=%t", err, called)
	}
}

// An upgrade of an instance built before per-instance logins is told to rebuild, not to
// restore a Secret that never existed.
func TestAnUpgradeOfAnInstanceWithNoLoginSaysRebuildNotRestore(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatal(err)
	}
	if err := c.CoreV1().Secrets(infraNamespace).Delete(context.Background(),
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
// SHARED STORE, and nothing a unit test drives reaches destroyInstanceOnly — it needs a
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
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "destroyInstanceOnly" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("destroy.go no longer declares destroyInstanceOnly")
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
	for _, name := range []string{"helmUninstall", "removeInstanceRelationalLogin"} {
		if _, ok := pos[name]; !ok {
			t.Fatalf("destroyInstanceOnly no longer calls %s; an instance destroy would leave its "+
				"database and login on the shared store", name)
		}
	}
	if pos["removeInstanceRelationalLogin"] < pos["helmUninstall"] {
		t.Error("the database is dropped before the services that hold sessions on it are uninstalled")
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
