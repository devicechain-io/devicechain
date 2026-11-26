// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofujson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestErrorDiagnosticsHaveAnAggregateBudget(t *testing.T) {
	for _, size := range []int{16, 16 << 10, MaxLine + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var output bytes.Buffer
			o := New(nil, &output, nil)
			for i := range 40 {
				line, err := json.Marshal(map[string]any{"type": "diagnostic", "diagnostic": map[string]string{
					"severity": "error", "summary": fmt.Sprintf("failure-%02d", i), "detail": strings.Repeat("x", size),
				}})
				if err != nil {
					t.Fatal(err)
				}
				_, _ = o.Write(append(line, '\n'))
			}
			o.Close()
			retainedBytes := 0
			for _, text := range o.errs {
				retainedBytes += len(text)
			}
			if len(o.errs) > 32 || retainedBytes > 64<<10 {
				t.Errorf("retained %d diagnostics / %d bytes; want at most 32 / 65536", len(o.errs), retainedBytes)
			}
			got := o.Errors()
			if !strings.HasPrefix(got, "Error: failure-00") {
				t.Error("lost the first diagnostic")
			}
			if want := fmt.Sprintf("%d more diagnostics omitted", 40-len(o.errs)); !strings.HasSuffix(got, want) {
				t.Errorf("missing final omission count %q", want)
			}
			if o.Errors() != got {
				t.Error("Errors changed the omission count")
			}
			if !strings.Contains(output.String(), "failure-39") {
				t.Error("the retention cap hid a diagnostic from the output writer")
			}
		})
	}
}

func TestErrorBudgetKeepsEveryRetryDiagnostic(t *testing.T) {
	for _, size := range []int{16, 16 << 10, MaxLine + 1} {
		o := New(nil, nil, nil)
		padding := strings.Repeat("x", size)
		line := `{"type":"diagnostic","diagnostic":{"severity":"error","detail":"` + padding + `"}}` + "\n"
		for range 40 {
			_, _ = o.Write([]byte(line))
		}
		for _, name := range []string{"mcluster.cnpg.io", "vcluster.cnpg.io"} {
			marker := `failed\n calling webhook \"` + name + `\"`
			_, _ = o.Write([]byte(strings.Replace(line, padding, padding+marker, 1)))
		}
		o.Close()
		for _, name := range []string{"mcluster.cnpg.io", "vcluster.cnpg.io"} {
			if !strings.Contains(o.Errors(), name) {
				t.Errorf("size %d: lost capped retry diagnostic for %s", size, name)
			}
		}
	}
}
