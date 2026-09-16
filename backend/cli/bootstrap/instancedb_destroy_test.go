// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
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
