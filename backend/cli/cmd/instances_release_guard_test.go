// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// `dcctl instances release` asks the operator guard BEFORE its prompt: a dcctl that does not match
// the cluster's operator is refused without being asked to confirm anything, and nothing is released.
func TestReleaseAsksTheOperatorGuardBeforeAnythingElse(t *testing.T) {
	orig := requireOperatorForRelease
	t.Cleanup(func() { requireOperatorForRelease = orig })
	refusal := errors.New("this cluster's DeviceChain operator is not the one this dcctl expects")
	asked := false
	requireOperatorForRelease = func(_ context.Context, kubeContext, verb, _ string) error {
		asked = true
		if kubeContext != "kind-test" || !strings.Contains(verb, "release") {
			t.Errorf("the guard was asked about %q / %q", kubeContext, verb)
		}
		return refusal
	}
	var out bytes.Buffer
	err := runRelease(t.Context(), &out, strings.NewReader("acme\n"), "kind-test", "acme")
	if !errors.Is(err, refusal) || !asked {
		t.Fatalf("release went on past a refused operator check (asked %v, err %v)", asked, err)
	}
	if strings.Contains(out.String(), "Type the instance name") {
		t.Fatal("the release prompted before the operator check refused")
	}
}
