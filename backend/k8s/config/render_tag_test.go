// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// 🔴 AN IMAGE TAG IS A STRING, AND YAML DOES NOT AGREE UNLESS IT IS TOLD.
//
// The manager kustomization is written as YAML and kustomize unmarshals newTag into
// a Go string, so a tag that YAML reads as another scalar type never reaches the
// renderer: `1.0` and `20260918` are NUMBERS, `y`/`no`/`on`/`true` are BOOLEANS.
// Docker tags may begin with a digit, so this covers every date-stamped release and
// every unprefixed semver anyone might publish — `dcctl install --version 1.0` is
// an ordinary thing to type.
//
// What made it worth a test rather than a fix alone is the failure mode. It is not
// a clear refusal: it is "cannot unmarshal number into Go struct field
// Image.images.newTag of type string", wrapped in a kustomize accumulation error
// naming a path the operator never typed.
func TestATagYamlWouldReadAsSomethingElseStillRenders(t *testing.T) {
	for _, tag := range []string{"v0.17.0", "dev", "1.0", "20260918", "2", "y", "no", "on", "true", "0755"} {
		ref := "ghcr.io/devicechain-io/operator:" + tag
		manifests, err := RenderOperator(ref)
		if err != nil {
			t.Errorf("an operator at tag %q does not render: %v", tag, err)
			continue
		}
		if !strings.Contains(string(manifests), ref) {
			t.Errorf("the overlay rendered at tag %q does not name %q, so the cluster would "+
				"pull an image nobody asked for", tag, ref)
		}
	}
}

// The counterweight: a registry with a port still splits at the right colon. Quoting
// the values must not change which part is the tag.
func TestQuotingDidNotMoveTheTagSeparator(t *testing.T) {
	manifests, err := RenderOperator("localhost:5000/operator:dev")
	if err != nil {
		t.Fatalf("rendering a port-bearing registry: %v", err)
	}
	if !strings.Contains(string(manifests), "localhost:5000/operator:dev") {
		t.Fatal("the rendered overlay does not name localhost:5000/operator:dev")
	}
}
