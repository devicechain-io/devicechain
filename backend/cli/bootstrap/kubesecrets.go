// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io"
	"os"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"sigs.k8s.io/yaml"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// credential is one secret value and how it is registered.
type credential struct {
	value string
	// found marks a value dcctl found rather than made: everything in a kubeconfig.
	// A typed or credential-shaped value is registered redact-only (AddRedaction),
	// as a whole token, and never refuses an event code. A kubeconfig holds
	// credentials of users and plugins this run never touches, and they can be
	// ordinary words: an unused user whose password is "install" must not stop an
	// install. Ordinary plugin settings, such as a profile named "prod", are not
	// registered. dcctl's own secrets (a root key, a passphrase, a minted password,
	// the alert and backup credentials it was given)
	// are registered strictly.
	found bool
	// typed marks a found value that is a credential by what it is, not by where it
	// sits: a token, a password, a client key, a URL's password, and an exec env
	// value, exec argument or auth-provider setting whose NAME says it holds one.
	// Only a typed value too short to redact is counted as uncovered; an exec
	// plugin's AWS_PROFILE=dev is a setting, not a credential, and is never warned
	// about.
	typed bool
}

// kubeconfigSecrets returns the credential values in the kubeconfig files client-go
// reads: each user's token, password and client key, its auth provider's settings,
// its exec plugin's environment and the credentials in its arguments, and the
// password in any cluster's server or proxy URL. Every one is a found value.
//
// It parses each file on its own, as a plain list, rather than through client-go's
// loader. A file client-go refuses — two users of the same name is the case that
// matters — still has its credentials found, which is exactly when they are most
// likely to be printed. A file that cannot be read or parsed at all contributes
// nothing; this is the second line, not the first.
func kubeconfigSecrets(rules *clientcmd.ClientConfigLoadingRules) []credential {
	var out []credential
	add := func(v string, typed bool) {
		if v != "" {
			out = append(out, credential{value: v, found: true, typed: typed})
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
			add(urlPassword(c.Cluster.Server), true)
			add(urlPassword(c.Cluster.ProxyURL), true)
		}
		for _, u := range cfg.AuthInfos {
			a := u.AuthInfo
			add(a.Token, true)
			add(a.Password, true)
			add(string(a.ClientKeyData), true)
			if a.AuthProvider != nil {
				for k, v := range a.AuthProvider.Config {
					add(v, sensitiveFlag(k))
				}
			}
			if a.Exec != nil {
				for _, env := range a.Exec.Env {
					add(env.Value, sensitiveFlag(env.Name))
				}
				// A flag's value is there because its name says credential; a shaped
				// argument is at least 20 bytes, never too short to redact.
				for _, v := range execArgSecrets(a.Exec.Args) {
					add(v, true)
				}
			}
		}
	}
	return out
}

// execArgSecrets is the credentials among an exec plugin's arguments: the value of
// any flag whose name says it holds one (--client-secret X, --token=X), and any other
// argument shaped like a credential. A plugin that echoes its command line, as many
// do when they fail, would otherwise print them.
func execArgSecrets(args []string) []string {
	var out []string
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if !sensitiveFlag(name) {
				continue
			}
			if inline {
				out = append(out, value)
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				out = append(out, args[i+1])
			}
			continue
		}
		if credentialShaped(a) {
			out = append(out, a)
		}
	}
	return out
}

// sensitiveFlag reports whether a flag's, env variable's or setting's name says its
// value is a credential.
func sensitiveFlag(name string) bool {
	name = strings.ToLower(name)
	for _, w := range []string{"secret", "token", "password", "passwd", "key", "credential"} {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// credentialShaped reports whether an argument no flag names looks like a
// credential rather than a word, a name, a path or a URL: at least 20 bytes, only
// the characters keys and tokens are written in (letters, digits, '+', '/', '=',
// '_'), not starting with '/', and both letters and digits. "get-token",
// "us-east-1", a cluster name with dashes and an absolute path are not; a 40-byte
// access key is.
func credentialShaped(a string) bool {
	if len(a) < 20 || a[0] == '/' {
		return false
	}
	letter, digit := false, false
	for _, c := range a {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
			letter = true
		case '0' <= c && c <= '9':
			digit = true
		case c == '+' || c == '/' || c == '=' || c == '_':
		default:
			return false
		}
	}
	return letter && digit
}

// registerCredentials registers each value with the hub, and returns how many it
// could not cover.
//
// A found value is registered redact-only if typed or credential-shaped; ordinary
// plugin settings are skipped. One of dcctl's own is registered strictly.
//
// 🔴 A VALUE THE HUB REFUSES IS NOT DROPPED SILENTLY. One of dcctl's own too short for
// a strict registration (under installflow.MinSecretBytes) is registered redact-only,
// which takes values down to installflow.MinRedactionBytes; only a value shorter than
// that, which cannot be told from ordinary text even as a whole token, is left
// uncovered, and counted, so the run can say so.
func registerCredentials(events installflow.Reporter, creds []credential) (uncovered int) {
	if events == nil {
		return 0
	}
	for _, c := range creds {
		if c.found && !c.typed && !credentialShaped(c.value) {
			continue
		}
		if !c.found && events.AddSecret(c.value) == nil {
			continue
		}
		if events.AddRedaction(c.value) != nil && (!c.found || c.typed) {
			uncovered++
		}
	}
	return uncovered
}

// registerKubeconfigSecrets tells the hub the kubeconfig's credentials, so a tool
// that echoes one (kubectl, helm or an OpenTofu provider reporting a kubeconfig it
// could not use) has it redacted. Like registerSecrets, it is defence in depth: no
// producer puts a kubeconfig value in an event, and a kubeconfig that cannot be
// used is reported without client-go's reason. A credential it cannot cover is
// warned about on w.
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

// warnings is where the engine says what it could not redact: each line a log.line
// event at warn level.
//
// 🔴 NEVER THE CONSOLE. Text output renders no log.line event, so it prints exactly
// what it printed before redaction existed; the event stream carries the warning,
// for the program that reads it.
func (r *installRun) warnings() io.Writer { return runWarnings{r} }

type runWarnings struct{ r *installRun }

func (w runWarnings) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line != "" {
			w.r.emit(installflow.Event{Payload: installflow.LogLine{Level: installflow.LogWarn,
				Source: "dcctl", Text: installflow.Text(line)}})
		}
	}
	return len(b), nil
}
