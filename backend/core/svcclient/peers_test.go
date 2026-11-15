// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
)

func TestPeers(t *testing.T) {
	infra := config.InfrastructureConfiguration{}
	p, err := NewPeers(infra, "dashboard-management")
	if err != nil {
		t.Fatal(err)
	}
	missing := func(err error, key string) {
		t.Helper()
		var m MissingError
		if !errors.Is(err, ErrPeerNotConfigured) || !errors.As(err, &m) || m.Key != key {
			t.Fatalf("got %v, want MissingError %s", err, key)
		}
	}
	missing(p.Ready(), "infrastructure.serviceAuth.secret")
	_, err = p.UserManagement(auth.TenantRead)
	missing(err, "infrastructure.serviceAuth.secret")
	infra.ServiceAuth.Secret = "secret"
	p, _ = NewPeers(infra, "dashboard-management")
	missing(p.Ready(), "infrastructure.userManagement")
	_, err = p.UserManagement(auth.TenantRead)
	missing(err, "infrastructure.userManagement")
	infra.UserManagement = config.UserManagementConfiguration{Hostname: "um", Port: 8080, InternalPort: 8082}
	p, _ = NewPeers(infra, "dashboard-management")
	_, err = p.Seam(infra.DeviceManagement.Endpoint(), auth.DeviceRead)
	missing(err, "infrastructure.deviceManagement")
	if err := p.Ready(); err != nil {
		t.Fatal(err)
	}
	a, err := p.UserManagement(auth.TenantRead)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.UserManagement(auth.TenantRead)
	if a.Client == b.Client {
		t.Fatal("clients must be isolated")
	}
	if a.URL != "http://um:8080/graphql" || a.Client.mintURL != "http://um:8082"+auth.ServiceTokenPath || a.Client.subject != "dashboard-management" || len(a.Client.authorities) != 1 || a.Client.authorities[0] != "tenant:read" {
		t.Fatalf("wiring changed: %#v", a.Client)
	}
	copy := p.With(WithQueryTimeout(time.Minute))
	if copy.Client().http.Timeout != time.Minute || p.Client().http.Timeout == time.Minute {
		t.Fatal("With mutated original")
	}
	if _, err := NewPeers(infra, "unknown"); err == nil {
		t.Fatal("unknown area accepted")
	}
}

func TestPeersBlankAreaPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("blank area accepted")
		}
	}()
	NewPeers(config.InfrastructureConfiguration{}, "")
}
