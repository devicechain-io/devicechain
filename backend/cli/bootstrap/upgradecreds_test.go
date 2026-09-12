// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// settleStringDataLikeAnAPIServer does to the fake client what a real API server does
// on admission: it moves StringData into Data and clears it.
//
// 🔴 WITHOUT IT THIS ROUND TRIP IS A FIXTURE TESTING ITSELF. ownedSecret writes
// through StringData, which is write-only at a real API server — what comes back is
// Data — and the fake client does no such conversion. So a reader that looked in the
// right place would find nothing, and one that looked in StringData would pass here
// and find nothing in production. reuse_test.go's own fixtures are built from Data
// for exactly this reason; this is the same fact stated from the writing side,
// because this test is the only one that puts the writer and the reader end to end.
func settleStringDataLikeAnAPIServer(t *testing.T, c *fake.Clientset) {
	t.Helper()
	list, err := c.CoreV1().Secrets("").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing the Secrets that were written: %v", err)
	}
	for i := range list.Items {
		s := list.Items[i]
		if len(s.StringData) == 0 {
			continue
		}
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		for k, v := range s.StringData {
			s.Data[k] = []byte(v)
		}
		s.StringData = nil
		if _, err := c.CoreV1().Secrets(s.Namespace).Update(
			context.Background(), &s, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("storing Secret %s/%s the way an API server would: %v", s.Namespace, s.Name, err)
		}
	}
}

// The round trip, and it is the test that matters: write an instance's credentials
// the way bootstrap writes them, then recover them the way an upgrade recovers them,
// and require every single one to come back.
//
// 🔴 IT WALKS THE STRUCT RATHER THAN A LIST OF FIELDS TYPED HERE. A list is exactly
// the artifact that cannot see the failure this is guarding against — a credential
// added to credentialSet, minted, written, and never given a placement. The reader
// would return it empty, the upgrade would compose a document with a blank password
// in it, and a test enumerating the fields somebody remembered would stay green.
// That is not hypothetical: the dashboard password was minted on every run and
// placed in no Secret at all, and nothing could see it until a test started walking
// the struct.
func TestEveryCredentialTheInstanceRunsOnIsRecoveredFromWhatWasWritten(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the credentials an instance would be built with: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)

	got, err := readInstanceCredentials(context.Background(), c, st)
	if err != nil {
		t.Fatalf("recovering the credentials that were just written: %v", err)
	}

	want := reflect.ValueOf(*st.Credentials)
	have := reflect.ValueOf(*got)
	typ := want.Type()
	for i := 0; i < want.NumField(); i++ {
		name := typ.Field(i).Name
		w, h := want.Field(i).String(), have.Field(i).String()
		if w == "" {
			// Not part of this configuration, so there was nothing to write and
			// nothing to recover. The configurations where that is WRONG are covered
			// by TestThePlacementsAreWhereThePlanActuallyPutsThem, which compares the
			// two sides under each one.
			continue
		}
		if h == "" {
			t.Errorf("credentialSet.%s was written and the upgrade recovered nothing: it has no "+
				"placement, so an upgrade would compose this instance's configuration with the "+
				"field blank", name)
			continue
		}
		if h != w {
			t.Errorf("credentialSet.%s came back as a different value than was written", name)
		}
	}
}

// The writer and the reader are held against each other under every configuration
// that changes which credentials exist, because the two lists agreeing today is not
// the property — the property is that they cannot disagree tomorrow.
func TestThePlacementsAreWhereThePlanActuallyPutsThem(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   *State
	}{
		{"everything on", aWritableState()},
		{"no monitoring", func() *State { s := aWritableState(); s.NoMonitoring = true; return s }()},
		{"no databases", func() *State { s := aWritableState(); s.NoCNPG = true; return s }()},
		{"backups to an object store the operator owns", func() *State {
			s := aWritableState()
			s.BackupDestination = &BackupDestination{
				EndpointURL: "https://example.invalid", BucketRdb: "a", BucketTsdb: "b",
				AccessKeyID: "k", SecretAccessKey: "s",
			}
			return s
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// What the writer decided to put where.
			planned := map[string]string{}
			for _, s := range planOwnedSecrets(tc.st, tc.st.Credentials) {
				for k, v := range s.Data {
					planned[s.Namespace+"/"+s.Name+":"+k] = v
				}
			}

			placed := map[string]bool{}
			for _, p := range credentialPlacements(tc.st) {
				at := p.Ref.Namespace + "/" + p.Ref.Name + ":" + p.Ref.Key
				want := *p.Into(tc.st.Credentials)
				got, ok := planned[at]
				if !ok {
					t.Errorf("the upgrade looks for %s at %s and the writer puts nothing there",
						p.Field, at)
					continue
				}
				if got != want {
					t.Errorf("the upgrade reads %s from %s, but the writer puts a different "+
						"credential in that key", p.Field, at)
				}
				placed[p.Field] = true
			}

			// ...and the other direction. A credential the writer places with no
			// placement to read it back is one an upgrade silently blanks.
			set := reflect.ValueOf(*tc.st.Credentials)
			typ := set.Type()
			for i := 0; i < set.NumField(); i++ {
				name := typ.Field(i).Name
				value := set.Field(i).String()
				if value == "" || placed[name] {
					continue
				}
				for at, v := range planned {
					if v == value {
						t.Errorf("the writer puts credentialSet.%s at %s and no placement reads "+
							"it back, so an upgrade would compose this instance without it",
							name, at)
					}
				}
			}
		})
	}
}

// 🔴 AN ABSENT CREDENTIAL IS A REFUSAL, NOT A MINT. This is the whole difference
// between the verb that creates an instance and the verb that evolves one, and it is
// a single missing branch away from being lost: "mint if it isn't there" reads as
// helpful and silently hands a live database a password it has never been told
// about.
func TestAMissingCredentialIsRefusedRatherThanMinted(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the credentials: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	// Take away exactly one, leaving everything else healthy.
	if err := c.CoreV1().Secrets(infraNamespace).Delete(context.Background(),
		rdbClusterName+"-app-credentials", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the relational store's credential: %v", err)
	}

	got, err := readInstanceCredentials(context.Background(), c, st)
	if err == nil {
		t.Fatalf("an upgrade recovered a credential that is gone and returned %+v", got)
	}
	if !strings.Contains(err.Error(), rdbClusterName+"-app-credentials") {
		t.Errorf("the refusal does not name the Secret an operator has to restore: %v", err)
	}
	if !strings.Contains(err.Error(), "mints nothing") {
		t.Errorf("the refusal does not say that an upgrade will not substitute a fresh value, "+
			"which is the thing that makes it a refusal rather than a failure: %v", err)
	}
}

// ...and the counterweight the live cluster taught this package: PRESENT BUT NOT
// OURS is not ABSENT. A Secret sitting right there must never be described as gone —
// that sentence sends an operator looking for a backup of something they are already
// holding.
func TestACredentialThatIsNotOursIsNamedAsOwnershipNotAbsence(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	if err := writeMintedSecrets(context.Background(), c, st); err != nil {
		t.Fatalf("writing the credentials: %v", err)
	}
	settleStringDataLikeAnAPIServer(t, c)
	// Replace one with a Secret that is present, populated, and somebody else's —
	// the shape every instance built before dcctl owned these credentials has.
	name := rdbClusterName + "-app-credentials"
	if err := c.CoreV1().Secrets(infraNamespace).Delete(context.Background(),
		name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("removing the relational store's credential: %v", err)
	}
	if _, err := c.CoreV1().Secrets(infraNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   infraNamespace,
			Annotations: map[string]string{"app.kubernetes.io/managed-by": "opentofu"},
		},
		Data: map[string][]byte{secretKeyPassword: []byte("someone else's")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("planting a Secret dcctl did not write: %v", err)
	}

	_, err := readInstanceCredentials(context.Background(), c, st)
	if err == nil {
		t.Fatal("an upgrade read a credential out of a Secret dcctl did not write")
	}
	if strings.Contains(err.Error(), "is gone") {
		t.Errorf("a Secret that is present was reported as missing, which is the message that "+
			"meets every instance built before dcctl owned these: %v", err)
	}
	if !strings.Contains(err.Error(), "not written by dcctl") {
		t.Errorf("the refusal does not say what it actually found: %v", err)
	}
}

// A read that cannot complete must not resolve to the permissive answer either. This
// is the same rule reuseMintedCredential follows, restated at this level because the
// consequence here is different: bootstrap would mint, and an upgrade would compose a
// configuration with a blank credential in it.
func TestAnUnreadableCredentialStopsTheUpgrade(t *testing.T) {
	st := aWritableState()
	c := fake.NewSimpleClientset()
	c.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server is not answering")
	})

	if _, err := readInstanceCredentials(context.Background(), c, st); err == nil {
		t.Fatal("an upgrade continued past an API server that would not answer")
	}
}
