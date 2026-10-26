// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "context"

// Every pipeline test drives bootstrap or upgrade without a cluster, and both now settle the
// instance's front end from one read of it (readFrontEndFacts). The default a test sees is a
// cluster with Traefik alone: it asks no kube config for anything, and it is the shape a new
// install gets. A test that is about the front end replaces it and restores it.
func init() {
	readFrontEndFacts = func(context.Context, string, string) (frontEndFacts, error) {
		return frontEndFacts{clusterFrontEnd: clusterFrontEnd{GatewayClass: true}}, nil
	}
}
