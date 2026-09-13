// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// mintedSecret builds a Secret as dcctl would have left it on an earlier run.
//
// Data, not StringData: StringData is write-only at the API server, so a fixture
// built from it reads back empty and every reuse test would pass for the wrong
// reason — the value would look absent exactly where the code is supposed to find it.
func mintedSecret(ns, name, uid string, data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				annotationManagedBy: managedByDcctl,
				annotationOwnerName: testInstance,
				annotationOwnerUID:  uid,
			},
		},
		Data: d,
	}
}

func rdbRef() mintedCredentialRef {
	return mintedCredentialRef{infraNamespace, rdbClusterName + "-app-credentials", secretKeyPassword}
}

// 🔴 THE CREDENTIAL A LIVE INSTANCE IS RUNNING ON IS THE ONE IT KEEPS. CloudNativePG
// sets the owner role's password when it creates the Cluster and never again, so a
// re-run that minted a fresh one would reach every service and neither database.
func TestALiveInstancesDatabasePasswordIsReadBackRatherThanReminted(t *testing.T) {
	c := fake.NewSimpleClientset(mintedSecret(infraNamespace,
		rdbClusterName+"-app-credentials", testUID,
		map[string]string{secretKeyUsername: dbRoleUsername, secretKeyPassword: "the-one-in-force"}))

	_, got, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err != nil {
		t.Fatalf("reading back a credential this instance minted: %v", err)
	}
	if got != "the-one-in-force" {
		t.Errorf("reuse returned %q, so the run would mint over a live database", got)
	}
}

// A first bootstrap has nothing to read back, and that IS an answer.
func TestAFirstBootstrapHasNothingToReuse(t *testing.T) {
	c := fake.NewSimpleClientset()
	_, got, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err != nil {
		t.Fatalf("an absent Secret was treated as a failure: %v", err)
	}
	if got != "" {
		t.Errorf("reuse invented %q out of an empty cluster", got)
	}
}

// 🔴 MALFORMED IS NOT ABSENT. A Secret that is ours but carries no value under the
// key cannot be reused AND must not be read as "nothing here" — that reading mints a
// replacement over a database whose role keeps the old password, on a green run.
func TestAnOursButEmptyCredentialIsRefusedRatherThanReadAsAbsent(t *testing.T) {
	c := fake.NewSimpleClientset(mintedSecret(infraNamespace,
		rdbClusterName+"-app-credentials", testUID,
		map[string]string{secretKeyUsername: dbRoleUsername, secretKeyPassword: ""}))

	_, _, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err == nil {
		t.Fatal("an empty credential was read as an absent one, so the run would mint over it")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the refusal does not say what it found: %v", err)
	}
}

// A read that fails is not an empty cluster either. Minting is the answer that cannot
// be undone, so it must not be reached by failing to look.
func TestAnUnreadableSecretStopsTheRunRatherThanMinting(t *testing.T) {
	boom := errors.New("apiserver is unavailable")
	c := fake.NewSimpleClientset()
	c.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, boom
	})

	_, _, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err == nil {
		t.Fatal("an unreadable Secret was read as an absent one")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the failure lost its cause: %v", err)
	}
}

// A Secret belonging to a previous generation of this name is NOT reused — that is
// how a rebuild would inherit a dead instance's credentials. It is left for
// writeOwnedSecret to refuse, which already says which case it is and what to do.
func TestAPreviousGenerationsCredentialIsNotInherited(t *testing.T) {
	c := fake.NewSimpleClientset(mintedSecret(infraNamespace,
		rdbClusterName+"-app-credentials", "99999999-9999-9999-9999-999999999999",
		map[string]string{secretKeyPassword: "belongs-to-the-dead-one"}))

	_, got, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	if got != "" {
		t.Errorf("reused %q from a declaration that is gone", got)
	}
}

// ...and neither is one dcctl did not write at all.
func TestAForeignCredentialIsNotReused(t *testing.T) {
	s := mintedSecret(infraNamespace, rdbClusterName+"-app-credentials", testUID,
		map[string]string{secretKeyPassword: "somebody-elses"})
	delete(s.Annotations, annotationManagedBy)
	c := fake.NewSimpleClientset(s)

	_, got, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	if got != "" {
		t.Errorf("reused %q from a Secret dcctl did not write", got)
	}
}

// 🔴 A CLUSTER THAT OUTLIVED ITS CREDENTIALS SECRET IS UNRECOVERABLE, AND SAYING SO
// IS THE WHOLE VALUE. The password is hashed in PostgreSQL and unreconciled by CNPG,
// so it exists nowhere else; minting a replacement produces a healthy-looking
// database that refuses every service.
func TestARunningDatabaseWithNoCredentialsSecretIsRefused(t *testing.T) {
	err := refuseUnrecoverableDatabaseCredential(true, reuseAbsent, rdbClusterName, rdbClusterName+"-app-credentials")
	if err == nil {
		t.Fatal("a live database with no recoverable credential was allowed to be re-minted")
	}
	for _, want := range []string{"cannot be recovered", "dcctl destroy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// The same absence on a cluster that does NOT exist is an ordinary fresh install.
func TestAnAbsentCredentialIsFineWhenThereIsNoDatabaseYet(t *testing.T) {
	if err := refuseUnrecoverableDatabaseCredential(false, reuseAbsent, rdbClusterName, "x"); err != nil {
		t.Errorf("a first bootstrap was refused: %v", err)
	}
}

// And a live cluster whose credential DID come back is the ordinary re-run.
func TestALiveDatabaseWithItsCredentialIsNotRefused(t *testing.T) {
	if err := refuseUnrecoverableDatabaseCredential(true, reuseRecovered, rdbClusterName, "x"); err != nil {
		t.Errorf("an ordinary re-run was refused: %v", err)
	}
}

// 🔴 A SECRET THAT IS THERE AND NOT OURS MUST NOT BE REPORTED AS MISSING. This is
// every instance built before dcctl owned these credentials: the Secret exists and
// belongs to OpenTofu. Before the outcome was distinguished from the empty string,
// the refusal told that operator "the only copy of the password ... is gone" about
// an object sitting in front of them — measured against a real 30-day-old instance,
// whose state holds exactly the nine addresses the fence lists.
//
// The right refusal for that instance is the fence's, which says what actually
// happened. This one has to stay out of the way.
func TestAPreCutoverInstancesForeignSecretIsNotReportedAsMissing(t *testing.T) {
	// As OpenTofu wrote it: real content, no dcctl ownership.
	tofuWritten := mintedSecret(infraNamespace, rdbClusterName+"-app-credentials", "",
		map[string]string{secretKeyUsername: dbRoleUsername, secretKeyPassword: "tofus-value"})
	tofuWritten.Annotations = map[string]string{}
	tofuWritten.Labels = map[string]string{"app.kubernetes.io/managed-by": "opentofu"}
	c := fake.NewSimpleClientset(tofuWritten)

	found, _, err := reuseMintedCredential(context.Background(), c, testInstance, testUID, rdbRef())
	if err != nil {
		t.Fatalf("reading a Secret OpenTofu wrote: %v", err)
	}
	if found != reuseForeign {
		t.Fatalf("a Secret dcctl did not write reported as %v, not reuseForeign", found)
	}
	// The Cluster is live, which is precisely when the old code fired.
	if err := refuseUnrecoverableDatabaseCredential(true, found, rdbClusterName,
		rdbClusterName+"-app-credentials"); err != nil {
		t.Errorf("a present-but-foreign credential was reported as unrecoverable, which "+
			"describes a cluster the operator is not looking at: %v", err)
	}
}
