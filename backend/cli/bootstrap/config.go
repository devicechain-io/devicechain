// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// RestConfig builds a kube REST config lazily from the chosen context. We must
// NOT route through the dc-k8s global client because its init() calls
// GetConfigOrDie() and would crash when no cluster is reachable. An empty
// kubeContext falls back to the kubeconfig's current-context.
func RestConfig(kubeContext string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	return clientConfig.ClientConfig()
}

// KubeContexts returns the list of context names and the current-context from
// the default loading rules. It only parses the kubeconfig file(s) on disk, so
// it never dies or contacts a cluster even if none is reachable.
func KubeContexts() (names []string, current string, err error) {
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, "", err
	}
	return contextNames(raw), raw.CurrentContext, nil
}

// contextNames extracts the sorted-as-defined context names from a parsed config.
func contextNames(raw *clientcmdapi.Config) []string {
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	return names
}
