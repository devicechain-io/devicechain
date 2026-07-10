// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package ruleverify

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// newValidator wires a Validator against a stub mint endpoint and a stub event-processing
// whose validateDetectionRules rejects any submitted rule whose token starts with "bad".
// It captures the tenant header the call carried.
func newValidator(t *testing.T) (*Validator, *string) {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)

	var gotTenant string
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(auth.ServiceTenantHeader)
		var body struct {
			Variables struct {
				Rules []struct {
					Token      string `json:"token"`
					Definition string `json:"definition"`
				} `json:"rules"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		errs := []map[string]any{}
		for i, rule := range body.Variables.Rules {
			if len(rule.Token) >= 3 && rule.Token[:3] == "bad" {
				errs = append(errs, map[string]any{"index": i, "token": rule.Token, "message": "cannot compile " + rule.Token})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"validateDetectionRules": map[string]any{"valid": len(errs) == 0, "errors": errs},
		}})
	}))
	t.Cleanup(ep.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "device-management", []string{string(auth.DeviceRead)})
	return NewValidator(client, ep.URL), &gotTenant
}

func rule(token string) model.RuleToValidate {
	return model.RuleToValidate{Token: token, Definition: `{"name":"n","type":"threshold"}`}
}

// All-compilable rules return no failures, and the tenant rides the call.
func TestValidate_AllValid(t *testing.T) {
	v, gotTenant := newValidator(t)
	ctx := core.WithTenant(context.Background(), "tenant-a")

	failures, err := v.ValidateDetectionRules(ctx, []model.RuleToValidate{rule("hot"), rule("cold")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if *gotTenant != "tenant-a" {
		t.Fatalf("event-processing did not receive the tenant header: %q", *gotTenant)
	}
}

// A rejected rule comes back as a failure carrying the token + reason, and the good rule
// alongside it is not reported.
func TestValidate_ReportsRejections(t *testing.T) {
	v, _ := newValidator(t)
	ctx := core.WithTenant(context.Background(), "t")

	failures, err := v.ValidateDetectionRules(ctx, []model.RuleToValidate{rule("ok"), rule("bad-one")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure, got %d: %+v", len(failures), failures)
	}
	if failures[0].Token != "bad-one" || failures[0].Message == "" {
		t.Fatalf("failure not anchored to the rejected rule: %+v", failures[0])
	}
}

// An empty rule set short-circuits without a service call (nothing to validate).
func TestValidate_EmptyShortCircuits(t *testing.T) {
	v, gotTenant := newValidator(t)
	failures, err := v.ValidateDetectionRules(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %+v", failures)
	}
	if *gotTenant != "" {
		t.Fatalf("expected no service call for an empty rule set, tenant seen: %q", *gotTenant)
	}
}

// Without a tenant in context the validator fails rather than issuing a tenantless call.
func TestValidate_NoTenant(t *testing.T) {
	v, _ := newValidator(t)
	if _, err := v.ValidateDetectionRules(context.Background(), []model.RuleToValidate{rule("x")}); err == nil {
		t.Fatal("expected an error when no tenant is in context")
	}
}

// The load-bearing property: an event-processing outage must ERROR (so the publish fails
// closed), never return an empty failure list — which would wrongly read a down validator
// as "all rules valid".
func TestValidate_OutageErrorsNotEmpty(t *testing.T) {
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc", ExpiresAt: 1 << 40})
	}))
	defer mint.Close()
	ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ep.Close()

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "dm", []string{string(auth.DeviceRead)})
	v := NewValidator(client, ep.URL)

	failures, err := v.ValidateDetectionRules(core.WithTenant(context.Background(), "A"), []model.RuleToValidate{rule("x")})
	if err == nil {
		t.Fatal("expected an error on an event-processing outage (must not read as 'all valid')")
	}
	if len(failures) != 0 {
		t.Fatalf("outage returned failures instead of erroring: %+v", failures)
	}
}
