// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "k8s.io/client-go/kubernetes"

// Every test that runs the pipeline's credential step has no cluster to read the broker's
// authority from, and no instance identity to own one with. A nil client is the dry run's answer
// (mint a pair, read nothing), which is what these tests were written against. The tests of the
// reuse itself call settleNATSTLS with a fake clientset instead.
func init() {
	natsAuthorityClient = func(string) (kubernetes.Interface, error) { return nil, nil }
}
