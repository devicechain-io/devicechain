// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigUnreadableError is what dcctl reports when the kubeconfig cannot be
// parsed. It names the files and nothing else.
//
// 🔴 CLIENT-GO'S OWN REASON IS NEVER REPEATED, because it can quote the credentials
// the file holds. With two users[] entries of the same name, its conversion error
// prints every user record, tokens, passwords and client keys included. That text
// would reach a terminal, a CI log, a preflight observation and the --output json
// stream alike, so it is dropped where it is made rather than redacted wherever it
// lands. Every kubeconfig load dcctl makes goes through loadKubeconfig.
type KubeconfigUnreadableError struct {
	// Paths are the files client-go reads, in its order of precedence.
	Paths []string
}

func (e *KubeconfigUnreadableError) Error() string {
	return fmt.Sprintf("the kubeconfig (%s) could not be read. dcctl does not repeat the parser's reason, "+
		"because it can quote the credentials the file holds; check the file for malformed YAML or for "+
		"a cluster, context or user name that is defined twice",
		strings.Join(e.Paths, string(os.PathListSeparator)))
}

// loadKubeconfig is rules.Load with a failure reported as KubeconfigUnreadableError.
func loadKubeconfig(rules *clientcmd.ClientConfigLoadingRules) (*clientcmdapi.Config, error) {
	raw, err := rules.Load()
	if err != nil {
		return nil, kubeconfigUnreadable(rules)
	}
	return raw, nil
}

func kubeconfigUnreadable(rules *clientcmd.ClientConfigLoadingRules) error {
	if rules.ExplicitPath != "" {
		return &KubeconfigUnreadableError{Paths: []string{rules.ExplicitPath}}
	}
	return &KubeconfigUnreadableError{Paths: rules.GetLoadingPrecedence()}
}

// clientConfig is ClientConfig of a non-interactive deferred-loading config, with a
// failure to READ the kubeconfig reported as KubeconfigUnreadableError. Its other
// errors (no such context, an incomplete entry) name entries, never their values,
// and are returned as they are.
func clientConfig(rules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		// Which kind of failure is it? Reading the files again answers that without
		// parsing client-go's text: if they cannot be read, the reason is dropped.
		if _, lerr := loadKubeconfig(rules); lerr != nil {
			return nil, lerr
		}
		return nil, err
	}
	return cfg, nil
}

// IsKubeconfigUnreadable reports whether err is a kubeconfig that could not be read.
func IsKubeconfigUnreadable(err error) bool {
	var k *KubeconfigUnreadableError
	return errors.As(err, &k)
}

// RestConfig builds a kube REST config lazily from the chosen context. We must
// NOT route through the dc-k8s global client because its init() calls
// GetConfigOrDie() and would crash when no cluster is reachable. An empty
// kubeContext falls back to the kubeconfig's current-context.
func RestConfig(kubeContext string) (*rest.Config, error) {
	return clientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext})
}

// KubeContexts returns the list of context names and the current-context from
// the default loading rules. It only parses the kubeconfig file(s) on disk, so
// it never dies or contacts a cluster even if none is reachable.
func KubeContexts() (names []string, current string, err error) {
	raw, err := loadKubeconfig(clientcmd.NewDefaultClientConfigLoadingRules())
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
