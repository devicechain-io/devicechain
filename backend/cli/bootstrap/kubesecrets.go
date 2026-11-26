// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io"
	"os"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"sigs.k8s.io/yaml"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// credential is one secret value and how strictly it is registered.
type credential struct {
	value string
	// textOnly registers it for redaction from text without refusing an event
	// code that contains it. Exec-plugin environments and auth-provider settings
	// hold credentials beside ordinary words (a profile named "default"), and a
	// strictly registered word would refuse every code that spells it.
	textOnly bool
}

// kubeconfigSecrets returns the credential values in the kubeconfig files client-go
// reads: each user's token, password and client key, its auth provider's settings,
// its exec plugin's environment, and the password in any cluster's server or proxy
// URL.
//
// It parses each file on its own, as a plain list, rather than through client-go's
// loader. A file client-go refuses — two users of the same name is the case that
// matters — still has its credentials found, which is exactly when they are most
// likely to be printed. A file that cannot be read or parsed at all contributes
// nothing; this is the second line, not the first.
func kubeconfigSecrets(rules *clientcmd.ClientConfigLoadingRules) []credential {
	var out []credential
	add := func(v string, textOnly bool) {
		if v != "" {
			out = append(out, credential{value: v, textOnly: textOnly})
		}
	}
	for _, path := range rules.GetLoadingPrecedence() {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg clientcmdv1.Config
		if yaml.Unmarshal(b, &cfg) != nil {
			continue
		}
		for _, c := range cfg.Clusters {
			add(urlPassword(c.Cluster.Server), false)
			add(urlPassword(c.Cluster.ProxyURL), false)
		}
		for _, u := range cfg.AuthInfos {
			a := u.AuthInfo
			add(a.Token, false)
			add(a.Password, false)
			add(string(a.ClientKeyData), false)
			if a.AuthProvider != nil {
				for _, v := range a.AuthProvider.Config {
					add(v, true)
				}
			}
			if a.Exec != nil {
				for _, env := range a.Exec.Env {
					add(env.Value, true)
				}
			}
		}
	}
	return out
}

// registerCredentials registers each value with the hub, and returns how many it
// could not cover.
//
// 🔴 A VALUE THE HUB REFUSES IS NOT DROPPED SILENTLY. One too short for a strict
// registration (under installflow.MinSecretBytes) is registered redact-only, which
// takes values down to installflow.MinRedactionBytes; only a value shorter than that,
// which cannot be removed from text without removing ordinary characters with it, is
// left uncovered, and counted, so the run can say so.
func registerCredentials(events installflow.Reporter, creds []credential) (uncovered int) {
	if events == nil {
		return 0
	}
	for _, c := range creds {
		if !c.textOnly && events.AddSecret(c.value) == nil {
			continue
		}
		if events.AddRedaction(c.value) != nil {
			uncovered++
		}
	}
	return uncovered
}

// registerKubeconfigSecrets tells the hub the kubeconfig's credentials, so a tool
// that echoes one (kubectl, helm or an OpenTofu provider reporting a kubeconfig it
// could not use) has it redacted. Like registerSecrets, it is defence in depth: no
// producer puts a kubeconfig value in an event, and a kubeconfig that cannot be
// used is reported without client-go's reason. A value it cannot cover is warned
// about on w.
func registerKubeconfigSecrets(events installflow.Reporter, w io.Writer) {
	warnUncovered(w, "kubeconfig credential",
		registerCredentials(events, kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules())))
}

// warnUncovered says that n values of what could not be redacted.
func warnUncovered(w io.Writer, what string, n int) {
	if n == 0 || w == nil {
		return
	}
	fmt.Fprintf(w, "warning: %d %s value(s) are shorter than %d bytes and cannot be redacted from this "+
		"install's output\n", n, what, installflow.MinRedactionBytes)
}

// refreshKubeconfigSecrets registers the kubeconfig's credentials again: the run
// itself adds some, as creating a kind cluster writes the cluster's client key.
func (r *installRun) refreshKubeconfigSecrets() {
	if r == nil {
		return
	}
	// Nil: anything it cannot cover was warned about when the run started.
	registerKubeconfigSecrets(r.events, nil)
}
