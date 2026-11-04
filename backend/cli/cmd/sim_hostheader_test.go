// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dcctl/sim"
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

// aHostRecordingGateway answers every request as a permissive admin surface and records
// the Host each arrived with, so a command can be driven end to end against it.
func aHostRecordingGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"data":{"login":{"identityToken":"t","expiresAt":"2099-01-01T00:00:00Z","superuser":true}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

func isolatedHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(adminPasswordEnv, "pw")
}

// The r27 case end to end: --server is the load balancer's address, the instance was
// bootstrapped without --host, and the Gateway only routes devicechain.local.
func TestSimCreatePresentsTheRecordedHostAndSavesIt(t *testing.T) {
	isolatedHome(t)
	stubRecordedHost(t, "devicechain.local", nil)
	srv, hosts := aHostRecordingGateway(t)

	c := freshSimCommand(t, simCreateCmd, "--server", strings.TrimPrefix(srv.URL, "http://"), "--instance", "acme")
	if err := runSimCreate(c, []string{"probe"}); err != nil {
		t.Fatal(err)
	}
	got := hosts()
	if len(got) == 0 {
		t.Fatal("the gateway saw no request")
	}
	for _, h := range got {
		if h != "devicechain.local" {
			t.Fatalf("a request arrived with Host %q, want devicechain.local (all: %v)", h, got)
		}
	}
	rec, err := sim.Load("probe")
	if err != nil {
		t.Fatal(err)
	}
	if rec.HostHeader != "devicechain.local" {
		t.Errorf("saved record HostHeader = %q", rec.HostHeader)
	}
}

// Control: the same command without a recorded host sends the address, as dcctl always
// did, and saves no header.
func TestSimCreateWithNothingRecordedSendsTheAddress(t *testing.T) {
	isolatedHome(t)
	stubRecordedHost(t, "", nil)
	srv, hosts := aHostRecordingGateway(t)

	c := freshSimCommand(t, simCreateCmd, "--server", strings.TrimPrefix(srv.URL, "http://"), "--instance", "acme")
	if err := runSimCreate(c, []string{"probe"}); err != nil {
		t.Fatal(err)
	}
	if h := hosts()[0]; !strings.HasPrefix(h, "127.0.0.1:") {
		t.Errorf("Host = %q, want the dialled address", h)
	}
	if rec, _ := sim.Load("probe"); rec == nil || rec.HostHeader != "" {
		t.Errorf("record = %+v, want no host header", rec)
	}
}

func TestSimDestroyReplaysTheRecordedHostWithNoFlag(t *testing.T) {
	isolatedHome(t)
	srv, hosts := aHostRecordingGateway(t)
	if err := sim.Save(&sim.Record{
		Name: "probe", Tenant: "sim-probe", InstanceId: "acme", HostHeader: "devicechain.local",
		Endpoints: sim.Endpoints{UserGraphQL: srv.URL + "/api/user-management/graphql"},
		AdminURL:  srv.URL + "/admin/graphql", ControlAddr: srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	c := freshSimCommand(t, simDestroyCmd)
	_ = runSimDestroy(c, []string{"probe"}) // the permissive fake need not satisfy every teardown step
	got := hosts()
	if len(got) == 0 {
		t.Fatal("the gateway saw no request")
	}
	if got[0] != "devicechain.local" {
		t.Errorf("destroy's first request had Host %q, want the record's devicechain.local", got[0])
	}
}

func TestWithHostHintNamesTheFixOnlyWhereItApplies(t *testing.T) {
	base := fmt.Errorf("admin login failed: %w", &userclient.StatusError{URL: "u", StatusCode: 404})
	if err := withHostHint(base, "34.85.231.141", ""); !strings.Contains(err.Error(), "--host-header") || !errors.Is(err, base) {
		t.Errorf("an address with no header should be told the fix and keep the cause: %v", err)
	}
	if err := withHostHint(base, "34.85.231.141", "devicechain.local"); err != base {
		t.Errorf("a header was sent, so the failure is something else: %v", err)
	}
	if err := withHostHint(base, "rc.example.io", ""); err != base {
		t.Errorf("a DNS server is not the Host-header case: %v", err)
	}
	unauth := fmt.Errorf("admin login failed: %w", &userclient.StatusError{URL: "u", StatusCode: 401})
	if err := withHostHint(unauth, "34.85.231.141", ""); err != unauth {
		t.Errorf("only a 404 is the hostname case: %v", err)
	}
	if err := withHostHint(nil, "34.85.231.141", ""); err != nil {
		t.Errorf("nil must stay nil: %v", err)
	}
}
