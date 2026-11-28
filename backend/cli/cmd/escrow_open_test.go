// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets/escrow"
	"github.com/devicechain-io/dcctl/internal/secretenv"
	corev1 "k8s.io/api/core/v1"
)

func TestEscrowVerifyOffersOptInOpen(t *testing.T) {
	flag := secretsEscrowVerifyCmd.Flags().Lookup("open")
	if flag == nil || flag.DefValue != "false" {
		t.Fatal("verify needs an opt-in --open flag; header-only verification must remain the default")
	}
	if secretsEscrowVerifyCmd.Flags().Lookup("escrow-passphrase-file") == nil {
		t.Fatal("verify --open needs --escrow-passphrase-file")
	}
}

// Exercise the real command against a local Kubernetes API, including the opt-in
// switch and the captured environment, without a cluster or a root-key lookup seam.
func TestEscrowVerifyOpenAuthenticatesAndComparesTheKey(t *testing.T) {
	key := bytes.Repeat([]byte{42}, escrow.RootKeySize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/secrets/dci-prod-config") {
			t.Errorf("unexpected API request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.Secret{Data: map[string][]byte{
			"instance": []byte(fmt.Sprintf(`{"infrastructure":{"secrets":{"rootKey":%q}}}`, base64.StdEncoding.EncodeToString(key))),
		}})
	}))
	defer server.Close()
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\nusers:\n- name: test\n  user: {}\n", server.URL)
	if err := os.WriteFile(kc, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kc)

	for _, tc := range []struct {
		name    string
		open    bool
		pass    string
		file    bool
		damage  bool
		other   bool
		wantErr bool
	}{
		{name: "default still checks only the header", damage: true},
		{name: "captured passphrase opens", open: true, pass: "original"},
		{name: "file takes precedence and trims newlines", open: true, pass: "original", file: true},
		{name: "wrong passphrase", open: true, pass: "wrong", wantErr: true},
		{name: "no passphrase", open: true, wantErr: true},
		{name: "damaged ciphertext", open: true, pass: "original", damage: true, wantErr: true},
		{name: "different running key", open: true, pass: "original", other: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secretenv.Forget()
			t.Cleanup(secretenv.Forget)
			t.Setenv(secretenv.EscrowPassphrase, tc.pass)
			if tc.pass == "" {
				_ = os.Unsetenv(secretenv.EscrowPassphrase)
			}
			if tc.file {
				t.Setenv(secretenv.EscrowPassphrase, "wrong-env-passphrase")
			}
			secretenv.Capture()
			artifactKey := key
			if tc.other {
				artifactKey = bytes.Repeat([]byte{7}, escrow.RootKeySize)
			}
			art, err := escrow.Wrap(artifactKey, "original", "prod", escrow.DefaultKDFParams(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			raw, err := art.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if tc.damage {
				const begin = "-----BEGIN DEVICECHAIN ROOT KEY ESCROW-----"
				const end = "-----END DEVICECHAIN ROOT KEY ESCROW-----"
				body := strings.Split(strings.Split(string(raw), begin)[1], end)[0]
				decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Header     json.RawMessage `json:"hdr"`
					Ciphertext []byte          `json:"ct"`
				}
				if err := json.Unmarshal(decoded, &doc); err != nil {
					t.Fatal(err)
				}
				doc.Ciphertext[0] ^= 1
				decoded, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(begin + "\n" + base64.StdEncoding.EncodeToString(decoded) + "\n" + end + "\n")
			}
			path := filepath.Join(t.TempDir(), "prod.escrow")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := secretsEscrowVerifyCmd
			cmd.SetContext(t.Context())
			flags := map[string]string{"instance": "prod", "kube-context": "test", "open": fmt.Sprint(tc.open), "escrow-passphrase-file": ""}
			if tc.file {
				passfile := filepath.Join(t.TempDir(), "pass")
				if err := os.WriteFile(passfile, []byte(tc.pass+"\r\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				flags["escrow-passphrase-file"] = passfile
			}
			for name, value := range flags {
				flag := cmd.Flags().Lookup(name)
				if flag == nil {
					t.Fatalf("missing --%s", name)
				}
				old, changed := flag.Value.String(), flag.Changed
				t.Cleanup(func() {
					_ = flag.Value.Set(old)
					flag.Changed = changed
				})
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.RunE(cmd, []string{path}); (err != nil) != tc.wantErr {
				t.Fatalf("verify --open=%t: %v, want error=%t", tc.open, err, tc.wantErr)
			}
		})
	}
}

func TestEscrowVerifyOpenRefusesBrokenPassphraseFiles(t *testing.T) {
	secretenv.Forget()
	t.Cleanup(secretenv.Forget)
	t.Setenv(secretenv.EscrowPassphrase, "environment-must-not-override-file")
	secretenv.Capture()
	for _, present := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "pass")
		if present {
			if err := os.WriteFile(path, []byte("\r\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := escrowVerificationPassphrase(path); err == nil {
			t.Fatalf("broken passphrase file (present=%t) fell back to environment", present)
		}
	}
}
