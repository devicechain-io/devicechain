// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"io"
	"net/url"
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
	// the alert and backup credentials it was given) are registered strictly, as
	// substrings.
	found bool
	// generated marks one of dcctl's own values that it generated at random: the
	// instance root key and the credentials it mints. Only such a value refuses an
	// event code that contains it (AddGeneratedSecret). A value a person chose — the
	// escrow passphrase, an SMTP password — can be a word of dcctl's own vocabulary
	// ("install", "bootstrap"), and refusing the codes that spell it made a run
	// that had done everything exit 1; it is redacted from text all the same.
	generated bool
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
		// A setting's name need not say credential: HTTPS_PROXY is a common case.
		// Register the password separately, since tools can echo just that part.
		if password := urlPassword(v); password != "" {
			out = append(out, credential{value: password, found: true, typed: true})
		}
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
					add(v, sensitiveSetting(k, v))
				}
			}
			if a.Exec != nil {
				for _, env := range a.Exec.Env {
					add(env.Value, sensitiveSetting(env.Name, env.Value))
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
// any flag whose name says it holds one (--client-secret X, --token=X), any other
// argument shaped like a credential, and the password of any URL among them. A plugin
// that echoes its command line, as many do when they fail, would otherwise print them.
func execArgSecrets(args []string) []string {
	var out []string
	for i, a := range args {
		if password := urlPassword(a); password != "" {
			out = append(out, password)
		}
		if strings.HasPrefix(a, "-") {
			name, value, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if inline {
				if password := urlPassword(value); password != "" {
					out = append(out, password)
				}
			}
			if !inline && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				value = args[i+1]
			}
			if value != "" && sensitiveSetting(name, value) {
				out = append(out, value)
			} else if inline && credentialShaped(value) {
				// A flag whose name says nothing can still carry one (--session=<a JWT>).
				// A separate next argument is judged on its own shape, as a positional.
				out = append(out, value)
			}
			continue
		}
		if credentialShaped(a) {
			out = append(out, a)
		}
	}
	return out
}

// strongCredentialWords name a credential wherever they appear in a setting's name.
var strongCredentialWords = []string{"secret", "token", "password", "passwd", "passphrase", "key", "credential", "bearer"}

// weakCredentialWords name a credential in DB_PASS or BASIC_AUTH, but just as often
// name something else: BYPASS, AUTH_MODE, AUTHORITY_HOST. See sensitiveSetting.
var weakCredentialWords = []string{"pass", "auth"}

// sensitiveFlag reports whether a flag's, env variable's or setting's name says its
// value may be a credential, by a strong or a weak word.
func sensitiveFlag(name string) bool {
	return strongName(name) || weakName(name)
}

func strongName(name string) bool { return nameHasAny(name, strongCredentialWords) }
func weakName(name string) bool   { return nameHasAny(name, weakCredentialWords) }

func nameHasAny(name string, words []string) bool {
	name = strings.ToLower(name)
	for _, w := range words {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// sensitiveSetting reports whether a named value is to be redacted as a credential
// because of its name. A strong word always says so. A weak word says so unless
// the value is an ordinary word: AUTH_MODE=default must not register "default",
// which the hub would then blank out of every line that says it.
func sensitiveSetting(name, value string) bool {
	return strongName(name) || (weakName(name) && !ordinaryWord(value))
}

// ordinaryWord reports whether a value reads as a word, a name or a number rather
// than a secret: under 20 bytes and a plain name ("default", "us-east-1",
// "eu.gcr.io", "1.27").
func ordinaryWord(v string) bool { return len(v) < 20 && plainName(v) }

// plainName reports whether v is made only of components, separated by '-', '.' or
// '_', each all lowercase letters, all uppercase letters or all digits. A cluster
// name, a region, a host name and a version are; anything mixing letters with
// digits or cases inside a component, or holding any other byte, is not.
func plainName(v string) bool {
	if v == "" {
		return false
	}
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '.' || r == '_' }) {
		lower, upper, digit := 0, 0, 0
		for _, c := range part {
			switch {
			case 'a' <= c && c <= 'z':
				lower = 1
			case 'A' <= c && c <= 'Z':
				upper = 1
			case '0' <= c && c <= '9':
				digit = 1
			default:
				return false
			}
		}
		if lower+upper+digit != 1 {
			return false
		}
	}
	// FieldsFunc drops empty components: "a--b" and ".a" are not plain names.
	return !strings.Contains(v, "--") && !strings.Contains(v, "..") &&
		strings.Trim(v, "-._") == v
}

// credentialShaped reports whether a value no name marks looks like a credential
// rather than a word, a name, a path or a URL. Two shapes are:
//   - a multiword passphrase: whitespace inside, at least 8 bytes;
//   - a long opaque value: at least 20 bytes, not starting with '/' (a path), not a URL, and
//     not a plain name (see plainName). A JWT, an access key, a "key:secret" pair
//     and a long passphrase written with '-', '.' or ':' are; "us-east-1", a
//     cluster or host name and an absolute path are not.
//
// 🔴 NEVER SHORT. Each value it accepts is registered as a token the hub blanks
// wherever it stands alone, so a short one ("1.27", "eu.gcr.io", "a:b") would erase
// ordinary words from the output. A short secret under a name that says credential
// is caught by its name instead; a URL's password by urlPassword.
func credentialShaped(a string) bool {
	if strings.ContainsAny(a, " \t\r\n") {
		return len(strings.TrimSpace(a)) >= 8
	}
	if len(a) < 20 || a[0] == '/' || isURL(a) {
		return false
	}
	// A long value is exempt only as a plain NAME; one the previous shape already
	// registered (letters and digits in the key alphabet, such as a long
	// "word_1234" passphrase) stays registered even though it parses as a plain name.
	return keyAlphabetShaped(a) || !plainName(a)
}

// keyAlphabetShaped is the shape credentialShaped registered before plainName
// existed: only letters, digits, '+', '/', '=' and '_', with both letters and digits.
func keyAlphabetShaped(a string) bool {
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

// isURL reports whether a is an absolute URL with a host. Its password, if it has
// one, is registered on its own (urlPassword); the rest is an address, not a secret.
func isURL(a string) bool {
	u, err := url.Parse(a)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// registerCredentials registers each value with the hub, and returns how many it
// could not cover.
//
// A found value is registered redact-only if typed or credential-shaped; ordinary
// plugin settings are skipped. One of dcctl's own is registered strictly, refusing
// identifiers only when dcctl generated it.
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
		if !c.found && strictly(events, c) == nil {
			continue
		}
		if events.AddRedaction(c.value) != nil && (!c.found || c.typed) {
			uncovered++
		}
	}
	return uncovered
}

// strictly registers one of dcctl's own values: see credential.generated.
func strictly(events installflow.Reporter, c credential) error {
	if c.generated {
		return events.AddGeneratedSecret(c.value)
	}
	return events.AddSecret(c.value)
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
