// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testInstance = "acme"
	testUID      = "11111111-1111-1111-1111-111111111111"
)

func fixedClock(s string) func() time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

func aSpec() ownedSecret {
	return ownedSecret{
		Name:      "dc-rdb-app-credentials",
		Namespace: "dc-system",
		Type:      corev1.SecretTypeBasicAuth,
		Labels:    map[string]string{"cnpg.io/reload": "true"},
		Data:      map[string]string{"username": "devicechain", "password": "s3cret"},
	}
}

func getSecret(t *testing.T, c *fake.Clientset, ns, name string) *corev1.Secret {
	t.Helper()
	s, err := c.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading back %s/%s: %v", ns, name, err)
	}
	return s
}

func TestAMintedSecretCarriesItsOwnershipAndItsStamp(t *testing.T) {
	c := fake.NewSimpleClientset()
	err := writeOwnedSecret(context.Background(), c, testInstance, testUID, aSpec(),
		fixedClock("2026-09-11T10:00:00Z"))
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	s := getSecret(t, c, "dc-system", "dc-rdb-app-credentials")
	for k, want := range map[string]string{
		annotationManagedBy: managedByDcctl,
		annotationOwnerName: testInstance,
		annotationOwnerUID:  testUID,
		annotationMintedAt:  "2026-09-11T10:00:00Z",
	} {
		if got := s.Annotations[k]; got != want {
			t.Errorf("annotation %s = %q, want %q", k, got, want)
		}
	}
	if s.Type != corev1.SecretTypeBasicAuth {
		t.Errorf("type = %q, want basic-auth", s.Type)
	}
	// 🔴 Without this label CloudNativePG does not act on a credential change, so
	// the write lands and the database keeps the old password. Nothing reports it.
	if s.Labels["cnpg.io/reload"] != "true" {
		t.Errorf("the reload label did not reach the Secret: %v", s.Labels)
	}
	if s.StringData["password"] != "s3cret" {
		t.Errorf("payload did not reach the Secret: %v", s.StringData)
	}
}

// 🔴 THE REFUSAL THIS FILE EXISTS FOR. Each case is a Secret that is present and is
// not ours, and every one of them would be silently destructive to overwrite: a
// database stops admitting its own services, or a root key makes every encrypted row
// unreadable. None of them errors at the time.
func TestAForeignSecretIsRefusedRatherThanOverwritten(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		wantInMsg   string
	}{
		{
			name:        "written by something that is not dcctl",
			annotations: map[string]string{"app.kubernetes.io/managed-by": "opentofu"},
			wantInMsg:   "did not write it",
		},
		{
			name:        "no annotations at all — the chart-rendered shape",
			annotations: nil,
			wantInMsg:   "did not write it",
		},
		{
			name: "ours, but a different instance",
			annotations: map[string]string{
				annotationManagedBy: managedByDcctl,
				annotationOwnerName: "other",
				annotationOwnerUID:  testUID,
			},
			wantInMsg: `belongs to instance "other"`,
		},
		{
			name: "the same instance NAME, a previous generation",
			annotations: map[string]string{
				annotationManagedBy: managedByDcctl,
				annotationOwnerName: testInstance,
				annotationOwnerUID:  "99999999-9999-9999-9999-999999999999",
			},
			wantInMsg: "previous",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewSimpleClientset(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "dc-rdb-app-credentials",
					Namespace:   "dc-system",
					Annotations: tc.annotations,
				},
				StringData: map[string]string{"password": "the-live-one"},
			})

			err := writeOwnedSecret(context.Background(), c, testInstance, testUID, aSpec(),
				fixedClock("2026-09-11T10:00:00Z"))
			if err == nil {
				t.Fatal("overwrote a Secret dcctl did not write")
			}
			var foreign *ErrForeignSecret
			if !errors.As(err, &foreign) {
				t.Fatalf("want an ErrForeignSecret so callers can tell this from an API "+
					"failure, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Errorf("message does not say why:\nwant to contain %q\ngot %s", tc.wantInMsg, err)
			}

			// The counterweight, and the one that matters: refusing is worth nothing
			// if the value was already gone.
			s := getSecret(t, c, "dc-system", "dc-rdb-app-credentials")
			if s.StringData["password"] != "the-live-one" {
				t.Fatalf("the live credential was modified despite the refusal: %v", s.StringData)
			}
		})
	}
}

// A stamp that moved on every re-run would report every bootstrap as a rotation,
// which is the single question it exists to answer.
func TestAReRunKeepsTheOriginalStampAndReplacesTheValue(t *testing.T) {
	c := fake.NewSimpleClientset()
	ctx := context.Background()
	if err := writeOwnedSecret(ctx, c, testInstance, testUID, aSpec(), fixedClock("2026-09-11T10:00:00Z")); err != nil {
		t.Fatalf("first mint: %v", err)
	}

	second := aSpec()
	second.Data = map[string]string{"username": "devicechain", "password": "rotated"}
	if err := writeOwnedSecret(ctx, c, testInstance, testUID, second, fixedClock("2027-01-01T00:00:00Z")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	s := getSecret(t, c, "dc-system", "dc-rdb-app-credentials")
	if got := s.Annotations[annotationMintedAt]; got != "2026-09-11T10:00:00Z" {
		t.Errorf("minted-at moved to %q; a re-run is not a mint", got)
	}
	if s.StringData["password"] != "rotated" {
		t.Errorf("the new value did not land: %v", s.StringData)
	}
}

// A key that has left the spec has to leave the Secret. Merging would keep serving a
// retired credential to anything that never stopped reading it.
func TestAKeyRemovedFromTheSpecLeavesTheSecret(t *testing.T) {
	c := fake.NewSimpleClientset()
	ctx := context.Background()
	first := aSpec()
	first.Data = map[string]string{"username": "devicechain", "password": "s3cret", "legacy": "old"}
	if err := writeOwnedSecret(ctx, c, testInstance, testUID, first, fixedClock("2026-09-11T10:00:00Z")); err != nil {
		t.Fatalf("first mint: %v", err)
	}
	if err := writeOwnedSecret(ctx, c, testInstance, testUID, aSpec(), fixedClock("2026-09-11T10:00:00Z")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, still := getSecret(t, c, "dc-system", "dc-rdb-app-credentials").StringData["legacy"]; still {
		t.Error("a key dropped from the spec is still in the Secret")
	}
}

// 🔴 Without a UID the staleness check degrades to a name comparison, and a name
// comparison is precisely what lets a rebuild adopt a dead generation's credentials.
func TestMintingRefusesWithoutADeclarationUID(t *testing.T) {
	c := fake.NewSimpleClientset()
	err := writeOwnedSecret(context.Background(), c, testInstance, "", aSpec(),
		fixedClock("2026-09-11T10:00:00Z"))
	if err == nil {
		t.Fatal("minted without being able to tell one instance generation from another")
	}
	if len(c.Actions()) == 0 {
		return // refused before touching the API at all, which is better
	}
	for _, a := range c.Actions() {
		if a.GetVerb() == "create" || a.GetVerb() == "update" {
			t.Fatalf("refused, but wrote anyway: %s", a.GetVerb())
		}
	}
}
