// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installjson"
)

// Kubeconfig canaries: a credential of each kind a kubeconfig user can carry.
const (
	kubeCanaryToken    = "dcCanaryKubeTokenA-7Qw2Er9Ty4"
	kubeCanaryTokenDup = "dcCanaryKubeTokenB-3Zx8Cv1Bn6"
	kubeCanaryPassword = "dcCanaryKubePw-5Hj2Kl8Mn3"
	kubeCanaryKeyCore  = "dcCanaryKubeKeyPem9Fg4Df7Sa2Lk"
	kubeCanaryExecEnv  = "dcCanaryKubeExecEnv-6Rt3Yu1Io8"
	kubeCanaryIDToken  = "dcCanaryKubeIdToken-2Pl9Ok4Ij7"
)

var kubeCanaryKeyPEM = "-----BEGIN EC PRIVATE KEY-----\n" + kubeCanaryKeyCore + "\n-----END EC PRIVATE KEY-----\n"

// canaryKubeconfig is a kubeconfig client-go refuses: two users named "golden". Its
// conversion error prints every user record, credentials included.
var canaryKubeconfig = fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: golden
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context:
    cluster: golden
    user: golden
current-context: prod
users:
- name: golden
  user:
    token: %s
    username: admin
    password: %s
    client-key-data: %s
- name: golden
  user:
    token: %s
    auth-provider:
      name: oidc
      config:
        id-token: %s
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: dc-no-such-plugin
      interactiveMode: IfAvailable
      env:
      - name: DC_PLUGIN_SECRET
        value: %s
`, kubeCanaryToken, kubeCanaryPassword, base64.StdEncoding.EncodeToString([]byte(kubeCanaryKeyPEM)),
	kubeCanaryTokenDup, kubeCanaryIDToken, kubeCanaryExecEnv)

// assertNoKubeCanary fails if b holds any kubeconfig canary, in any form client-go's
// error or a redaction miss would print it: raw, base64, or a []byte's %v.
func assertNoKubeCanary(t *testing.T, where string, b []byte) {
	t.Helper()
	keyBytes := fmt.Sprint([]byte(kubeCanaryKeyCore))
	for _, c := range []string{kubeCanaryToken, kubeCanaryTokenDup, kubeCanaryPassword, kubeCanaryKeyCore,
		kubeCanaryExecEnv, kubeCanaryIDToken, base64.StdEncoding.EncodeToString([]byte(kubeCanaryKeyPEM)),
		keyBytes[1 : len(keyBytes)-1]} {
		if bytes.Contains(b, []byte(c)) {
			t.Errorf("%s holds a kubeconfig credential (%q):\n%s", where, c, b)
		}
	}
}

// 🔴 A KUBECONFIG'S CREDENTIALS NEVER REACH dcctl's OUTPUT, in either format, even when
// client-go's own refusal of the file would print them. The preflight (which reports
// the kubeconfig) and the engine (which reads it to find the context) each meet the
// broken file; each must say that it could not be read, and name it, and nothing else.
func TestInstallKubeconfigCredentialsNeverReachOutput(t *testing.T) {
	for _, name := range []string{"kubeconfig-duplicate-users", "kubeconfig-duplicate-users-engine",
		"json-kubeconfig-duplicate-users", "json-kubeconfig-duplicate-users-engine"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sc := findInstallScenario(t, name)
			r := runInstallChildRaw(t, sc)
			assertNoKubeCanary(t, "stdout", r.stdout)
			assertNoKubeCanary(t, "stderr", r.stderr)
			if r.exit != 1 {
				t.Errorf("exit %d, want 1", r.exit)
			}
			// It still says what is wrong, and where.
			all := string(r.stdout) + string(r.stderr)
			if !strings.Contains(all, "could not be read") || !strings.Contains(all, "kubeconfig") {
				t.Errorf("the output does not say the kubeconfig could not be read:\nstdout:\n%s\nstderr:\n%s",
					r.stdout, r.stderr)
			}
		})
	}
}

// 🔴 THE EXIT STATUS IS THE VERDICT. A failure wrapping an error with an exit status of
// its own (a kind child killed by the interrupt is -1, which the process would exit
// as 255) exits 1, in both formats.
func TestInstallExitIsTheVerdictWhateverTheErrorWraps(t *testing.T) {
	for _, name := range []string{"engine-exit-status", "json-engine-exit-status"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInstallChildRaw(t, findInstallScenario(t, name))
			if r.exit != 1 {
				t.Errorf("exit %d, want 1", r.exit)
			}
		})
	}
}

// --help with --output json writes the help to stderr: stdout is events or nothing.
func TestInstallJSONHelpLeavesStdoutEmpty(t *testing.T) {
	r := runInstallChildRaw(t, findInstallScenario(t, "json-help"))
	if len(r.stdout) != 0 {
		t.Errorf("stdout is not empty:\n%s", r.stdout)
	}
	if r.exit != 0 || !strings.Contains(string(r.stderr), "Usage:") || !strings.Contains(string(r.stderr), "--output") {
		t.Errorf("exit %d, stderr %q: want 0 and the install's help", r.exit, r.stderr)
	}
}

// Nothing in a --output json run can read stdin, the engine included: what a
// kubeconfig exec plugin would be handed is at EOF and never a terminal.
func TestInstallJSONStdinIsUnreadableDuringTheRun(t *testing.T) {
	r := runInstallChildRaw(t, findInstallScenario(t, "json-stdin-unreadable"))
	envs, err := installjson.DecodeStream(r.stdout)
	if err != nil {
		t.Fatal(err)
	}
	sum := envs[len(envs)-1].Payload.(installflow.Summary)
	if sum.Outcome != installflow.RunSucceeded || r.exit != 0 {
		t.Errorf("summary %s, exit %d, stderr %s: the engine could read stdin", sum.Outcome, r.exit, r.stderr)
	}
}

// 🔴 A PANIC STILL ENDS THE STREAM WITH ITS SUMMARY, AND THE EXIT AGREES WITH IT.
func TestInstallJSONPanicEndsWithTheVerdict(t *testing.T) {
	r := runInstallChildRaw(t, findInstallScenario(t, "json-engine-panic"))
	envs, err := installjson.DecodeStream(r.stdout)
	if err != nil {
		t.Fatalf("%v\nstdout:\n%s", err, r.stdout)
	}
	if len(envs) == 0 {
		t.Fatalf("no events; stderr:\n%s", r.stderr)
	}
	sum, ok := envs[len(envs)-1].Payload.(installflow.Summary)
	if !ok || sum.Outcome != installflow.RunFailed {
		t.Fatalf("the last line is %+v, want a failed run.summary", envs[len(envs)-1])
	}
	if r.exit != sum.ExitCode() {
		t.Errorf("exit %d, but the summary says %d", r.exit, sum.ExitCode())
	}
	if !strings.Contains(string(r.stderr), "the fake engine panicked") {
		t.Errorf("stderr does not carry the panic:\n%s", r.stderr)
	}
}

// echoKubeconfig is a usable kubeconfig whose user carries the canary token.
var echoKubeconfig = strings.Replace(goldenKubeconfig("prod"), "token: not-a-real-token", "token: "+kubeCanaryToken, 1)

// 🔴 A CREDENTIAL THAT REACHES THE PROCESS'S OWN STREAMS OR THE ERROR IS REDACTED. With
// --output json, a write to the process's stderr (a kubeconfig credential plugin's,
// which client-go hands os.Stderr) and the error the command prints are both
// redacted. With --output text the error is; a write straight to the terminal's
// stderr is not, as the events page says.
func TestInstallCredentialEchoedOutsideTheConsoleIsRedacted(t *testing.T) {
	for _, name := range []string{"kubeconfig-credential-echoed", "json-kubeconfig-credential-echoed"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInstallChildRaw(t, findInstallScenario(t, name))
			if r.exit != 1 {
				t.Errorf("exit %d, want 1", r.exit)
			}
			assertNoKubeCanary(t, "stdout", r.stdout)
			for _, line := range strings.Split(string(r.stderr), "\n") {
				if strings.HasPrefix(line, "plugin: token ") && !strings.HasPrefix(name, "json-") {
					continue // text mode: written to the terminal directly, documented as not redacted
				}
				assertNoKubeCanary(t, "stderr line", []byte(line))
			}
			if !strings.Contains(string(r.stderr), "provider says [redacted]") {
				t.Errorf("the error is not there, redacted:\n%s", r.stderr)
			}
		})
	}
}

const proxyCanary = "dcCanaryProxySecret5Mn2"

// proxyKubeconfig parses, but its cluster's proxy-url carries a password that
// client-go's validation quotes whole.
var proxyKubeconfig = strings.Replace(goldenKubeconfig("prod"), "    server: https://127.0.0.1:1\n",
	"    server: https://127.0.0.1:1\n    proxy-url: http://user:"+proxyCanary+"@proxy:bad\n", 1)

// 🔴 A KUBECONFIG THAT PARSES BUT DOES NOT VALIDATE never has client-go's reason
// printed, by install or bootstrap, in either output format, dry run or not: the
// proxy-url's password stays out of stdout and stderr, and the refusal still says
// what is wrong and where.
func TestKubeconfigValidationErrorsNeverReachTheOutput(t *testing.T) {
	for _, name := range []string{"install-proxy-url", "json-install-proxy-url", "bootstrap-proxy-url-dry-run",
		"bootstrap-proxy-url-no-escrow"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInstallChildRaw(t, findInstallScenario(t, name))
			all := string(r.stdout) + string(r.stderr)
			if strings.Contains(all, proxyCanary) {
				t.Errorf("the proxy-url's password reached the output:\nstdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
			}
			if !strings.Contains(all, "has an invalid entry for the context") {
				t.Errorf("the output does not say the kubeconfig entry is invalid:\nstdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
			}
		})
	}
}

// twoContextKubeconfig has the contexts "production" and "prod", so the preflight
// lists "prod" as a whole token and "production" beside it.
var twoContextKubeconfig = strings.Replace(goldenKubeconfig("production"), "current-context:",
	"- name: prod\n  context:\n    cluster: golden\n    user: golden\ncurrent-context:", 1)

// profileKubeconfig names its context "production", and its user's exec plugin has
// AWS_PROFILE=prod: a found value that is also the start of a word the output shows.
var profileKubeconfig = twoContextKubeconfig +
	"    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: dc-plugin\n" +
	"      env:\n      - name: AWS_PROFILE\n        value: prod\n"

// 🔴 A FOUND VALUE NEVER CORRUPTS THE OUTPUT. With a kubeconfig whose exec plugin has
// AWS_PROFILE=prod, the preflight still reports "current-context: production": in
// the event stream (prod is redacted only as a whole token), and in text output,
// which is byte for byte what the same kubeconfig without that value prints.
func TestAFoundValueNeverCorruptsTheOutput(t *testing.T) {
	plainWith := runInstallChildRaw(t, findInstallScenario(t, "plain-profile-prod"))
	plainWithout := runInstallChildRaw(t, findInstallScenario(t, "plain-profile-none"))
	if !bytes.Contains(plainWith.stdout, []byte("current-context: production")) {
		t.Errorf("text output lost the context name:\n%s", plainWith.stdout)
	}
	// The contexts are listed in map order, so the lines are compared as sets.
	if !bytes.Contains(plainWith.stdout, []byte("- prod\n")) || sortedLines(plainWith.stdout) != sortedLines(plainWithout.stdout) ||
		!bytes.Equal(plainWith.stderr, plainWithout.stderr) || plainWith.exit != plainWithout.exit {
		t.Errorf("text output changed with a registered kubeconfig value:\nwith:\n%s\nwithout:\n%s",
			plainWith.stdout, plainWithout.stdout)
	}
	js := runInstallChildRaw(t, findInstallScenario(t, "json-profile-prod"))
	if !bytes.Contains(js.stdout, []byte(`"highlight":"production"`)) || bytes.Contains(js.stdout, []byte("[redacted]uction")) {
		t.Errorf("the stream lost the context name:\n%s", js.stdout)
	}
}

func sortedLines(b []byte) string {
	lines := strings.Split(string(b), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
