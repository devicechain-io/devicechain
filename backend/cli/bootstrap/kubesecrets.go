// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"sigs.k8s.io/yaml"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// minIncidentalSecret is the shortest exec-plugin env value or auth-provider
// setting registered for redaction. Those maps hold credentials (an id-token, a
// client secret) beside ordinary words (a profile named "default", a region), and
// a registered word is redacted from every log line and refused in every code the
// run emits. Credential-shaped values are long; ordinary settings are short.
const minIncidentalSecret = 16

// kubeconfigSecrets returns the credential values in the kubeconfig files client-go
// reads: each user's token, password and client key, its auth provider's settings and
// its exec plugin's environment.
//
// It parses each file on its own, as a plain list, rather than through client-go's
// loader. A file client-go refuses — two users of the same name is the case that
// matters — still has its credentials found, which is exactly when they are most
// likely to be printed. A file that cannot be read or parsed at all contributes
// nothing; this is the second line, not the first.
func kubeconfigSecrets(rules *clientcmd.ClientConfigLoadingRules) []string {
	var out []string
	add := func(v string, min int) {
		if len(v) >= min {
			out = append(out, v)
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
		for _, u := range cfg.AuthInfos {
			a := u.AuthInfo
			add(a.Token, 1)
			add(a.Password, 1)
			add(string(a.ClientKeyData), 1)
			if a.AuthProvider != nil {
				for _, v := range a.AuthProvider.Config {
					add(v, minIncidentalSecret)
				}
			}
			if a.Exec != nil {
				for _, env := range a.Exec.Env {
					add(env.Value, minIncidentalSecret)
				}
			}
		}
	}
	return out
}

// registerKubeconfigSecrets tells the hub the kubeconfig's credentials, so a tool
// that echoes one (kubectl, helm or an OpenTofu provider reporting a kubeconfig it
// could not use) has it redacted from the stream. Like registerSecrets, it is
// defence in depth: no producer puts a kubeconfig value in an event, and a
// kubeconfig that cannot be read is reported without client-go's reason.
func registerKubeconfigSecrets(events installflow.Reporter) {
	if events == nil {
		return
	}
	for _, v := range kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules()) {
		_ = events.AddSecret(v)
	}
}

// refreshKubeconfigSecrets registers the kubeconfig's credentials again: the run
// itself adds some, as creating a kind cluster writes the cluster's client key.
func (r *installRun) refreshKubeconfigSecrets() {
	if r == nil {
		return
	}
	registerKubeconfigSecrets(r.events)
}
