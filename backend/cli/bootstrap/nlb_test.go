// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func controllerService(app, ns string, typ corev1.ServiceType, annotations map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: app, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/name": app}, Annotations: annotations},
		Spec:       corev1.ServiceSpec{Type: typ},
	}
}

// The caller: the install's cluster variables, with and without --aws-nlb, on a cluster that does
// or does not already carry the annotation. The carried annotation is what a re-run without the
// flag must not take off (that replaces the load balancer).
func TestTheNLBSettingFollowsTheFlagAndTheClusterItWasAppliedTo(t *testing.T) {
	cls := nginxClass()
	nlb := map[string]string{awsLoadBalancerTypeAnnotation: "nlb"}
	vars := func(asked bool, objs ...*corev1.Service) []string {
		t.Helper()
		st := installState(ClusterBinding{KubeContext: "gke_p_z_c"}, "local", InstallOptions{AWSNLB: asked})
		if st.AWSNLB != asked {
			t.Fatalf("installState dropped AWSNLB: %v", st.AWSNLB)
		}
		typed := fakeTypedWith(&cls)
		for _, s := range objs {
			if _, err := typed.CoreV1().Services(s.Namespace).Create(t.Context(), s, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		v, err := installClusterVars(t.Context(), st, typed)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	has := func(v []string, s string) bool { return slices.Contains(v, s) }

	if v := vars(false); has(v, "traefik_aws_nlb=true") || has(v, "ingress_nginx_aws_nlb=true") {
		t.Errorf("no flag, nothing on the cluster, yet an NLB was asked for: %v", v)
	}
	if v := vars(true); !has(v, "traefik_aws_nlb=true") || !has(v, "ingress_nginx_aws_nlb=true") {
		t.Errorf("--aws-nlb did not reach both controllers: %v", v)
	}
	// A re-run without the flag keeps what is there, per controller.
	v := vars(false, controllerService("traefik", "traefik", corev1.ServiceTypeLoadBalancer, nlb))
	if !has(v, "traefik_aws_nlb=true") || has(v, "ingress_nginx_aws_nlb=true") {
		t.Errorf("a Traefik Service with the annotation was not kept (or leaked to nginx): %v", v)
	}
	// Negative controls: the annotation on a Service that is not a LoadBalancer, or on one that is
	// not a controller's, or a LoadBalancer without it, is not a reason.
	for name, s := range map[string]*corev1.Service{
		"a NodePort":    controllerService("traefik", "traefik", corev1.ServiceTypeNodePort, nlb),
		"another app":   controllerService("grafana", "monitoring", corev1.ServiceTypeLoadBalancer, nlb),
		"no annotation": controllerService("traefik", "traefik", corev1.ServiceTypeLoadBalancer, nil),
		"another type":  controllerService("traefik", "traefik", corev1.ServiceTypeLoadBalancer, map[string]string{awsLoadBalancerTypeAnnotation: "external"}),
	} {
		if v := vars(false, s); has(v, "traefik_aws_nlb=true") {
			t.Errorf("%s: kept an NLB nothing asked for: %v", name, v)
		}
	}
}
