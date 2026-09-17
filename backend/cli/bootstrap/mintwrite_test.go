// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func aWritableState() *State {
	return &State{
		Instance:    testInstance,
		InstanceUID: testUID,
		ClusterUID:  testClusterUID,
		Values:      map[string]string{},
		Credentials: &credentialSet{
			RDBPassword:            "rdb-pw",
			RDBProvisionerPassword: "rdb-provisioner-pw",
			RDBInstancePassword:    "rdb-instance-pw",
			TSDBPassword:           "tsdb-pw",
			ObjectStoreUser:        "os-user",
			ObjectStoreSecret:      "os-secret",
			GrafanaAdminPassword:   "grafana-pw",
		},
	}
}

// writeInstallThenBootstrapSecrets writes what an install and then a bootstrap write, in
// that order, into one cluster: the cluster half, then the instance half carrying its
// copy of the cluster's archive credential — which a bootstrap reads back from the
// cluster (readClusterArchiveCredential) and this takes straight from the plan.
func writeInstallThenBootstrapSecrets(t *testing.T, c kubernetes.Interface, st *State) {
	t.Helper()
	if err := writeClusterSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the cluster's credentials the way an install does: %v", err)
	}
	_, st.InstanceArchive = planClusterSecrets(st, st.Credentials)
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the instance's credentials the way a bootstrap does: %v", err)
	}
}

// assertWritten requires every spec to be in the cluster, stamped with its scope's owner.
func assertWritten(t *testing.T, c kubernetes.Interface, st *State, specs []ownedSecret, writer string) {
	t.Helper()
	for _, spec := range specs {
		s, err := c.CoreV1().Secrets(spec.Namespace).Get(context.Background(), spec.Name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("%s/%s was planned for %s but never written: %v", spec.Namespace, spec.Name, writer, err)
			continue
		}
		if got, want := readOwnership(s).owner, ownerFor(spec.Scope, st); got != want {
			t.Errorf("%s/%s was written as %s's, want %s's", spec.Namespace, spec.Name, got, want)
		}
	}
}

// assertNotWritten requires none of specs to be in the cluster.
func assertNotWritten(t *testing.T, c kubernetes.Interface, specs []ownedSecret, writer, why string) {
	t.Helper()
	for _, spec := range specs {
		if _, err := c.CoreV1().Secrets(spec.Namespace).Get(context.Background(), spec.Name, metav1.GetOptions{}); err == nil {
			t.Errorf("%s wrote %s/%s — %s", writer, spec.Namespace, spec.Name, why)
		}
	}
}

// Every credential the plan places is actually written, by the writer whose half it is,
// and the broker's TLS with the instance's. The count is asserted against the plan
// rather than a number typed here: a literal would pass unchanged the day a credential
// stops being written.
//
// 🔴 AND EACH WRITER WRITES ONLY ITS OWN HALF. The cluster's credentials are what every
// instance on the cluster runs on; a bootstrap writing them again is a second writer of
// a value the install owns, and an install writing an instance's is a Secret for an
// instance that does not exist.
func TestEveryPlannedCredentialIsWrittenBeforeTheApply(t *testing.T) {
	st := aWritableState()
	st.NATSTLS = &natsTLSMaterial{
		CACertPEM: "ca", CAKeyPEM: "cakey", LeafCertPEM: "leaf", LeafKeyPEM: "leafkey",
	}
	clusterHalf, archive := planClusterSecrets(st, st.Credentials)
	if archive == nil {
		t.Fatal("the fixture plans no archive credential, so the instance's copy of it is not exercised")
	}
	instanceHalf := planInstanceSecrets(st, st.Credentials, archive)
	tls := natsTLSSecret(natsReleaseName, st.NATSTLS)

	t.Run("the install's writer", func(t *testing.T) {
		c := fake.NewSimpleClientset()
		if err := writeClusterSecrets(context.Background(), c, st); err != nil {
			t.Fatalf("writing the cluster's credentials: %v", err)
		}
		assertWritten(t, c, st, clusterHalf, "the install")
		assertNotWritten(t, c, instanceHalf, "the install's writer",
			"an instance's credentials are its bootstrap's")
		if _, err := c.CoreV1().Secrets(tls.Namespace).Get(context.Background(), tls.Name, metav1.GetOptions{}); err == nil {
			t.Error("the install's writer wrote an instance broker's TLS material")
		}
	})

	t.Run("the bootstrap's writer", func(t *testing.T) {
		st := aWritableState()
		st.NATSTLS = &natsTLSMaterial{
			CACertPEM: "ca", CAKeyPEM: "cakey", LeafCertPEM: "leaf", LeafKeyPEM: "leafkey",
		}
		st.InstanceArchive = archive
		c := fake.NewSimpleClientset()
		if err := writeMintedSecrets(context.Background(), c, st); err != nil {
			t.Fatalf("writing the instance's credentials: %v", err)
		}
		assertWritten(t, c, st, instanceHalf, "the bootstrap")
		assertNotWritten(t, c, clusterHalf, "the bootstrap's writer",
			"the cluster's credentials are the install's, and every other instance runs on them")
		if _, err := c.CoreV1().Secrets(tls.Namespace).Get(context.Background(), tls.Name, metav1.GetOptions{}); err != nil {
			t.Errorf("the broker's TLS material was not written: %v", err)
		}
	})
}

// 🔴 A BOOTSTRAP FOLLOWING AN INSTALL WRITES NOTHING THE CLUSTER OWNS — not even when its
// credential set happens to carry cluster values. Asked of the cluster rather than of
// the plan: every Secret that lands is the instance's.
func TestABootstrapWritesNoClusterOwnedCredential(t *testing.T) {
	st := aWritableState()
	rec := aCompleteInstall()
	FollowInstall(st, &rec)
	_, st.InstanceArchive = planClusterSecrets(st, st.Credentials)
	if st.InstanceArchive == nil {
		t.Fatal("the recorded install archives nowhere, so the instance's archive copy is not exercised")
	}
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing a bootstrap's credentials: %v", err)
	}
	list, err := c.CoreV1().Secrets("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatal("the bootstrap wrote nothing at all, so the check below is vacuous")
	}
	for _, s := range list.Items {
		if own := readOwnership(&s).owner; own.Kind != ownerInstance || s.Namespace != instanceNamespace(st.Instance) {
			t.Errorf("a bootstrap wrote %s/%s as %s's; everything it writes is its instance's, in its namespace",
				s.Namespace, s.Name, own)
		}
	}
}

// ...and an install, which has no instance, writes nothing an instance owns.
func TestAnInstallWritesNoInstanceCredential(t *testing.T) {
	st := aWritableState()
	st.Instance, st.InstanceUID = "", ""
	c := fake.NewSimpleClientset()
	if err := writeClusterSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing an install's credentials: %v", err)
	}
	list, err := c.CoreV1().Secrets("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatal("the install wrote nothing at all, so the check below is vacuous")
	}
	for _, s := range list.Items {
		if own := readOwnership(&s).owner; own.Kind != ownerCluster {
			t.Errorf("an install wrote %s/%s as %s's; it has no instance to write for", s.Namespace, s.Name, own)
		}
	}
}

// 🔴 THE DASHBOARD CREDENTIAL'S NAMESPACE IS CREATED BY THE APPLY THIS WRITE
// PRECEDES. Without making it first, the write fails on a namespace that does not
// exist yet — and it is the only planned Secret that does not live in the
// infrastructure namespace, so nothing else would have caught it. It is the install's
// credential, so it is the install's writer that has to make it.
func TestTheMonitoringNamespaceIsMadeBeforeTheDashboardCredentialLandsInIt(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()

	if err := writeClusterSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing into a cluster with no monitoring namespace: %v", err)
	}
	if _, err := c.CoreV1().Namespaces().Get(context.Background(), monitoringNamespace, metav1.GetOptions{}); err != nil {
		t.Fatalf("the monitoring namespace was never created: %v", err)
	}
	if _, err := c.CoreV1().Secrets(monitoringNamespace).Get(context.Background(), grafanaSecretName, metav1.GetOptions{}); err != nil {
		t.Errorf("the dashboard credential did not land: %v", err)
	}

	// The instance's writer puts nothing there, so it has no reason to make it.
	c = fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the instance's credentials: %v", err)
	}
	if _, err := c.CoreV1().Namespaces().Get(context.Background(), monitoringNamespace, metav1.GetOptions{}); err == nil {
		t.Error("a bootstrap created the monitoring namespace; the dashboard credential is the install's")
	}
}

// ...and it is not created for an install that has no monitoring stack, which would be
// an empty namespace nothing ever fills.
func TestNoMonitoringNamespaceIsMadeWhenThereIsNoMonitoringStack(t *testing.T) {
	st := aWritableState()
	st.NoMonitoring = true
	c := fake.NewSimpleClientset()

	if err := writeClusterSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing with monitoring off: %v", err)
	}
	if _, err := c.CoreV1().Namespaces().Get(context.Background(), monitoringNamespace, metav1.GetOptions{}); err == nil {
		t.Error("a monitoring namespace was created for a cluster with no monitoring stack")
	}
}

// 🔴 APPLYING WITH NO SETTLED CREDENTIALS MUST NOT BE A QUIET NO-OP, for either root.
// Writing nothing and running the apply anyway is the path where CloudNativePG mints a
// password of its own and the services never learn it.
func TestApplyingWithoutSettledCredentialsIsRefused(t *testing.T) {
	for name, write := range map[string]func(context.Context, kubernetes.Interface, *State) error{
		"the instance": writeMintedSecrets,
		"the cluster":  writeClusterSecrets,
	} {
		t.Run(name, func(t *testing.T) {
			st := aWritableState()
			st.Credentials = nil
			c := fake.NewSimpleClientset()

			err := write(context.Background(), c, st)
			if err == nil {
				t.Fatal("an apply with no settled credentials was allowed to proceed")
			}
			if !strings.Contains(err.Error(), "never settled") {
				t.Errorf("the refusal does not say what is missing: %v", err)
			}
		})
	}
}

// 🔴 THE DATABASE PASSWORDS IN THE DOCUMENT ARE THE ONES THAT WENT INTO THE SECRETS.
// They travel by different routes — one through a Secret CloudNativePG builds the
// role from, the other through the configuration every service reads — and if they
// can differ, the database refuses its own services on a green run.
func TestTheDocumentCarriesTheSamePasswordsTheDatabasesWereBuiltWith(t *testing.T) {
	st := aWritableState()
	vals := helmValues(st)

	instance, _ := vals["instance"].(map[string]interface{})
	cfg, _ := instance["config"].(map[string]interface{})
	persistence, ok := cfg["persistence"].(map[string]interface{})
	if !ok {
		t.Fatal("the rendered values carry no persistence block, so the services would " +
			"connect with the chart's default password")
	}

	// 🔴 THE RELATIONAL STORE'S IS THE INSTANCE'S OWN LOGIN, not the store's owner: the
	// owner's password reaching the document would put every instance's services on one
	// shared identity again.
	for _, c := range []struct{ store, want, user string }{
		{"rdb", st.Credentials.RDBInstancePassword, st.Instance},
		{"tsdb", st.Credentials.TSDBPassword, dbRoleUsername},
	} {
		store, _ := persistence[c.store].(map[string]interface{})
		conf, _ := store["configuration"].(map[string]interface{})
		if got, _ := conf["password"].(string); got != c.want {
			t.Errorf("%s password in the document is %q, not the %q the Secret was written with",
				c.store, got, c.want)
		}
		if got, _ := conf["username"].(string); got != c.user {
			t.Errorf("%s username in the document is %q, not the role %q that actually exists",
				c.store, got, c.user)
		}
	}
}

// A run that settled no credentials renders no persistence block at all, rather than
// one full of empty strings that would look authored.
func TestNoCredentialsMeansNoPersistenceBlockRatherThanAnEmptyOne(t *testing.T) {
	st := aWritableState()
	st.Credentials = nil
	vals := helmValues(st)
	instance, _ := vals["instance"].(map[string]interface{})
	if cfg, ok := instance["config"].(map[string]interface{}); ok {
		if _, has := cfg["persistence"]; has {
			t.Error("a persistence block was rendered from credentials that were never settled")
		}
	}
}
