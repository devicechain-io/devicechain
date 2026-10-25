// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"slices"
	"strings"
	"testing"
)

// Traefik is installed next to ingress-nginx in this release, and on a local cluster the
// two cannot share the node's host 80/443: the cluster root refuses both asking for them,
// and a second pod would stay Pending until the apply timed out. So dcctl gives ingress-nginx
// the host ports (as it always has) and gives Traefik only what a kind cluster can satisfy,
// a NodePort Service. A LoadBalancer there stays <pending> and hangs the release's --wait.
func TestALocalClusterGetsTraefikOnANodePortAndNeverOnHostPorts(t *testing.T) {
	vars := infraVars(compactState(false)) // KubeContext kind-dctest: looks local

	if !slices.Contains(vars, "traefik_service_type=NodePort") {
		t.Error("a local cluster must give Traefik a NodePort Service: a LoadBalancer stays <pending> on kind " +
			"and helm --wait hangs until its timeout")
	}
	if !slices.Contains(vars, "ingress_use_host_port=true") {
		t.Error("a local cluster keeps ingress-nginx on the host ports in this release")
	}
	for _, v := range vars {
		if strings.HasPrefix(v, "traefik_use_host_port=") && v != "traefik_use_host_port=false" {
			t.Errorf("%q: Traefik must not ask for the host ports while ingress-nginx holds them", v)
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
