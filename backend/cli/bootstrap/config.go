// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigError is what dcctl reports when a kubeconfig cannot be read, or cannot
// be turned into a client for a context. It says which kind of failure it was and
// names the files and the context, and nothing else.
//
// 🔴 CLIENT-GO'S OWN REASON IS NEVER REPEATED, because it can quote what the file
// holds. With two users[] entries of the same name its conversion error prints every
// user record, tokens, passwords and client keys included; its validation quotes a
// proxy-url whole, password and all; a server URL it cannot parse is quoted with its
// userinfo. That text would reach a terminal, a CI log, a preflight observation and
// the --output json stream alike, so it is dropped where it is made rather than
// redacted wherever it lands. Every kubeconfig load dcctl makes goes through
// loadKubeconfig or clientConfig, and both return only this.
type KubeconfigError struct {
	// Paths are the files client-go reads, in its order of precedence.
	Paths []string
	// Context is the context the client was being built for: a name, never a value.
	Context string
	// Problem is one of the fixed sentences below.
	Problem string
}

// The fixed sentences a KubeconfigError carries.
const (
	kubeconfigUnreadable = "could not be read: check it for malformed YAML or for a cluster, context or " +
		"user name that is defined twice"
	kubeconfigEmpty         = "has no usable configuration: no context is set and none was given"
	kubeconfigNoContext     = "does not define the context"
	kubeconfigInvalidEntry  = "has an invalid entry for the context: check its cluster (server and proxy URLs, certificate authority) and its user (credentials, client certificate, exec plugin)"
	kubeconfigUnusableEntry = "could not be turned into a client for the context"
)

func (e *KubeconfigError) Error() string {
	ctx := ""
	if e.Context != "" {
		ctx = fmt.Sprintf(" (context %q)", e.Context)
	}
	return fmt.Sprintf("the kubeconfig (%s)%s %s. dcctl does not repeat the Kubernetes client's reason, "+
		"because it can quote the credentials and URLs the file holds",
		strings.Join(e.Paths, string(os.PathListSeparator)), ctx, e.Problem)
}

// IsKubeconfigError reports whether err is a kubeconfig that could not be read or used.
func IsKubeconfigError(err error) bool {
	var k *KubeconfigError
	return errors.As(err, &k)
}

func kubeconfigPaths(rules *clientcmd.ClientConfigLoadingRules) []string {
	if rules.ExplicitPath != "" {
		return []string{rules.ExplicitPath}
	}
	return rules.GetLoadingPrecedence()
}

// loadKubeconfig is rules.Load with a failure reported as a KubeconfigError.
func loadKubeconfig(rules *clientcmd.ClientConfigLoadingRules) (*clientcmdapi.Config, error) {
	raw, err := rules.Load()
	if err != nil {
		return nil, &KubeconfigError{Paths: kubeconfigPaths(rules), Problem: kubeconfigUnreadable}
	}
	return raw, nil
}

// clientConfig is ClientConfig of a non-interactive deferred-loading config, with
// every failure reported as a KubeconfigError: which kind it was, decided from the
// error's type rather than its text, and the context's name.
func clientConfig(rules *clientcmd.ClientConfigLoadingRules, overrides *clientcmd.ConfigOverrides) (*rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err == nil {
		// The server address is parsed only when a client is built from the config,
		// and that error quotes it, userinfo and all. Parse it here, where the
		// failure can be told in dcctl's words.
		if _, _, uerr := rest.DefaultServerUrlFor(cfg); uerr == nil {
			return cfg, nil
		}
		err = clientcmd.ErrEmptyCluster // an invalid entry, below
	}
	raw, lerr := loadKubeconfig(rules)
	if lerr != nil {
		return nil, lerr
	}
	kerr := &KubeconfigError{Paths: kubeconfigPaths(rules), Context: overrides.CurrentContext}
	if kerr.Context == "" {
		kerr.Context = raw.CurrentContext
	}
	if _, ok := raw.Contexts[kerr.Context]; kerr.Context != "" && !ok {
		// client-go's own sentence, which names the context and nothing else: what
		// dcctl has always printed for it.
		return nil, fmt.Errorf("context %q does not exist", kerr.Context)
	}
	switch {
	case clientcmd.IsEmptyConfig(err):
		kerr.Problem = kubeconfigEmpty
	case clientcmd.IsContextNotFound(err):
		kerr.Problem = kubeconfigNoContext
	case clientcmd.IsConfigurationInvalid(err), errors.Is(err, clientcmd.ErrEmptyCluster):
		kerr.Problem = kubeconfigInvalidEntry
	default:
		kerr.Problem = kubeconfigUnusableEntry
	}
	return nil, kerr
}

// urlPassword is the password in a URL's userinfo, or "".
func urlPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	p, _ := u.User.Password()
	return p
}

// withoutUserinfo is a server address with any userinfo removed, for display.
func withoutUserinfo(host string) string {
	if u, err := url.Parse(host); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return host
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
