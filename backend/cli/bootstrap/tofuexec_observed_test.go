// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/internal/tofujson"
)

func TestObservedEnvironmentMatchesTerraformExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX fake tofu")
	}
	testKube(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	script := `#!/bin/sh
case "$1" in
  version) echo '{"terraform_version":"1.8.0","platform":"linux_amd64"}' ;;
  apply) env -0 > "$0.env" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tf, err := newTofuExec(dir, bin, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ name, ua, checkpoint string }{
		{"unset", "", ""},
		{"custom", "  installer/1.0  ", "1"},
		{"deduplicated", "HashiCorp-terraform-exec/0.26.0", ""},
	} {
		t.Run(input.name, func(t *testing.T) {
			for key, value := range map[string]string{
				"TF_APPEND_USER_AGENT": input.ua, "CHECKPOINT_DISABLE": input.checkpoint,
				"TF_WORKSPACE": "ignored", "TF_LOG": "TRACE", "TF_LOG_CORE": "DEBUG",
				"TF_LOG_PATH": "ignored.log", "TF_LOG_PROVIDER": "INFO", "TF_IN_AUTOMATION": "0",
				"TF_VAR_example": "spaces = value", "AWS_REGION": "us-east-1",
			} {
				t.Setenv(key, value)
			}
			if input.name == "unset" {
				for _, key := range []string{"TF_APPEND_USER_AGENT", "CHECKPOINT_DISABLE"} {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			readEnv := func() []string {
				t.Helper()
				raw, err := os.ReadFile(bin + ".env")
				if err != nil {
					t.Fatal(err)
				}
				env := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
				slices.Sort(env)
				return env
			}
			if err := tf.Terraform.Apply(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := readEnv()
			if err := tf.observedApply(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			if got := readEnv(); !reflect.DeepEqual(got, want) {
				// Report only differing entries, keeping provider inputs out of logs.
				for _, entry := range want {
					if !slices.Contains(got, entry) {
						t.Errorf("observed environment missing terraform-exec entry %q", entry)
					}
				}
				for _, entry := range got {
					if !slices.Contains(want, entry) {
						t.Errorf("observed environment has extra entry %q", entry)
					}
				}
			}
		})
	}
}

func TestDiagnosticBudgetAlwaysKeepsCNPGRetryEvidence(t *testing.T) {
	for _, size := range []int{16, 16 << 10, tofujson.MaxLine + 1} {
		o := tofujson.New(nil, nil, nil)
		line := []byte(`{"type":"diagnostic","diagnostic":{"severity":"error","detail":"` + strings.Repeat("x", size) + `"}}` + "\n")
		for range 40 {
			_, _ = o.Write(line)
		}
		for _, name := range []string{"mcluster.cnpg.io", "vcluster.cnpg.io"} {
			marker := `failed\n calling webhook \"` + name + `\"`
			late := bytes.Replace(line, []byte(strings.Repeat("x", size)), []byte(strings.Repeat("x", size)+marker), 1)
			_, _ = o.Write(late)
			if !isCNPGWebhookUnavailable(o.Errors()) || !strings.Contains(o.Errors(), name) {
				t.Errorf("size %d: capped diagnostics lost retry evidence for %s", size, name)
			}
		}
		o.Close()
		if !strings.Contains(o.Errors(), "mcluster.cnpg.io") || !strings.Contains(o.Errors(), "vcluster.cnpg.io") {
			t.Errorf("size %d: a later retry diagnostic displaced an earlier one", size)
		}
	}
}
