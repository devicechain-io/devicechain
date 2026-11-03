// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
)

func dep(area, image string, desired, replicas, updated, available int32, gen, observed int64) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: area, Namespace: "dci-x", Generation: gen,
			Labels: map[string]string{instanceLabel: "x", areaLabel: area},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: observed, Replicas: replicas, UpdatedReplicas: updated,
			ReadyReplicas: available, AvailableReplicas: available,
		},
	}
}

func TestComputeStatusReasons(t *testing.T) {
	inst := &corev1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "x", Generation: 3}}
	inst.Spec.ImageVersion = "1.0.0"

	cases := []struct {
		name      string
		ds        []appsv1.Deployment
		available string
		rollout   string
		ready     string
		running   string
	}{
		{"none", nil, corev1beta1.ReasonNoWorkloads, corev1beta1.ReasonNoWorkloads, corev1beta1.ReasonNoWorkloads, ""},
		{"all scaled to zero", []appsv1.Deployment{dep("a", "r/a:1.0.0", 0, 0, 0, 0, 1, 1)},
			corev1beta1.ReasonNoWorkloads, corev1beta1.ReasonVersionUnknown, corev1beta1.ReasonNoWorkloads, ""},
		{"healthy", []appsv1.Deployment{dep("a", "r/a:1.0.0", 2, 2, 2, 2, 1, 1), dep("b", "r/b:1.0.0", 0, 0, 0, 0, 1, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonRolloutComplete, corev1beta1.ReasonReady, "1.0.0"},
		{"v prefix tolerated", []appsv1.Deployment{dep("a", "r/a:v1.0.0", 1, 1, 1, 1, 1, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonRolloutComplete, corev1beta1.ReasonReady, "v1.0.0"},
		{"stale observed generation", []appsv1.Deployment{dep("a", "r/a:1.0.0", 1, 1, 1, 1, 2, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonRolloutInProgress, corev1beta1.ReasonRolloutInProgress, "1.0.0"},
		{"old pods linger", []appsv1.Deployment{dep("a", "r/a:1.0.0", 2, 3, 2, 2, 1, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonRolloutInProgress, corev1beta1.ReasonRolloutInProgress, "1.0.0"},
		{"mixed tags are a mismatch", []appsv1.Deployment{dep("a", "r/a:1.0.0", 1, 1, 1, 1, 1, 1), dep("b", "r/b:1.0.1", 1, 1, 1, 1, 1, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonVersionMismatch, corev1beta1.ReasonVersionMismatch, corev1beta1.VersionMixed},
		{"digest only", []appsv1.Deployment{dep("a", "r/a@sha256:abc", 1, 1, 1, 1, 1, 1)},
			corev1beta1.ReasonAllAreasAvailable, corev1beta1.ReasonVersionUnknown, corev1beta1.ReasonVersionUnknown, ""},
		{"one area down", []appsv1.Deployment{dep("a", "r/a:1.0.0", 1, 1, 1, 1, 1, 1), dep("b", "r/b:1.0.0", 1, 1, 1, 0, 1, 1)},
			corev1beta1.ReasonAreaUnavailable, corev1beta1.ReasonRolloutInProgress, corev1beta1.ReasonAreaUnavailable, "1.0.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := computeStatus(inst, c.ds)
			for typ, want := range map[string]string{
				corev1beta1.ConditionWorkloadsAvailable: c.available,
				corev1beta1.ConditionRolloutComplete:    c.rollout,
				corev1beta1.ConditionReady:              c.ready,
			} {
				got := meta.FindStatusCondition(st.Conditions, typ)
				if got == nil || got.Reason != want {
					t.Errorf("%s = %+v, want reason %s", typ, got, want)
				}
				if got != nil && got.ObservedGeneration != 3 {
					t.Errorf("%s observedGeneration = %d, want 3", typ, got.ObservedGeneration)
				}
			}
			if st.Version.Running != c.running {
				t.Errorf("running = %q, want %q", st.Version.Running, c.running)
			}
			if st.ObservedGeneration != 3 {
				t.Errorf("observedGeneration = %d, want 3", st.ObservedGeneration)
			}
		})
	}
}

func TestImageTag(t *testing.T) {
	for image, want := range map[string]string{
		"reg/dm:1.2.3":            "1.2.3",
		"localhost:5000/dm:1.2.3": "1.2.3",
		"localhost:5000/dm":       "",
		"dm":                      "",
		"reg/dm@sha256:ab":        "",
		"reg/dm:1.2.3@sha256:ab":  "1.2.3",
		"":                        "",
	} {
		if got := imageTag(image); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestInstanceForDeployment(t *testing.T) {
	mk := func(ns string, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Labels: labels}}
	}
	both := map[string]string{instanceLabel: "x", areaLabel: "device-state"}
	for name, c := range map[string]struct {
		d    *appsv1.Deployment
		want string
	}{
		"its own namespace and labels": {mk("dci-x", both), "x"},
		"label in another namespace":   {mk("elsewhere", both), ""},
		"another instance's namespace": {mk("dci-y", both), ""},
		"no area label":                {mk("dci-x", map[string]string{instanceLabel: "x"}), ""},
		"no instance label":            {mk("dci-x", map[string]string{areaLabel: "a"}), ""},
		"the id without the prefix":    {mk("x", both), ""},
	} {
		if got := instanceForDeployment(c.d); got != c.want {
			t.Errorf("%s: instanceForDeployment = %q, want %q", name, got, c.want)
		}
	}
}

func TestMessagesNameWhatIsWrong(t *testing.T) {
	inst := &corev1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "x", Generation: 1}}
	inst.Spec.ImageVersion = "1.0.1"
	msg := func(ds ...appsv1.Deployment) string {
		return meta.FindStatusCondition(computeStatus(inst, ds).Conditions, corev1beta1.ConditionRolloutComplete).Message
	}

	// A crashed pod after a finished rollout is not "not converged on the template".
	if m := msg(dep("a", "r/a:1.0.1", 2, 2, 2, 1, 1, 1)); strings.Contains(m, "not yet converged") || !strings.Contains(m, "a") {
		t.Errorf("availability shortfall message = %q", m)
	}
	// A mismatch names the areas that differ.
	m := msg(dep("a", "r/a:1.0.1", 1, 1, 1, 1, 1, 1), dep("b", "r/b:1.0.0", 1, 1, 1, 1, 1, 1))
	if !strings.Contains(m, `b runs "1.0.0"`) || strings.Contains(m, "a runs") {
		t.Errorf("mismatch message = %q", m)
	}
}
