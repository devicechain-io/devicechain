// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

func TestStatedValueIgnoresBlockComments(t *testing.T) {
	t.Setenv("TF_VAR_nats_jetstream_storage", "")
	for _, tc := range []struct {
		name, source, want string
		stated, readable   bool
	}{
		{"after active value", "nats_jetstream_storage = \"64Gi\"\n/*\nnats_jetstream_storage = \"1Gi\"\n*/\n", "64Gi", true, true},
		{"before active value", "/*\nnats_jetstream_storage = \"1Gi\"\n*/\nnats_jetstream_storage = \"64Gi\"\n", "64Gi", true, true},
		{"separate spans", "/* old size */\nnats_jetstream_storage = \"64Gi\"\n/*\nnats_jetstream_storage = \"1Gi\"\n*/\n", "64Gi", true, true},
		{"comment only", "/*\nnats_jetstream_storage = \"1Gi\"\n*/\n", "", false, false},
		{"quoted markers", "nats_jetstream_storage = \"64/*keep*/Gi\"\n", "64/*keep*/Gi", true, true},
		{"escaped quote", "other = \"escaped \\\" /*\"\nnats_jetstream_storage = \"64Gi\"\n/* old size */\n", "64Gi", true, true},
		{"line comment markers", "# \" /*\n// \" /*\nnats_jetstream_storage = \"64Gi\"\n/* old size */\n", "64Gi", true, true},
		{"expression stays unreadable", "nats_jetstream_storage = var.storage\n/*\nnats_jetstream_storage = \"1Gi\"\n*/\n", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeStated(t, map[string]string{"terraform.tfvars": tc.source})
			value, stated, readable := statedValueIn(dir, "nats_jetstream_storage")
			if value != tc.want || stated != tc.stated || readable != tc.readable {
				t.Fatalf("statedValueIn = (%q, %v, %v); want (%q, %v, %v)", value, stated, readable, tc.want, tc.stated, tc.readable)
			}
		})
	}
}
