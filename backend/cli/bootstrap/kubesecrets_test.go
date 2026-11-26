// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
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
		if !IsKubeconfigUnreadable(err) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v, want a KubeconfigUnreadableError naming %s", name, err, path)
		}
		for _, c := range []string{kcToken, kcTokenDup, kcPassword, kcKeyCore, kcExecEnv, kcIDToken, "golden"} {
			if strings.Contains(err.Error(), c) {
				t.Errorf("%s's error quotes the file (%q): %v", name, c, err)
			}
		}
	}
}

// The kubeconfig's credentials are found even in a file client-go refuses, and an
// incidental setting too short to be a credential is left alone.
func TestKubeconfigSecretsFindsEveryCredential(t *testing.T) {
	writeCanaryKubeconfig(t)
	got := map[string]bool{}
	for _, v := range kubeconfigSecrets(clientcmd.NewDefaultClientConfigLoadingRules()) {
		got[v] = true
	}
	for _, want := range []string{kcToken, kcTokenDup, kcPassword, kcKeyPEM, kcExecEnv, kcIDToken} {
		if !got[want] {
			t.Errorf("%q was not found", want)
		}
	}
	if got[kcShortEnv] {
		t.Errorf("%q, an exec env value too short to be a credential, would be redacted from every line", kcShortEnv)
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
