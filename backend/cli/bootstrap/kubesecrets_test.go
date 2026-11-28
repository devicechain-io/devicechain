// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installjson"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

const (
	kcToken    = "dcCanaryKcTokenA-7Qw2Er9Ty4"
	kcTokenDup = "dcCanaryKcTokenB-3Zx8Cv1Bn6"
	kcPassword = "dcCanaryKcPw-5Hj2Kl8Mn3"
	kcKeyCore  = "dcCanaryKcKeyPem9Fg4Df7Sa2Lk"
	kcExecEnv  = "dcCanaryKcExecEnv-6Rt3Yu1Io8"
	kcIDToken  = "dcCanaryKcIdToken-2Pl9Ok4Ij7"
	// kcShortEnv is an exec env value too short to be a credential: a profile name.
	kcShortEnv = "staging"
)

var kcKeyPEM = "-----BEGIN EC PRIVATE KEY-----\n" + kcKeyCore + "\n-----END EC PRIVATE KEY-----\n"

// writeCanaryKubeconfig writes a kubeconfig client-go refuses (two users named
// "golden"), carrying a credential of every kind, and points KUBECONFIG at it.
func writeCanaryKubeconfig(t *testing.T) string {
	t.Helper()
	kc := "apiVersion: v1\nkind: Config\nclusters:\n- name: golden\n  cluster:\n    server: https://127.0.0.1:1\n" +
		"contexts:\n- name: prod\n  context:\n    cluster: golden\n    user: golden\ncurrent-context: prod\n" +
		"users:\n- name: golden\n  user:\n    token: " + kcToken + "\n    password: " + kcPassword +
		"\n    client-key-data: " + base64.StdEncoding.EncodeToString([]byte(kcKeyPEM)) +
		"\n- name: golden\n  user:\n    token: " + kcTokenDup +
		"\n    auth-provider:\n      name: oidc\n      config:\n        id-token: " + kcIDToken +
		"\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: dc-no-such-plugin\n" +
		"      env:\n      - name: DC_PLUGIN_SECRET\n        value: " + kcExecEnv +
		"\n      - name: AWS_PROFILE\n        value: " + kcShortEnv + "\n"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return path
}

// A kubeconfig client-go refuses is reported by path, never with client-go's reason,
// which quotes every user record of a file with two users of one name.
func TestKubeconfigErrorsNeverQuoteTheFile(t *testing.T) {
	path := writeCanaryKubeconfig(t)
	_, _, kerr := KubeContexts()
	_, rerr := RestConfig("prod")
	_, ferr := freezeKubeTarget("prod")
	for name, err := range map[string]error{"KubeContexts": kerr, "RestConfig": rerr, "freezeKubeTarget": ferr} {
		if err == nil {
			t.Fatalf("%s accepted a kubeconfig client-go refuses", name)
		}
		if !IsKubeconfigError(err) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v, want a KubeconfigError naming %s", name, err, path)
		}
		for _, c := range []string{kcToken, kcTokenDup, kcPassword, kcKeyCore, kcExecEnv, kcIDToken, "golden"} {
			if strings.Contains(err.Error(), c) {
				t.Errorf("%s's error quotes the file (%q): %v", name, c, err)
			}
		}
	}
}

// The kubeconfig's credentials are found even in a file client-go refuses, and every
// one is a found value: registered redact-only, as a whole token, never strictly.
func TestKubeconfigSecretsFindsEveryCredential(t *testing.T) {
	writeCanaryKubeconfig(t)
	got := map[string]credential{}
	for _, c := range kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules()) {
		got[c.value] = c
	}
	for _, want := range []string{kcToken, kcTokenDup, kcPassword, kcKeyPEM, kcExecEnv, kcIDToken, kcShortEnv} {
		c, ok := got[want]
		if !ok {
			t.Errorf("%q was not found", want)
		} else if !c.found {
			t.Errorf("%q is registered strictly: a kubeconfig value must never refuse an event code", want)
		}
	}
}

// An exec plugin's arguments: the value of a flag that names a credential, inline or
// next, and an argument shaped like one; never a word, a region or a path.
func TestExecArgSecrets(t *testing.T) {
	got := execArgSecrets([]string{"get-token", "--client-secret", "s3cr", "--token=tk-inline",
		"--region", "us-east-1", "--cluster-name", "production-cluster-01", "AKIA1234567890ABCDEF",
		"/usr/local/bin/helper-2024", "--api-key", "--verbose"})
	want := []string{"s3cr", "tk-inline", "AKIA1234567890ABCDEF"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 🔴 DEFENCE IN DEPTH: a tool that echoes a kubeconfig credential into the console has
// it redacted from the stream. The engine registers the kubeconfig's credentials when
// the run starts.
func TestKubeconfigSecretsAreRedactedFromTheStream(t *testing.T) {
	writeCanaryKubeconfig(t)
	printed := []string{kcToken, kcTokenDup, kcPassword, kcKeyCore, kcExecEnv, kcIDToken}
	e := &InstallEngine{
		Provider: &fakeProvider{name: "local"}, Options: InstallOptions{Options: Options{AssumeYes: true}}, SkipPreflight: true,
		Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
			c := out(ctx)
			for _, v := range printed {
				c.println("kubectl says: " + v)
			}
			return nil
		},
	}
	var stream bytes.Buffer
	err := installjson.Presenter{Stdout: &stream, RunID: "kubeconfig", Clock: testkit.NewManualClock(testkit.Epoch)}.Run(
		t.Context(), e, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, derr := installjson.DecodeStream(stream.Bytes()); derr != nil {
		t.Fatal(derr)
	}
	for _, v := range printed {
		if bytes.Contains(stream.Bytes(), []byte(v)) {
			t.Errorf("the stream carries %q", v)
		}
	}
	if n := bytes.Count(stream.Bytes(), []byte("[redacted]")); n < len(printed) {
		t.Errorf("%d redactions for %d printed credentials", n, len(printed))
	}
}

// JSON and the TUI both use whole-token redaction in events. Ordinary plugin
// settings must survive, while typed and credential-shaped values stay covered.
func TestKubeconfigPluginSettingsInJSONAndTUIEvents(t *testing.T) {
	for _, mode := range []string{"json", "tui"} {
		t.Run(mode, func(t *testing.T) {
			const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl"
			writeKubeconfig(t, validKubeconfig("", "    auth-provider:\n      name: oidc\n      config:\n"+
				"        mode: default\n        region: us-east-1\n        namespace: devicechain-production-namespace\n"+
				"        opaque: ProviderCredential1234567890\n        client-secret: cs4x\n"+
				"    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: aws\n"+
				"      env:\n      - name: HTTPS_PROXY\n        value: http://alice:Pr0xy-pw@proxy:3128\n"+
				"      - name: SESSION\n        value: "+jwt+"\n"+
				"      - name: OPAQUE\n        value: Long.p4ss:Phrase-7Qw2Er9Ty4\n"+
				"      - name: SESSION\n        value: correcthorsebatterystaple_1234\n"+
				"      - name: AWS_PROFILE\n        value: default\n"+
				"      - name: AUTH_MODE\n        value: default\n"+
				"      - name: COMPASS_REGISTRY\n        value: eu.gcr.io\n"+
				"      - name: KUBE_VERSION\n        value: 1.27.3\n"+
				"      - name: DB_PASS\n        value: Pw4short\n"+
				"      - name: AWS_REGION\n        value: us-east-1\n"+
				"      - name: OPAQUE\n        value: EnvCredential1234567890\n"+
				"      - name: API_TOKEN\n        value: tk4x\n"))
			lines := []string{"docker context default", "namespace default", "helm default us-east-1",
				"proxy password Pr0xy-pw", "session " + jwt, "opaque Long.p4ss:Phrase-7Qw2Er9Ty4",
				"namespace devicechain-production-namespace", "env EnvCredential1234567890", "provider ProviderCredential1234567890",
				"typed env tk4x", "typed provider cs4x", "registry eu.gcr.io", "kubectl 1.27.3", "db pass Pw4short", "horse correcthorsebatterystaple_1234"}
			e := &InstallEngine{
				Provider: &fakeProvider{name: "local"}, Options: InstallOptions{Options: Options{AssumeYes: true}}, SkipPreflight: true,
				Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
					for _, line := range lines {
						out(ctx).println(line)
					}
					return nil
				},
			}
			req := installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}
			var envs []installflow.Envelope
			if mode == "json" {
				var stream bytes.Buffer
				if err := (installjson.Presenter{Stdout: &stream, RunID: "plugin-settings",
					Clock: testkit.NewManualClock(testkit.Epoch)}).Run(t.Context(), e, req, nil); err != nil {
					t.Fatal(err)
				}
				var err error
				envs, err = installjson.DecodeStream(stream.Bytes())
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// Capture the events delivered to the TUI, using its hub options and
				// console log writer, without starting a terminal event loop.
				sink := &testkit.CaptureSink{}
				hub, err := installflow.NewHub(installflow.Options{Sink: sink,
					Clock: testkit.NewManualClock(testkit.Epoch)})
				if err != nil {
					t.Fatal(err)
				}
				log, err := hub.LogWriter("", "dcctl", installflow.LogInfo)
				if err != nil {
					t.Fatal(err)
				}
				_, err = hub.Run(t.Context(), e, req, installflow.Runtime{
					Prompts: installflow.NewBroker(hub, nil, true),
					Console: installflow.Console{Stdout: log, Stderr: log, Flush: hub.Flush},
				})
				<-hub.Done()
				if err != nil {
					t.Fatal(err)
				}
				envs = sink.Envelopes()
				if got := hub.Redact("profile default, token tk4x"); got != "profile default, token [redacted]" {
					t.Errorf("TUI composed line = %q", got)
				}
			}
			if err := testkit.VerifyStream(envs); err != nil {
				t.Fatal(err)
			}
			logs := map[string]bool{}
			for _, env := range envs {
				if line, ok := env.Payload.(installflow.LogLine); ok {
					logs[line.Text.String()] = true
				}
			}
			for _, want := range []string{"docker context default", "namespace default", "helm default us-east-1",
				"proxy password [redacted]", "session [redacted]", "opaque [redacted]",
				"namespace devicechain-production-namespace", "env [redacted]", "provider [redacted]", "typed env [redacted]", "typed provider [redacted]",
				"registry eu.gcr.io", "kubectl 1.27.3", "db pass [redacted]", "horse [redacted]"} {
				if !logs[want] {
					t.Errorf("missing log %q; got %v", want, logs)
				}
			}
		})
	}
}

// writeKubeconfig writes content as the only kubeconfig and points KUBECONFIG at it.
func writeKubeconfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return path
}

const kcValidationCanary = "dcCanaryKcValidation7Hn3Qe"

// validKubeconfig is a well-formed kubeconfig whose cluster and user carry the
// extra lines given, each of which client-go's validation would quote.
func validKubeconfig(cluster, user string) string {
	return "apiVersion: v1\nkind: Config\nclusters:\n- name: golden\n  cluster:\n    server: https://127.0.0.1:1\n" +
		cluster + "contexts:\n- name: prod\n  context:\n    cluster: golden\n    user: golden\n" +
		"current-context: prod\nusers:\n- name: golden\n  user:\n" + user
}

// 🔴 NOR DOES ANY OTHER CLIENT-GO ERROR REACH THE OUTPUT. A kubeconfig that parses
// but does not validate is refused by client-go with a reason that quotes the
// offending value: a proxy-url (password and all), a certificate or key path, an
// exec plugin setting. Every one is reported as the kind of failure, the file and
// the context's name, and nothing from inside the entry.
func TestKubeconfigValidationErrorsNeverQuoteTheEntry(t *testing.T) {
	c := kcValidationCanary
	for name, kc := range map[string]string{
		"proxy-url with a password": validKubeconfig("    proxy-url: http://user:"+c+"@proxy:bad\n", "    token: tok-"+c+"\n"),
		"unreadable certificate authority": validKubeconfig("    certificate-authority: /nonexistent/"+c+"/ca.crt\n",
			"    token: tok-"+c+"\n"),
		"unreadable client certificate": validKubeconfig("",
			"    client-certificate: /nonexistent/"+c+"/client.crt\n    client-key: /nonexistent/"+c+"/client.key\n"),
		"invalid exec interactive mode": validKubeconfig("", "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n"+
			"      command: dc-plugin\n      interactiveMode: "+c+"\n      env:\n      - name: TOKEN\n        value: "+c+"\n"),
		"two authentication methods": validKubeconfig("", "    token: tok-"+c+"\n    username: admin\n    password: pw-"+c+"\n"),
		// Parsed only when a client is built, and then quoted with its userinfo.
		"unparseable server URL with a password": strings.Replace(validKubeconfig("", "    token: tok-"+c+"\n"),
			"server: https://127.0.0.1:1", "server: https://admin:"+c+"@127.0.0.1:bad", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := writeKubeconfig(t, kc)
			_, rerr := RestConfig("prod")
			_, ferr := freezeKubeTarget("prod")
			for which, err := range map[string]error{"RestConfig": rerr, "freezeKubeTarget": ferr} {
				if err == nil {
					t.Fatalf("%s accepted the entry", which)
				}
				if !IsKubeconfigError(err) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), `"prod"`) {
					t.Errorf("%s: %v, want a KubeconfigError naming %s and the context", which, err, path)
				}
				if strings.Contains(err.Error(), c) {
					t.Errorf("%s's error quotes the entry: %v", which, err)
				}
			}
		})
	}
	// A context the file does not define is named; nothing else is.
	writeKubeconfig(t, validKubeconfig("", "    token: tok-"+c+"\n"))
	if _, err := RestConfig("staging"); err == nil || err.Error() != `context "staging" does not exist` {
		t.Errorf("a missing context: %v", err)
	}
}

// A server URL's userinfo never reaches the cluster identity dcctl prints when it
// refuses to run OpenTofu against a different cluster.
func TestClusterIdentityDropsServerUserinfo(t *testing.T) {
	c := kcValidationCanary
	writeKubeconfig(t, strings.Replace(validKubeconfig("", "    token: tok-"+c+"\n"),
		"server: https://127.0.0.1:1", "server: https://admin:"+c+"@127.0.0.1:1", 1))
	target, err := freezeKubeTarget("prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(target.Identity, c) || !strings.Contains(target.Identity, "127.0.0.1:1") {
		t.Errorf("identity %q", target.Identity)
	}
	got := map[string]bool{}
	for _, cr := range kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules()) {
		got[cr.value] = true
	}
	if !got[c] {
		t.Errorf("the server URL's password is not registered for redaction")
	}
}

// 🔴 SHORT CREDENTIALS ARE STILL REDACTED, AND THE ONES THAT CANNOT BE ARE SAID. A
// five-byte token is under the strict minimum and is registered redact-only; an exec
// env value that is an ordinary word is preserved; a three-byte password cannot
// be redacted at all, and the run warns that one value is not covered.
func TestShortKubeconfigCredentials(t *testing.T) {
	writeKubeconfig(t, validKubeconfig("", "    token: tk5ab\n    password: pw3\n"+
		"    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: dc-plugin\n"+
		"      env:\n      - name: AWS_PROFILE\n        value: default\n"))
	e := &InstallEngine{
		Provider: &fakeProvider{name: "local"}, Options: InstallOptions{Options: Options{AssumeYes: true}}, SkipPreflight: true,
		Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
			out(ctx).println("kubectl says: token tk5ab, profile default")
			return nil
		},
	}
	var stream bytes.Buffer
	err := installjson.Presenter{Stdout: &stream, RunID: "short", Clock: testkit.NewManualClock(testkit.Epoch)}.Run(
		t.Context(), e, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, stream.Bytes())
	}
	text := stream.String()
	if strings.Contains(text, "tk5ab") {
		t.Error("the stream carries the token")
	}
	if !strings.Contains(text, "profile default") {
		t.Error("the stream redacted an ordinary profile name")
	}
	if !strings.Contains(text, "warning: 1 kubeconfig credential value(s) are shorter than 4 bytes") {
		t.Errorf("no warning that the 3-byte password is not covered:\n%s", text)
	}
}

// A found credential (a password that is an ordinary word) is redacted from text
// but never refuses an event code that spells it; a generated one does.
func TestRegisterCredentialsTextOnlyNeverRefusesACode(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}, Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	if n := registerCredentials(hub, []credential{{value: "default", found: true, typed: true}, {value: "s3cr3t-strict", generated: true}, {value: "ab"}}); n != 1 {
		t.Errorf("%d uncovered, want 1 (the two-byte value)", n)
	}
	if err := hub.Emit(installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"},
		Manifest: []installflow.ManifestStep{{ID: "install.default"}}}}); err != nil {
		t.Fatalf("a manifest naming a text-only value was refused: %v", err)
	}
	if err := hub.Emit(installflow.Event{Step: "install.default", Attempt: 1, Payload: installflow.StepStarted{
		Message: "install.default"}}); err != nil {
		t.Fatalf("a step code naming a text-only value was refused: %v", err)
	}
	if err := hub.Emit(installflow.Event{Step: "install.default", Attempt: 1, Payload: installflow.StepFailed{
		Code: "install.s3cr3t-strict", Retry: installflow.RetryUnknown, Effect: installflow.EffectNoneObserved}}); !errors.Is(err, installflow.ErrInvalidEvent) {
		t.Errorf("a code that is a strict secret: %v, want ErrInvalidEvent", err)
	}
	if got := hub.Redact("profile default, key s3cr3t-strict"); strings.Contains(got, "default") || strings.Contains(got, "s3cr3t") {
		t.Errorf("Redact: %q", got)
	}
}

// 🔴 ONE OF DCCTL'S OWN VALUES THAT A PERSON CHOSE — the escrow passphrase, an SMTP
// password — is registered as strictly as ever for text, cut out as a substring, and
// never refuses an event code that spells it: "install" is a substring of every
// install step id. Only a value dcctl generated refuses one.
func TestRegisterCredentialsAChosenValueNeverRefusesACode(t *testing.T) {
	for _, generated := range []bool{false, true} {
		hub, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}, Clock: testkit.NewManualClock(testkit.Epoch)})
		if err != nil {
			t.Fatal(err)
		}
		if n := registerCredentials(hub, []credential{{value: "install", generated: generated}}); n != 0 {
			t.Fatalf("%d uncovered", n)
		}
		err = hub.Emit(installflow.Event{Payload: installflow.RunStarted{Target: installflow.Target{Mode: "managed-kind"},
			Manifest: []installflow.ManifestStep{{ID: installflow.StepInstallInputs}}}})
		if generated {
			if !errors.Is(err, installflow.ErrInvalidEvent) {
				t.Errorf("the control: a generated value's code was not refused: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("a manifest spelling a chosen secret was refused: %v", err)
		}
		if got := hub.Redact("SMTP password install rejected"); strings.Contains(got, "install") {
			t.Errorf("Redact kept a chosen secret: %q", got)
		}
	}
}

// 🔴 A KUBECONFIG CREDENTIAL NEVER CHANGES WHAT A RUN DOES. A user this run never uses,
// whose password is "install", must neither refuse an event code that spells it
// (install.inputs, the manifest) nor stop the run: kubeconfig values are registered
// redact-only.
func TestAnUnusedKubeconfigPasswordNeverStopsARun(t *testing.T) {
	writeKubeconfig(t, validKubeconfig("", "    token: tok-"+kcValidationCanary+"\n")+
		"- name: unused\n  user:\n    username: someone\n    password: install\n")
	e := &InstallEngine{
		Provider: &fakeProvider{name: "local"}, Options: InstallOptions{Options: Options{AssumeYes: true}}, SkipPreflight: true,
		Cluster: func(context.Context, Provider, InstallOptions) error { return nil },
	}
	var stream bytes.Buffer
	err := installjson.Presenter{Stdout: &stream, RunID: "unused", Clock: testkit.NewManualClock(testkit.Epoch)}.Run(
		t.Context(), e, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err != nil {
		t.Fatalf("the run failed: %v\n%s", err, stream.Bytes())
	}
	envs, derr := installjson.DecodeStream(stream.Bytes())
	if derr != nil {
		t.Fatal(derr)
	}
	sum, ok := envs[len(envs)-1].Payload.(installflow.Summary)
	if !ok || sum.Outcome != installflow.RunSucceeded {
		t.Fatalf("the run ended %+v", envs[len(envs)-1].Payload)
	}
	if !bytes.Contains(stream.Bytes(), []byte(`"step_id":"install.inputs"`)) {
		t.Errorf("install.inputs was not reported:\n%s", stream.Bytes())
	}
}

// An exec plugin's credential argument in a kubeconfig is registered, so a plugin
// that echoes its command line has it redacted, and the URL userinfo form of a proxy
// password is too.
func TestKubeconfigExecArgsAndURLPasswordsAreRedacted(t *testing.T) {
	writeKubeconfig(t, validKubeconfig("    proxy-url: http://user:foo+bar:baz@proxy:3128\n",
		"    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: dc-plugin\n"+
			"      args: [\"get-token\", \"--client-secret\", \"cs-"+kcValidationCanary+"\"]\n"))
	hub, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}, Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	if n := registerCredentials(hub, kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules())); n != 0 {
		t.Errorf("%d values not covered", n)
	}
	in := "exec: dc-plugin get-token --client-secret cs-" + kcValidationCanary + " via http://user:foo+bar%3Abaz@proxy:3128"
	got := hub.Redact(in)
	if strings.Contains(got, kcValidationCanary) || strings.Contains(got, "foo+bar") || !strings.Contains(got, "get-token") {
		t.Errorf("Redact(%q) = %q", in, got)
	}
}

// Only a credential by type is counted as uncovered: a short token or password, and an
// exec env value or auth-provider setting whose name says credential. AWS_PROFILE=dev
// and an auth provider's short "mode" setting are settings: never registered,
// counted or warned about.
func TestOnlyATypedCredentialIsCountedUncovered(t *testing.T) {
	writeKubeconfig(t, validKubeconfig("", "    password: pw3\n"+
		"    auth-provider:\n      name: oidc\n      config:\n        mode: ab\n        client-secret: cs1\n"+
		"    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: dc-plugin\n"+
		"      env:\n      - name: AWS_PROFILE\n        value: dev\n      - name: API_TOKEN\n        value: t9z\n"))
	typed := map[string]bool{}
	for _, c := range kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules()) {
		typed[c.value] = c.typed
	}
	for v, want := range map[string]bool{"pw3": true, "cs1": true, "t9z": true, "dev": false, "ab": false} {
		if got, ok := typed[v]; !ok || got != want {
			t.Errorf("%q: typed %t (found %t), want %t", v, got, ok, want)
		}
	}
	hub, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}, Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	if n := registerCredentials(hub, kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules())); n != 3 {
		t.Errorf("%d uncovered, want 3 (pw3, cs1, t9z): a setting is never counted", n)
	}
}

// 🔴 TEXT OUTPUT NEVER CARRIES THE WARNING. A typed credential too short to redact is
// warned about in the event stream (TestShortKubeconfigCredentials); text output
// renders no log event, so it prints what it always printed.
func TestTheUncoveredWarningNeverReachesTextOutput(t *testing.T) {
	writeKubeconfig(t, validKubeconfig("", "    token: tok-"+kcValidationCanary+"\n    password: pw3\n"))
	e := &InstallEngine{
		Provider: &fakeProvider{name: "local"}, Options: InstallOptions{Options: Options{AssumeYes: true}}, SkipPreflight: true,
		Cluster: func(context.Context, Provider, InstallOptions) error { return nil },
	}
	var stdout, stderr bytes.Buffer
	err := installplain.Presenter{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}.Run(
		t.Context(), e, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), "warning") {
		t.Errorf("text output carries the warning:\nstdout %q\nstderr %q", stdout.String(), stderr.String())
	}
}

// The names a credential hides under: a strong word anywhere, or a weak one ("pass",
// "auth") whose value is not an ordinary word.
func TestAdditionalCredentialPredicates(t *testing.T) {
	for _, name := range []string{"PASS", "PassPhrase", "AUTH", "Bearer", "db-pass", "reauthorize"} {
		if !sensitiveFlag(name) {
			t.Errorf("%s must name a credential", name)
		}
	}
	for _, c := range []struct {
		name, value string
		want        bool
	}{
		{"BEARER", "default", true}, {"PASSPHRASE", "us-east-1", true}, // a strong word is the name's own say
		{"DB_PASS", "Pw4short", true}, {"BASIC_AUTH", "alice:pw", true},
		{"AUTH_MODE", "default", false}, {"BYPASS_REGION", "us-east-1", false},
		{"AUTHORITY_HOST", "login.example.com", false}, {"REGION", "Pw4short", false},
	} {
		if got := sensitiveSetting(c.name, c.value); got != c.want {
			t.Errorf("sensitiveSetting(%q, %q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
	for _, value := range []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl", "Long-p4ssPhrase-7Qw2Er9Ty4",
		"alpha7bravo-charlie9delta-echo", "long--namespace-identifier", "Long.p4ss:Phrase-7Qw2Er9Ty4",
		"AKIAEXAMPLE1234567890:wJalrXUtnFEMIK7MDENG", "long mixed passphrase with spaces", "two secret words"} {
		if !credentialShaped(value) {
			t.Errorf("%q must be redacted without a sensitive name", value)
		}
	}
	// 🔴 Each value accepted becomes a token blanked wherever it stands alone, so none
	// of these may be: short punctuated values, names, hosts, versions and paths.
	for _, value := range []string{"default", "us-east-1", "devicechain-production-namespace", "eu.gcr.io", "1.27.3",
		"x:y4", "a@b9", "localhost:8080", "C.UTF-8", "sts.us-east-1.amazonaws.com", "kubernetes.default.svc.cluster",
		"/home/operator/.kube/aws-config", "a b", "production-cluster-01", "https://login.example.com/oauth2/v2.0/token"} {
		if credentialShaped(value) {
			t.Errorf("ordinary setting %q must remain visible", value)
		}
	}
}

// legacyCredentialShaped is the predicate credentialShaped replaced, kept verbatim as the
// coverage floor: whatever it registered at 20 bytes or more must stay registered.
func legacyCredentialShaped(a string) bool {
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

// 🔴 COVERAGE NEVER SHRINKS: every long value the earlier predicate registered is still
// registered. The only exemptions are a path (leading '/') and a URL, whose password is
// registered on its own. The plain-name exemption is for short values only.
func TestCredentialShapedKeepsLegacyCoverage(t *testing.T) {
	corpus := []string{
		"correcthorsebatterystaple_1234", "correct_horse_battery_staple_1234", "passphrase1234567890abcd",
		"AKIA1234567890ABCDEF", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY", "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"abcdefghijklmnopqrstuvwxyz12", "ABCDEFGHIJKLMNOPQRSTUVWXYZ12", "dGhpcyBpcyBhIHRva2VuIHZhbHVl+/==",
		"Tok3n_Tok3n_Tok3n_Tok3n", "aaaaaaaaaaaaaaaaaaaa1", "1aaaaaaaaaaaaaaaaaaaa", "UPPERCASEPASSPHRASE_9876",
		// short: the 20-byte floor applies
		"correcthorse_1234", "AKIA1234567890", "1.27.3",
		// documented exemptions: paths and URLs
		"/usr/local/bin/helper-2024-aaaa", "/home/operator/.kube/aws-config1",
		"https://login.example.com/oauth2/v2.0/token", "http://alice:ArgProxyPW@proxy:3128",
		// plain names the earlier predicate also left alone
		"devicechain-production-namespace", "production-cluster-01", "kubernetes.default.svc.cluster",
	}
	for _, v := range corpus {
		if legacyCredentialShaped(v) && !credentialShaped(v) {
			t.Errorf("%q was registered before and is no longer", v)
		}
	}
	for _, v := range []string{"correcthorsebatterystaple_1234", "AKIA1234567890ABCDEF"} {
		if !credentialShaped(v) {
			t.Errorf("%q must be registered", v)
		}
	}
	for _, v := range []string{"1.27.3", "eu.gcr.io", "localhost:8080", "correcthorse_1234"} {
		if credentialShaped(v) {
			t.Errorf("short ordinary value %q must stay visible", v)
		}
	}
	if got := execArgSecrets([]string{"correcthorsebatterystaple_1234", "--session=correcthorsebatterystaple_5678"}); len(got) != 2 {
		t.Errorf("positional and inline long values: got %q", got)
	}
}

func TestExecOpaqueArgumentsAndURLPasswords(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl"
	got := execArgSecrets([]string{"http://alice:ArgProxyPW@proxy:3128", "--endpoint=http://alice:InlineProxyPW@proxy:3128",
		"--session=" + jwt, "--auth-mode", "default", "--pass=Pw4short", "--cluster=eu.gcr.io"})
	want := []string{"ArgProxyPW", "InlineProxyPW", jwt, "Pw4short"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %q, want %q", got, want)
	}
}
