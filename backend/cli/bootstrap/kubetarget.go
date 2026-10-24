// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// kubePin is the cluster an OpenTofu command is told to act on: an explicit kubeconfig
// file and an explicit context in it.
//
// 🔴 THE TWO HALVES OF dcctl MUST NAME ONE CLUSTER. dcctl's own clients resolve their
// target with client-go's rules (KUBECONFIG, which may list several files, else the home
// file; then --kube-context, else the current context). The OpenTofu roots used to take
// their kubeconfig from a variable defaulting to ~/.kube/config, so a lane-private
// KUBECONFIG pointing at a new cluster, beside a stale home file holding a context of
// the SAME NAME, had the client create the namespace on the new cluster while tofu
// planned against the old endpoint. Had that endpoint been live, the apply would have
// landed on the wrong cluster. A pin is how that cannot happen: both halves are resolved
// here, once, and tofu is never allowed to resolve anything itself.
type kubePin struct {
	// Path and Context are what tofu is handed, as kubeconfig_path and kubeconfig_context.
	Path    string
	Context string
	// identity is the server and CA the pinned file reaches, compared with the client's.
	identity string
	// merged says Path is a file this pin wrote (KUBECONFIG listed several), which the
	// caller removes when the command that needed it is over.
	merged bool
}

// release removes a merged kubeconfig this pin wrote. The files the operator owns are
// never touched.
func (p kubePin) release() {
	if p.merged {
		_ = os.Remove(p.Path)
	}
}

// providerOverrideEnv are the variables the Kubernetes and Helm providers read as the
// default for a setting the configuration leaves unset. The roots set only the
// kubeconfig path and context, so any of these would silently override part of the
// cluster those name -- KUBE_HOST replaces the server of whatever context was chosen.
//
// KUBE_CONFIG_PATH, KUBE_CONFIG_PATHS and KUBE_CTX are deliberately NOT here: both roots
// always set config_path and a non-empty config_context, and those variables only
// default an attribute that is unset, so they cannot override the pin. Refusing them
// would turn operators away for nothing.
var providerOverrideEnv = []string{
	"KUBE_CTX_AUTH_INFO", "KUBE_CTX_CLUSTER", "KUBE_HOST", "KUBE_USER", "KUBE_PASSWORD", "KUBE_TOKEN",
	"KUBE_CLIENT_CERT_DATA", "KUBE_CLIENT_KEY_DATA", "KUBE_CLUSTER_CA_CERT_DATA",
	"KUBE_INSECURE", "KUBE_TLS_SERVER_NAME", "KUBE_PROXY_URL",
}

// kubeTarget is the cluster a tofu runner is allowed to act on, FROZEN when the runner is
// built: a concrete context name and the identity (server and CA) of the cluster the
// client reaches for it.
//
// FROZEN, NOT RE-DERIVED. With no --kube-context the context is the kubeconfig's
// current-context, and another process running `kubectl config use-context` mid-run
// would retarget a pin that re-derived it while dcctl's already-built clients kept the old
// cluster. Every later pin must reach the identity recorded here, so that move (or a
// rebuilt cluster wearing the same context name) is a refusal.
type kubeTarget struct {
	Context  string
	Identity string
}

// freezeKubeTarget resolves kubeContext ("" = the current context) to a concrete name
// and records the identity of the cluster dcctl's own clients reach for it.
func freezeKubeTarget(kubeContext string) (kubeTarget, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	raw, err := rules.Load()
	if err != nil {
		return kubeTarget{}, fmt.Errorf("reading the kubeconfig: %w", err)
	}
	name := kubeContext
	if name == "" {
		name = raw.CurrentContext
	}
	if name == "" {
		return kubeTarget{}, fmt.Errorf("the kubeconfig names no current context and none was given with " +
			"--kube-context, so there is no cluster to apply to")
	}
	if _, ok := raw.Contexts[name]; !ok {
		return kubeTarget{}, fmt.Errorf("kube-context %q is not in the kubeconfig dcctl reads (%s)",
			name, strings.Join(rules.GetLoadingPrecedence(), string(os.PathListSeparator)))
	}
	cfg, err := RestConfig(name)
	if err != nil {
		return kubeTarget{}, fmt.Errorf("resolving the cluster dcctl's client reaches: %w", err)
	}
	id, err := restIdentity(cfg)
	if err != nil {
		return kubeTarget{}, err
	}
	return kubeTarget{Context: name, Identity: id}, nil
}

// restIdentity is the server URL plus a digest of the CA: what says which cluster a
// config reaches without contacting it.
func restIdentity(cfg *rest.Config) (string, error) {
	ca := cfg.CAData
	if len(ca) == 0 && cfg.CAFile != "" {
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return "", err
		}
		ca = b
	}
	sum := sha256.Sum256(ca)
	return cfg.Host + " ca-sha256:" + hex.EncodeToString(sum[:]), nil
}

// resolveKubePin resolves the frozen target's context with client-go's rules and
// verifies the result still names the cluster dcctl's clients were built against. mergedDir is where a
// merged kubeconfig is written when KUBECONFIG lists several files; it must be owner-only.
//
// 🔴 IT REFUSES RATHER THAN FALLING BACK. An unresolvable context, no kubeconfig at all,
// a provider override in the environment, or a pinned file that reaches a different
// cluster than the client are all errors, never a default.
func resolveKubePin(target kubeTarget, mergedDir string) (kubePin, error) {
	var set []string
	for _, name := range providerOverrideEnv {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	if len(set) > 0 {
		return kubePin{}, fmt.Errorf("refusing to run OpenTofu with %s set: the Kubernetes and Helm "+
			"providers read it as a default for the cluster connection, so it can silently "+
			"override the cluster dcctl chose. Unset it and re-run", strings.Join(set, ", "))
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	raw, err := rules.Load()
	if err != nil {
		return kubePin{}, fmt.Errorf("reading the kubeconfig: %w", err)
	}
	ctxName := target.Context
	if _, ok := raw.Contexts[ctxName]; !ok {
		return kubePin{}, fmt.Errorf("kube-context %q is not in the kubeconfig dcctl reads (%s)",
			ctxName, strings.Join(rules.GetLoadingPrecedence(), string(os.PathListSeparator)))
	}

	var files []string
	seen := map[string]bool{}
	for _, f := range rules.GetLoadingPrecedence() {
		abs, err := filepath.Abs(f)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		if st, err := os.Stat(abs); err == nil && !st.IsDir() {
			files = append(files, abs)
		}
	}

	pin := kubePin{Context: ctxName}
	switch len(files) {
	case 0:
		return kubePin{}, fmt.Errorf("no kubeconfig file exists among %s",
			strings.Join(rules.GetLoadingPrecedence(), string(os.PathListSeparator)))
	case 1:
		// The operator's own file, passed as it is: nothing is copied.
		pin.Path = files[0]
	default:
		// KUBECONFIG lists several files and client-go merges them, so the context may
		// be defined in any of them. Neither "the first" nor "the home file" is the
		// answer; the merge, cut down to the one context tofu needs, is.
		merged := raw.DeepCopy()
		merged.CurrentContext = ctxName
		if err := clientcmdapi.MinifyConfig(merged); err != nil {
			return kubePin{}, fmt.Errorf("merging the kubeconfig files for context %q: %w", ctxName, err)
		}
		if err := os.MkdirAll(mergedDir, 0o700); err != nil {
			return kubePin{}, err
		}
		pin.Path = filepath.Join(mergedDir, "kubeconfig.merged")
		if err := os.WriteFile(pin.Path, nil, 0o600); err != nil {
			return kubePin{}, err
		}
		if err := clientcmd.WriteToFile(*merged, pin.Path); err != nil {
			return kubePin{}, fmt.Errorf("writing the merged kubeconfig: %w", err)
		}
		pin.merged = true
	}

	// The fail-closed check, on the artifact tofu will be handed: load exactly that file
	// and context and compare with the cluster the client reaches.
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: pin.Path},
		&clientcmd.ConfigOverrides{CurrentContext: pin.Context}).ClientConfig()
	if err != nil {
		pin.release()
		return kubePin{}, fmt.Errorf("loading kubeconfig %s context %q: %w", pin.Path, pin.Context, err)
	}
	if pin.identity, err = restIdentity(cfg); err != nil {
		pin.release()
		return kubePin{}, err
	}
	if want := target.Identity; want != pin.identity {
		pin.release()
		return kubePin{}, fmt.Errorf("refusing to run OpenTofu: it would target %s (kubeconfig %s, "+
			"context %q) but dcctl's client is talking to %s. Nothing has been changed",
			pin.identity, pin.Path, pin.Context, want)
	}
	return pin, nil
}
