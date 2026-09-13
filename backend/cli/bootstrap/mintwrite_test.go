// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func aWritableState() *State {
	return &State{
		Instance:    testInstance,
		InstanceUID: testUID,
		Values:      map[string]string{},
		Credentials: &credentialSet{
			RDBPassword:          "rdb-pw",
			TSDBPassword:         "tsdb-pw",
			ObjectStoreUser:      "os-user",
			ObjectStoreSecret:    "os-secret",
			GrafanaAdminPassword: "grafana-pw",
		},
	}
}

// Every credential the plan places is actually written, and the broker's TLS with
// them. The count is asserted against the plan rather than a number typed here: a
// literal would pass unchanged the day a credential stops being written.
func TestEveryPlannedCredentialIsWrittenBeforeTheApply(t *testing.T) {
	st := aWritableState()
	st.NATSTLS = &natsTLSMaterial{
		CACertPEM: "ca", CAKeyPEM: "cakey", LeafCertPEM: "leaf", LeafKeyPEM: "leafkey",
	}
	c := fake.NewSimpleClientset()

	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the minted credentials: %v", err)
	}

	for _, spec := range planOwnedSecrets(st, st.Credentials) {
		s, err := c.CoreV1().Secrets(spec.Namespace).Get(context.Background(), spec.Name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("%s/%s was planned but never written: %v", spec.Namespace, spec.Name, err)
			continue
		}
		if readOwnership(s).instance != testInstance {
			t.Errorf("%s/%s was written without this instance's ownership", spec.Namespace, spec.Name)
		}
	}

	tls := natsTLSSecret(natsReleaseName, st.NATSTLS)
	if _, err := c.CoreV1().Secrets(tls.Namespace).Get(context.Background(), tls.Name, metav1.GetOptions{}); err != nil {
		t.Errorf("the broker's TLS material was not written: %v", err)
	}
}

// 🔴 THE DASHBOARD CREDENTIAL'S NAMESPACE IS CREATED BY THE APPLY THIS WRITE
// PRECEDES. Without making it first, the write fails on a namespace that does not
// exist yet — and it is the only planned Secret that does not live in the
// infrastructure namespace, so nothing else would have caught it.
func TestTheMonitoringNamespaceIsMadeBeforeTheDashboardCredentialLandsInIt(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()

	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing into a cluster with no monitoring namespace: %v", err)
	}
	if _, err := c.CoreV1().Namespaces().Get(context.Background(), monitoringNamespace, metav1.GetOptions{}); err != nil {
		t.Fatalf("the monitoring namespace was never created: %v", err)
	}
	if _, err := c.CoreV1().Secrets(monitoringNamespace).Get(context.Background(), grafanaSecretName, metav1.GetOptions{}); err != nil {
		t.Errorf("the dashboard credential did not land: %v", err)
	}
}

// ...and it is not created for a run that has no monitoring stack, which would be an
// empty namespace nothing ever fills.
func TestNoMonitoringNamespaceIsMadeWhenThereIsNoMonitoringStack(t *testing.T) {
	st := aWritableState()
	st.NoMonitoring = true
	c := fake.NewSimpleClientset()

	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing with monitoring off: %v", err)
	}
	if _, err := c.CoreV1().Namespaces().Get(context.Background(), monitoringNamespace, metav1.GetOptions{}); err == nil {
		t.Error("a monitoring namespace was created for an instance with no monitoring stack")
	}
}

// 🔴 APPLYING WITH NO SETTLED CREDENTIALS MUST NOT BE A QUIET NO-OP. Writing nothing
// and running the apply anyway is the path where CloudNativePG mints a password of
// its own and the services never learn it.
func TestApplyingWithoutSettledCredentialsIsRefused(t *testing.T) {
	st := aWritableState()
	st.Credentials = nil

	err := writeMintedSecrets(context.Background(), fake.NewSimpleClientset(), st)
	if err == nil {
		t.Fatal("an apply with no settled credentials was allowed to proceed")
	}
	if !strings.Contains(err.Error(), "never settled") {
		t.Errorf("the refusal does not say what is missing: %v", err)
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

	for _, c := range []struct{ store, want string }{
		{"rdb", st.Credentials.RDBPassword},
		{"tsdb", st.Credentials.TSDBPassword},
	} {
		store, _ := persistence[c.store].(map[string]interface{})
		conf, _ := store["configuration"].(map[string]interface{})
		if got, _ := conf["password"].(string); got != c.want {
			t.Errorf("%s password in the document is %q, not the %q the Secret was written with",
				c.store, got, c.want)
		}
		if got, _ := conf["username"].(string); got != dbRoleUsername {
			t.Errorf("%s username in the document is %q, not the role %q that actually exists",
				c.store, got, dbRoleUsername)
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
