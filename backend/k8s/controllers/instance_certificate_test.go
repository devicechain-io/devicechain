// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
)

var certNow = time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)

func instanceWithCert(leaf, ca string) *corev1beta1.Instance {
	i := &corev1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "x", Annotations: map[string]string{}}}
	if leaf != "" {
		i.Annotations[corev1beta1.AnnotationBrokerCertificateNotAfter] = leaf
	}
	if ca != "" {
		i.Annotations[corev1beta1.AnnotationBrokerCANotAfter] = ca
	}
	return i
}

func at(d time.Duration) string { return certNow.Add(d).Format(time.RFC3339) }

func TestCertificateVerdict(t *testing.T) {
	const day = 24 * time.Hour
	decade := at(3000 * day)
	cases := []struct {
		name   string
		inst   *corev1beta1.Instance
		status metav1.ConditionStatus
		reason string
	}{
		{"nothing published is Unknown, never valid", instanceWithCert("", ""), metav1.ConditionUnknown, corev1beta1.ReasonCertificateNotPublished},
		{"a year left is valid", instanceWithCert(at(365*day), decade), metav1.ConditionTrue, corev1beta1.ReasonCertificateValid},
		{"31 days left is still valid", instanceWithCert(at(31*day), decade), metav1.ConditionTrue, corev1beta1.ReasonCertificateValid},
		{"exactly 30 days left is expiring soon", instanceWithCert(at(30*day), decade), metav1.ConditionTrue, corev1beta1.ReasonCertificateExpiringSoon},
		{"a day left is expiring soon, not expired", instanceWithCert(at(day), decade), metav1.ConditionTrue, corev1beta1.ReasonCertificateExpiringSoon},
		{"the moment of expiry is expired", instanceWithCert(at(0), decade), metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
		{"long past is expired", instanceWithCert(at(-90*day), decade), metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
		{"the authority expiring soon counts though the leaf is fine", instanceWithCert(at(300*day), at(10*day)), metav1.ConditionTrue, corev1beta1.ReasonCertificateExpiringSoon},
		{"an expired authority beats a fresh leaf", instanceWithCert(at(300*day), at(-day)), metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
		{"expired beats expiring soon", instanceWithCert(at(5*day), at(-day)), metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
		{"a leaf alone is judged", instanceWithCert(at(5*day), ""), metav1.ConditionTrue, corev1beta1.ReasonCertificateExpiringSoon},
		{"garbage is Unknown, not valid", instanceWithCert("next tuesday", decade), metav1.ConditionUnknown, corev1beta1.ReasonCertificateUnreadable},
		{"a non-UTC offset is read as the instant it names",
			instanceWithCert(certNow.Add(-time.Hour).In(time.FixedZone("x", 5*3600)).Format(time.RFC3339), ""),
			metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, _ := certificateVerdict(c.inst, certNow)
			if v.Status != c.status || v.Reason != c.reason {
				t.Fatalf("got %s/%s (%s), want %s/%s", v.Status, v.Reason, v.Message, c.status, c.reason)
			}
		})
	}
}

// The time-driven flips are the point of the condition: nothing in the cluster changes
// when a certificate crosses a boundary, so the verdict has to say when to look again.
func TestCertificateVerdictSchedulesItsOwnNextLook(t *testing.T) {
	const day = 24 * time.Hour
	if _, d := certificateVerdict(instanceWithCert("", ""), certNow); d != 0 {
		t.Errorf("nothing published must not schedule anything, got %s", d)
	}
	// Ten minutes before the warning boundary: nearer than the backstop.
	_, d := certificateVerdict(instanceWithCert(at(30*day+10*time.Minute), ""), certNow)
	if want := 10*time.Minute + time.Second; d != want {
		t.Errorf("RequeueAfter = %s, want %s (just past the warning boundary)", d, want)
	}
	// A year away the boundary is far off; the hourly backstop applies.
	if _, d := certificateVerdict(instanceWithCert(at(365*day), ""), certNow); d != certRecheck {
		t.Errorf("RequeueAfter = %s, want the %s backstop", d, certRecheck)
	}
	// Inside the window, the next change is expiry itself.
	_, d = certificateVerdict(instanceWithCert(at(90*time.Second), ""), certNow)
	if want := 91 * time.Second; d != want {
		t.Errorf("RequeueAfter = %s, want %s (just past expiry)", d, want)
	}
}

// Ready: an expired certificate takes it False; expiring soon and unknown must not.
func TestCertificateGatesReadyOnlyWhenExpired(t *testing.T) {
	const day = 24 * time.Hour
	ds := []appsv1.Deployment{dep("a", "r/a:1.0.0", 1, 1, 1, 1, 1, 1)}
	for _, c := range []struct {
		name   string
		inst   *corev1beta1.Instance
		status metav1.ConditionStatus
		reason string
	}{
		{"valid", instanceWithCert(at(365*day), ""), metav1.ConditionTrue, corev1beta1.ReasonReady},
		{"expiring soon does not gate", instanceWithCert(at(3*day), ""), metav1.ConditionTrue, corev1beta1.ReasonReady},
		{"unknown does not gate", instanceWithCert("", ""), metav1.ConditionTrue, corev1beta1.ReasonReady},
		{"unreadable does not gate", instanceWithCert("junk", ""), metav1.ConditionTrue, corev1beta1.ReasonReady},
		{"expired gates", instanceWithCert(at(-day), ""), metav1.ConditionFalse, corev1beta1.ReasonCertificateExpired},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.inst.Spec.ImageVersion = "1.0.0"
			st := computeStatus(c.inst, ds, certNow)
			ready := findCondition(st.Conditions, corev1beta1.ConditionReady)
			if ready == nil || ready.Status != c.status || ready.Reason != c.reason {
				t.Fatalf("Ready = %+v, want %s/%s", ready, c.status, c.reason)
			}
		})
	}
}

// Workload trouble outranks the certificate in Ready's reason: it is the thing to fix first.
func TestWorkloadTroubleOutranksExpiredCertificateInReady(t *testing.T) {
	inst := instanceWithCert(at(-time.Hour), "")
	inst.Spec.ImageVersion = "1.0.0"
	st := computeStatus(inst, []appsv1.Deployment{dep("a", "r/a:1.0.0", 1, 1, 1, 0, 1, 1)}, certNow)
	if r := findCondition(st.Conditions, corev1beta1.ConditionReady); r == nil || r.Reason != corev1beta1.ReasonAreaUnavailable {
		t.Fatalf("Ready = %+v, want AreaUnavailable", r)
	}
	if c := findCondition(st.Conditions, corev1beta1.ConditionBrokerCertificateValid); c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("BrokerCertificateValid = %+v, want False regardless", c)
	}
}

func findCondition(cs []metav1.Condition, typ string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == typ {
			return &cs[i]
		}
	}
	return nil
}
