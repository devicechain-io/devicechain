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
// env value that is an ordinary word is redacted from the text but never refuses a
// code that spells it; a three-byte password cannot be redacted at all, and the run
// warns that one value is not covered.
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
	for _, v := range []string{"tk5ab", "profile default"} {
		if strings.Contains(text, v) {
			t.Errorf("the stream carries %q", v)
		}
	}
	if !strings.Contains(text, "warning: 1 kubeconfig credential value(s) are shorter than 4 bytes") {
		t.Errorf("no warning that the 3-byte password is not covered:\n%s", text)
	}
}

// A text-only credential (an exec env value that is an ordinary word) is redacted
// from text but never refuses an event code that spells it; a strict one does.
func TestRegisterCredentialsTextOnlyNeverRefusesACode(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Sink: &testkit.CaptureSink{}, Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	if n := registerCredentials(hub, []credential{{value: "default", found: true}, {value: "s3cr3t-strict"}, {value: "ab"}}); n != 1 {
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
