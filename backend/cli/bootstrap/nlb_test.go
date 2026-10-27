// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var nlbAnnotation = map[string]string{awsLoadBalancerTypeAnnotation: "nlb"}

// traefikSvc and nginxSvc are the Services THIS root installs: its namespace, release labels, a
// LoadBalancer.
func traefikSvc(annotations map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: traefikNamespace, Annotations: annotations,
		Labels: map[string]string{"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-" + traefikNamespace}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
}

func nginxSvc(annotations map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx-controller", Namespace: ingressNginxNamespace, Annotations: annotations,
		Labels: map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/instance": "ingress-nginx",
			"app.kubernetes.io/component": "controller"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
}

// The caller: the install's cluster variables, for each --aws-nlb value, over what the cluster's
// Services carry now.
func TestTheNLBSettingFollowsTheFlagAndTheServicesItWasAppliedTo(t *testing.T) {
	cls := nginxClass()
	run := func(t *testing.T, mode string, objs ...*corev1.Service) (vars []string, out string) {
		t.Helper()
		st := installState(ClusterBinding{KubeContext: "gke_p_z_c"}, "local", InstallOptions{AWSNLB: mode})
		if st.AWSNLB != mode {
			t.Fatalf("installState dropped AWSNLB: %q", st.AWSNLB)
		}
		typed := fakeTypedWith(&cls)
		for _, s := range objs {
			if _, err := typed.CoreV1().Services(s.Namespace).Create(t.Context(), s, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		out = captureStdout(t, func() {
			var err error
			if vars, err = installClusterVars(t.Context(), st, typed); err != nil {
				t.Fatal(err)
			}
		})
		return vars, out
	}
	has := func(v []string, s string) bool { return slices.Contains(v, s) }
	const tr, ng, warn = "traefik_aws_nlb=true", "ingress_nginx_aws_nlb=true", "AWS REPLACES the load balancer"

	t.Run("absent, nothing on the cluster: off, no warning", func(t *testing.T) {
		v, out := run(t, "")
		if has(v, tr) || has(v, ng) || strings.Contains(out, warn) {
			t.Errorf("vars %v out %s", v, out)
		}
	})
	t.Run("absent KEEPS what each Service has", func(t *testing.T) {
		v, out := run(t, "", traefikSvc(nlbAnnotation), nginxSvc(nil))
		if !has(v, tr) || has(v, ng) || strings.Contains(out, warn) {
			t.Errorf("vars %v out %s", v, out)
		}
		v, _ = run(t, "", traefikSvc(nil), nginxSvc(nlbAnnotation))
		if has(v, tr) || !has(v, ng) {
			t.Errorf("vars %v", v)
		}
	})
	t.Run("traefik sets only Traefik's, and nginx keeps what it has", func(t *testing.T) {
		v, out := run(t, "traefik", traefikSvc(nil), nginxSvc(nil))
		if !has(v, tr) || has(v, ng) || !strings.Contains(out, warn) || !strings.Contains(out, "traefik Service") {
			t.Errorf("vars %v out %s", v, out)
		}
		if v, _ = run(t, "traefik", traefikSvc(nil), nginxSvc(nlbAnnotation)); !has(v, tr) || !has(v, ng) {
			t.Errorf("traefik turned an annotated nginx off: %v", v)
		}
	})
	t.Run("nginx sets only nginx's", func(t *testing.T) {
		v, out := run(t, "nginx", traefikSvc(nil), nginxSvc(nil))
		if has(v, tr) || !has(v, ng) || !strings.Contains(out, "nginx Service") {
			t.Errorf("vars %v out %s", v, out)
		}
	})
	t.Run("all sets both", func(t *testing.T) {
		v, _ := run(t, "all")
		if !has(v, tr) || !has(v, ng) {
			t.Errorf("vars %v", v)
		}
	})
	t.Run("none turns both off, and says it replaces an annotated Service's balancer", func(t *testing.T) {
		v, out := run(t, "none", traefikSvc(nlbAnnotation), nginxSvc(nlbAnnotation))
		if has(v, tr) || has(v, ng) || strings.Count(out, warn) != 2 {
			t.Errorf("vars %v out %s", v, out)
		}
	})
	t.Run("a value that changes nothing warns of nothing; a first install has no Service to replace", func(t *testing.T) {
		if _, out := run(t, "traefik", traefikSvc(nlbAnnotation)); strings.Contains(out, warn) {
			t.Errorf("re-asserting the annotation warned: %s", out)
		}
		if _, out := run(t, "all"); strings.Contains(out, warn) {
			t.Errorf("creating the Services warned of replacing them: %s", out)
		}
	})
	t.Run("the read is scoped to THIS root's Services", func(t *testing.T) {
		foreign := traefikSvc(nlbAnnotation)
		foreign.Namespace = "somebody-elses"
		otherRelease := traefikSvc(nlbAnnotation)
		otherRelease.Labels["app.kubernetes.io/instance"] = "traefik-other"
		notLB := traefikSvc(nlbAnnotation)
		notLB.Spec.Type = corev1.ServiceTypeNodePort
		for name, s := range map[string]*corev1.Service{"another namespace": foreign, "another release": otherRelease, "a NodePort": notLB} {
			if v, out := run(t, "", s); has(v, tr) || strings.Contains(out, warn) {
				t.Errorf("%s decided the setting: %v %s", name, v, out)
			}
		}
		if v, _ := run(t, "none", foreign); has(v, tr) {
			t.Errorf("none left it on: %v", v)
		}
	})
	t.Run("a value it does not know is refused", func(t *testing.T) {
		if err := ValidateAWSNLB("both"); err == nil {
			t.Error("accepted 'both'")
		}
		for _, ok := range []string{"", "traefik", "nginx", "all", "none"} {
			if err := ValidateAWSNLB(ok); err != nil {
				t.Errorf("refused %q: %v", ok, err)
			}
		}
	})
}
