// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// mkfifo makes a FIFO nobody writes to, and lets go of any reader blocked on it when
// the test ends.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

// 🔴 FINAL 3: a credential file that is not a regular file (a FIFO no one writes, a
// device) is never opened: reading it can block for ever, past every bound and the
// screen's Exit. The run says so, NOT CHECKED, at once.
func TestACredentialFileThatIsNotRegularIsNotRead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string) string // returns KUBECONFIG
	}{
		{"token file", func(t *testing.T, dir string) string {
			fifo := filepath.Join(dir, "token")
			mkfifo(t, fifo)
			kc := filepath.Join(dir, "config")
			writeCloudKubeconfig(t, kc, "    tokenFile: "+fifo+"\n")
			return kc
		}},
		{"client key", func(t *testing.T, dir string) string {
			fifo := filepath.Join(dir, "key")
			mkfifo(t, fifo)
			cert := filepath.Join(dir, "cert")
			if err := os.WriteFile(cert, []byte("not read"), 0o600); err != nil {
				t.Fatal(err)
			}
			kc := filepath.Join(dir, "config")
			writeCloudKubeconfig(t, kc, "    client-certificate: "+cert+"\n    client-key: "+fifo+"\n")
			return kc
		}},
		{"the kubeconfig itself", func(t *testing.T, dir string) string {
			fifo := filepath.Join(dir, "config")
			mkfifo(t, fifo)
			return fifo
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", tc.setup(t, t.TempDir()))
			var rows []installflow.PreflightObservation
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = cloudDiagnosticsRunner(prodTarget, liveCloudProbes).RunPreflight(context.Background(),
					func(o installflow.PreflightObservation) {
						if o.Observation == installflow.ObserveCheck {
							rows = append(rows, o)
						}
					})
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("reading a FIFO held the run")
			}
			if len(rows) != 1 || rows[0].Verdict != installflow.CheckNotChecked ||
				!strings.Contains(rows[0].Text.String(), "not a regular file") {
				t.Fatalf("rows %+v", rows)
			}
		})
	}
}

func writeCloudKubeconfig(t *testing.T, path, user string) {
	t.Helper()
	doc := "apiVersion: v1\nkind: Config\ncurrent-context: prod\n" +
		"clusters:\n- name: c\n  cluster:\n    server: https://127.0.0.1:1\n" +
		"users:\n- name: u\n  user:\n" + user +
		"contexts:\n- name: prod\n  context:\n    cluster: c\n    user: u\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}
