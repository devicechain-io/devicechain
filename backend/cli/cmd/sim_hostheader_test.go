// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func hostHeaderCmd(t *testing.T, flag string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "x"}
	c.Flags().String("host-header", "", "")
	c.Flags().String("kube-context", "", "")
	if flag != "" {
		if err := c.Flags().Set("host-header", flag); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func stubRecordedHost(t *testing.T, host string, err error) *int {
	t.Helper()
	orig := readRecordedHost
	calls := 0
	readRecordedHost = func(context.Context, string, string) (string, error) { calls++; return host, err }
	t.Cleanup(func() { readRecordedHost = orig })
	return &calls
}

// The r27 case: bootstrap with no --host records devicechain.local, and sim create is
// pointed at the load balancer's address.
func TestResolveHostHeaderUsesTheInstancesRecordedHostForAnAddress(t *testing.T) {
	stubRecordedHost(t, "devicechain.local", nil)
	if got := resolveHostHeader(hostHeaderCmd(t, ""), "34.85.231.141", "rc"); got != "devicechain.local" {
		t.Errorf("got %q, want devicechain.local", got)
	}
}

func TestResolveHostHeaderLeavesADNSServerAloneWithoutReadingTheCluster(t *testing.T) {
	calls := stubRecordedHost(t, "devicechain.local", nil)
	if got := resolveHostHeader(hostHeaderCmd(t, ""), "rc.example.io", "rc"); got != "" {
		t.Errorf("got %q, want none", got)
	}
	if *calls != 0 {
		t.Errorf("the cluster was read %d times for a server that needs no header", *calls)
	}
}

func TestResolveHostHeaderFlagWinsAndSkipsTheClusterRead(t *testing.T) {
	calls := stubRecordedHost(t, "devicechain.local", nil)
	if got := resolveHostHeader(hostHeaderCmd(t, "rc.example.io"), "34.85.231.141", "rc"); got != "rc.example.io" {
		t.Errorf("got %q, want rc.example.io", got)
	}
	if *calls != 0 {
		t.Errorf("the cluster was read despite an explicit --host-header")
	}
}

func TestResolveHostHeaderUnreadableDeclarationFallsBackWithoutAHeader(t *testing.T) {
	stubRecordedHost(t, "", errors.New("boom"))
	if got := resolveHostHeader(hostHeaderCmd(t, ""), "34.85.231.141", "rc"); got != "" {
		t.Errorf("got %q, want none", got)
	}
}

func TestWithHostHintNamesTheFixOnlyWhereItApplies(t *testing.T) {
	base := errors.New("admin login failed: 404")
	if err := withHostHint(base, "34.85.231.141", ""); !strings.Contains(err.Error(), "--host-header") || !errors.Is(err, base) {
		t.Errorf("an address with no header should be told the fix and keep the cause: %v", err)
	}
	if err := withHostHint(base, "34.85.231.141", "devicechain.local"); err != base {
		t.Errorf("a header was sent, so the failure is something else: %v", err)
	}
	if err := withHostHint(base, "rc.example.io", ""); err != base {
		t.Errorf("a DNS server is not the Host-header case: %v", err)
	}
	if err := withHostHint(nil, "34.85.231.141", ""); err != nil {
		t.Errorf("nil must stay nil: %v", err)
	}
}
