// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// A binding with no recorded cluster name describes itself as "(context X)"; the
// announcement must not append the context a second time.
func TestRecordedWhereNamesTheContextOnce(t *testing.T) {
	got := recordedWhere(ClusterBinding{KubeContext: "kind-x"})
	if n := strings.Count(got, "context kind-x"); n != 1 {
		t.Errorf("context named %d times in %q", n, got)
	}
	got = recordedWhere(ClusterBinding{Cluster: "c", KubeContext: "kind-c"})
	if got != "cluster c (context kind-c)" {
		t.Errorf("got %q", got)
	}
}

// And the announcement itself says it once: recordedWhere is worth nothing unless
// announceBinding is what prints it.
func TestTheRecordedBindingAnnouncementNamesTheContextOnce(t *testing.T) {
	out := captureStdout(t, func() {
		announceBinding(ClusterBinding{KubeContext: "kind-x"}, BindingFromRecord, "guide")
	})
	if n := strings.Count(out, "kind-x"); n != 1 {
		t.Errorf("context named %d times in %q", n, out)
	}
	out = captureStdout(t, func() {
		announceBinding(ClusterBinding{Cluster: "c", KubeContext: "kind-c"}, BindingFromRecord, "guide")
	})
	if !strings.Contains(out, "cluster c (context kind-c)") || strings.Count(out, "kind-c") != 1 {
		t.Errorf("announcement %q does not name cluster c and context kind-c once each", out)
	}
}
