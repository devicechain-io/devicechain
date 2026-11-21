// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strings"
	"testing"
)

func TestExecutorFoundationIsRenderedAsCRDsOnlyByDefault(t *testing.T) {
	b, err := RenderOperator("")
	if err != nil {
		t.Fatal(err)
	}
	objects, err := decodeAnnotated(b)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, o := range objects {
		if strings.HasPrefix(o.name, "dc-executor") || o.kind == "ExecutorEnrollment" {
			t.Fatalf("executor installed by default: %s/%s", o.kind, o.name)
		}
		if o.kind == "CustomResourceDefinition" {
			found[o.name] = true
		}
	}
	for _, name := range []string{"executorenrollments.core.devicechain.io", "instanceoperations.core.devicechain.io"} {
		if !found[name] {
			t.Errorf("missing foundation CRD %s", name)
		}
	}
}

func TestExecutorOverlayExists(t *testing.T) {
	if _, err := os.ReadFile("executor/kustomization.yaml"); err != nil {
		t.Fatal(err)
	}
}
