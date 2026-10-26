// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"
)

// Traefik's Service on a local cluster must be one a kind cluster can satisfy: a LoadBalancer
// stays <pending> there and hangs the release's --wait. Which controller holds the node's host
// 80/443 is not decided here but by the cluster's front-end plan (planClusterFrontEnd, held in
// frontend_test.go), because only one can and the answer depends on whether ingress-nginx is
// still on the cluster.
func TestALocalClusterGetsTraefikOnANodePort(t *testing.T) {
	vars := infraVars(compactState(false)) // KubeContext kind-dctest: looks local

	if !slices.Contains(vars, "traefik_service_type=NodePort") {
		t.Error("a local cluster must give Traefik a NodePort Service: a LoadBalancer stays <pending> on kind " +
			"and helm --wait hangs until its timeout")
	}
	// infraVars alone says nothing about host ports: a State that never planned the cluster's
	// controllers must not decide who holds them.
	for _, v := range vars {
		if strings.HasPrefix(v, "ingress_use_host_port=") || strings.HasPrefix(v, "traefik_use_host_port=") {
			t.Errorf("%q: the host ports are the front-end plan's to give, not infraVars'", v)
		}
	}
}

func TestACloudClusterLeavesTraefikOnItsLoadBalancerDefault(t *testing.T) {
	st := compactState(false)
	st.KubeContext = "gke_proj_europe-west1_prod"
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, "traefik_service_type=") {
			t.Errorf("%q: a cloud cluster takes the module's LoadBalancer default; overriding it here would "+
				"put a NodePort in front of a production install", v)
		}
	}
}

// The plan's variables reach the cluster root, and only the plan's.
func TestThePlannedFrontEndVariablesReachInfraVars(t *testing.T) {
	st := compactState(false)
	st.ClusterFrontEndVars = []string{"enable_ingress_nginx=false", "traefik_use_host_port=true"}
	vars := infraVars(st)
	for _, want := range st.ClusterFrontEndVars {
		if !slices.Contains(vars, want) {
			t.Errorf("%q was planned but infraVars does not emit it: %v", want, vars)
		}
	}
}
