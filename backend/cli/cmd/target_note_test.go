// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dcctl/bootstrap"
)

// When no record says which cluster an instance lives on, --instance still works, but it must
// SAY it is guessing and name the context: the host it reads is only as right as that guess. A
// recorded binding is silent.
func TestAGuessedBindingIsAnnouncedOnStderr(t *testing.T) {
	origResolve, origRead, origOut := resolveTargetBinding, readTargetInstanceCR, targetNoteOut
	t.Cleanup(func() { resolveTargetBinding, readTargetInstanceCR, targetNoteOut = origResolve, origRead, origOut })

	var readFrom string
	readTargetInstanceCR = func(_ context.Context, kubeContext, _ string) (*dcv1beta1.Instance, error) {
		readFrom = kubeContext
		inst := &dcv1beta1.Instance{}
		inst.Spec.Host = "dc.example.com"
		inst.Spec.TLS = true
		return inst, nil
	}
	run := func(source bootstrap.BindingSource) (string, string) {
		resolveTargetBinding = func(bootstrap.Options) (bootstrap.ClusterBinding, bootstrap.BindingSource) {
			return bootstrap.ClusterBinding{KubeContext: "kind-prod"}, source
		}
		var out bytes.Buffer
		targetNoteOut = &out
		host, tls, err := readRecordedTarget(context.Background(), "", "prod")
		if err != nil || host != "dc.example.com" || !tls {
			t.Fatalf("got %q %v %v", host, tls, err)
		}
		return out.String(), readFrom
	}

	note, from := run(bootstrap.BindingGuessed)
	if !strings.Contains(note, "guessing context kind-prod") || !strings.Contains(note, `"prod"`) {
		t.Fatalf("a guessed binding was not announced: %q", note)
	}
	if from != "kind-prod" {
		t.Fatalf("the declaration was read from %q, not the guessed context", from)
	}
	if note, _ := run(bootstrap.BindingFromRecord); note != "" {
		t.Fatalf("a recorded binding printed a note: %q", note)
	}
}
