// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tofujson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestOversizedDiagnosticsAreBoundedAndKeepFraming(t *testing.T) {
	marker := `failed calling webhook \"mcluster.cnpg.io\"`
	for _, chunk := range []int{1, 7, 4096, MaxLine * 2} {
		stream := `{"type":"diagnostic","diagnostic":{"severity":"error","summary":"failed","detail":"` +
			strings.Repeat("x", MaxLine+1) + marker + `"}}` + "\n" +
			`{"type":"apply_complete","hook":{"action":"create"},"@message":"next"}`
		_, o, human, diags := observe(t, []byte(stream), chunk)
		if o.Oversized != 1 || o.Progress().Done != 1 || human != "next\n" {
			t.Errorf("chunk %d lost framing: oversized=%d progress=%+v human=%q", chunk, o.Oversized, o.Progress(), human)
		}
		if !strings.Contains(o.Errors(), `failed calling webhook "mcluster.cnpg.io"`) ||
			!strings.Contains(diags, "diagnostic truncated") || len(diags) > MaxLine {
			t.Errorf("chunk %d lost or failed to bound the diagnostic", chunk)
		}
	}
}

func TestOversizedWarningAndUnrelatedFieldsDoNotBecomeApplyErrors(t *testing.T) {
	padding := strings.Repeat("x", MaxLine+1)
	for _, stream := range []string{
		`{"type":"diagnostic","diagnostic":{"severity":"warning","detail":"` + padding + `failed calling webhook \"mcluster.cnpg.io\""}}`,
		`{"type":"apply_complete","@message":"` + padding + `failed calling webhook \"mcluster.cnpg.io\""}`,
		`{"type":"diagnostic","diagnostic":{"severity":"error","detail":"unrelated"},"resource":{"detail":"` + padding + `failed calling webhook \"mcluster.cnpg.io\""}}`,
	} {
		_, o, _, _ := observe(t, []byte(stream), 3)
		if strings.Contains(o.Errors(), "failed calling webhook") {
			t.Fatalf("a warning, non-diagnostic or unrelated field became retry evidence")
		}
	}
}

func TestUnterminatedLineStorageStaysBounded(t *testing.T) {
	o := New(nil, nil, nil)
	chunk := bytes.Repeat([]byte("z"), 4096)
	for range 4096 {
		_, _ = o.Write(chunk)
		if len(o.pending) > MaxLine || cap(o.pending) > 2*MaxLine {
			t.Fatal("line storage grew past its bound")
		}
	}
	if o.Oversized != 1 {
		t.Fatalf("got %d oversized lines, want one", o.Oversized)
	}
	o.Close()
}

func TestDeepUnknownFieldsDoNotHideOversizedDiagnostic(t *testing.T) {
	stream := `{"extra":` + strings.Repeat("[", 128) + `"` + strings.Repeat("x", MaxLine+1) +
		`"` + strings.Repeat("]", 128) + `,"type":"diagnostic","diagnostic":{"severity":"error",` +
		`"detail":"failed calling webhook \"mcluster.cnpg.io\""}}`
	if !json.Valid([]byte(stream)) {
		t.Fatal("invalid fixture")
	}
	_, o, _, _ := observe(t, []byte(stream), 7)
	if !strings.Contains(o.Errors(), `failed calling webhook "mcluster.cnpg.io"`) {
		t.Fatal("an unknown nested value hid the diagnostic")
	}
}
