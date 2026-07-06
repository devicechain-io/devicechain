// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// newVerifier wires a DeviceVerifier against a stub mint endpoint and a stub
// device-management whose devicesByToken echoes a device iff the queried token is
// "known".
func newVerifier(t *testing.T) (*DeviceVerifier, *string) {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)

	var gotTenant string
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(auth.ServiceTenantHeader)
		var body struct {
			Variables struct {
				Tokens []string `json:"tokens"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		devices := []map[string]string{}
		if len(body.Variables.Tokens) == 1 && body.Variables.Tokens[0] == "known" {
			devices = append(devices, map[string]string{"token": "known"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"devicesByToken": devices}})
	}))
	t.Cleanup(dm.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "command-delivery", []string{string(auth.DeviceRead)})
	return NewDeviceVerifier(client, dm.URL), &gotTenant
}

func TestDeviceExists(t *testing.T) {
	v, gotTenant := newVerifier(t)
	ctx := core.WithTenant(context.Background(), "tenant-a")

	exists, err := v.DeviceExists(ctx, "known")
	if err != nil {
		t.Fatalf("DeviceExists(known): %v", err)
	}
	if !exists {
		t.Fatal("expected a known device to exist")
	}
	if *gotTenant != "tenant-a" {
		t.Fatalf("device-management did not receive the tenant header: %q", *gotTenant)
	}

	missing, err := v.DeviceExists(ctx, "missing")
	if err != nil {
		t.Fatalf("DeviceExists(missing): %v", err)
	}
	if missing {
		t.Fatal("expected an unknown device to be absent")
	}
}

// Without a tenant in context the verifier fails rather than issuing a tenantless
// service call.
func TestDeviceExists_NoTenant(t *testing.T) {
	v, _ := newVerifier(t)
	if _, err := v.DeviceExists(context.Background(), "known"); err == nil {
		t.Fatal("expected an error when no tenant is in context")
	}
}
