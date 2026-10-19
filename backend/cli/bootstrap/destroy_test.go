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
