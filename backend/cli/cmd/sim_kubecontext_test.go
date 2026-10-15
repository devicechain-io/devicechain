// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dcctl/bootstrap"
)

// `dcctl sim` reads the superuser password through the same binding upgrade and destroy
// resolve, so a --kube-context naming another cluster than the recorded one is refused
// there too, in the same words, before any cluster is read.
func TestSimRefusesAKubeContextThatConflictsWithTheRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := bootstrap.WriteInstanceRecord(bootstrap.InstanceRecord{Instance: "guide", Provider: "local",
		Cluster: "devicechain", KubeContext: "kind-devicechain", Managed: true}); err != nil {
		t.Fatal(err)
	}
	_, err := readSuperuserPassword(context.Background(), "kind-elsewhere", "guide")
	if err == nil {
		t.Fatal("a conflicting --kube-context was not refused")
	}
	for _, want := range []string{"different cluster", "kind-devicechain", "re-run with --kube-context kind-elsewhere"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q: %v", want, err)
		}
	}
}
