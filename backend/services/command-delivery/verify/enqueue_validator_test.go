// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// capturedQuery records what the validator asked device-management.
type capturedQuery struct {
	tenant      string
	deviceToken string
	commandKey  string
	payload     any
	payloadSent bool
}

// newValidator wires an EnqueueValidator against a stub mint endpoint and a stub
// device-management that returns the supplied verdict.
func newValidator(t *testing.T, allowed bool, reason *string) (*EnqueueValidator, *capturedQuery) {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)

	captured := &capturedQuery{}
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.tenant = r.Header.Get(auth.ServiceTenantHeader)
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured.deviceToken, _ = body.Variables["deviceToken"].(string)
		captured.commandKey, _ = body.Variables["commandKey"].(string)
		captured.payload, captured.payloadSent = body.Variables["payload"]

		verdict := map[string]any{"allowed": allowed, "reason": nil}
		if reason != nil {
			verdict["reason"] = *reason
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"validateCommandEnqueue": verdict},
		})
	}))
	t.Cleanup(dm.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "command-delivery", []string{string(auth.DeviceRead)})
	return NewEnqueueValidator(client, dm.URL), captured
}

func TestValidateEnqueue_Allowed(t *testing.T) {
	v, captured := newValidator(t, true, nil)
	ctx := core.WithTenant(context.Background(), "tenant-a")

	if err := v.ValidateEnqueue(ctx, "dev-1", "drive", []byte(`{"speed":10}`)); err != nil {
		t.Fatalf("expected the command to be allowed: %v", err)
	}
	if captured.tenant != "tenant-a" {
		t.Fatalf("tenant not propagated to device-management: %q", captured.tenant)
	}
	if captured.deviceToken != "dev-1" || captured.commandKey != "drive" {
		t.Fatalf("wrong query variables: device=%q command=%q", captured.deviceToken, captured.commandKey)
	}
	if captured.payload != `{"speed":10}` {
		t.Fatalf("payload not forwarded verbatim: %v", captured.payload)
	}
}

func TestValidateEnqueue_RejectionIsTyped(t *testing.T) {
	reason := `command "drive": required parameter "speed" is missing`
	v, _ := newValidator(t, false, &reason)
	ctx := core.WithTenant(context.Background(), "tenant-a")

	err := v.ValidateEnqueue(ctx, "dev-1", "drive", nil)
	if err == nil {
		t.Fatal("expected a rejection")
	}
	// A rejection must be typed so the caller can relay it verbatim rather than
	// treating it as an outage.
	var rejected *model.EnqueueRejected
	if !errors.As(err, &rejected) {
		t.Fatalf("rejection was not an *EnqueueRejected: %T (%v)", err, err)
	}
	if rejected.Reason != reason {
		t.Fatalf("reason not carried through: %q", rejected.Reason)
	}
}

// An absent payload must reach the owner as a GraphQL null, not as "". The owner
// treats null as "no arguments supplied" (valid unless a required parameter is
// declared) but "" as a present-but-unparseable body, which would reject every
// argument-less command.
func TestValidateEnqueue_AbsentPayloadIsNull(t *testing.T) {
	v, captured := newValidator(t, true, nil)
	ctx := core.WithTenant(context.Background(), "tenant-a")

	if err := v.ValidateEnqueue(ctx, "dev-1", "ping", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !captured.payloadSent {
		t.Fatal("payload variable was omitted entirely; expected an explicit null")
	}
	if captured.payload != nil {
		t.Fatalf("absent payload should be null, got %#v", captured.payload)
	}
}

func TestValidateEnqueue_NoTenant(t *testing.T) {
	v, _ := newValidator(t, true, nil)
	if err := v.ValidateEnqueue(context.Background(), "dev-1", "drive", nil); err == nil {
		t.Fatal("expected an error with no tenant in context — a tenantless service call must never be issued")
	}
}

// The load-bearing case: device-management being unreachable must surface as a
// transport error, NEVER as a rejection. If an outage were reported as a rejection,
// the caller would relay "your command is invalid" to the API client and the
// operator would chase a payload bug that does not exist — and, worse, the enqueue
// path would look like it was correctly validating when it was not.
func TestValidateEnqueue_OutageErrorsNotRejection(t *testing.T) {
	v, _ := newValidator(t, true, nil)
	// Point the validator at a closed port.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	v.url = url

	err := v.ValidateEnqueue(core.WithTenant(context.Background(), "tenant-a"), "dev-1", "drive", nil)
	if err == nil {
		t.Fatal("expected an error when device-management is unreachable")
	}
	var rejected *model.EnqueueRejected
	if errors.As(err, &rejected) {
		t.Fatalf("an outage must NOT be reported as a rejection: %v", err)
	}
}

// A GraphQL-level error (e.g. the caller lacks device:read) must also fail closed
// as a transport error rather than being read as a permissive verdict.
func TestValidateEnqueue_GraphQLErrorFailsClosed(t *testing.T) {
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "not authorized"}},
		})
	}))
	t.Cleanup(dm.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)}, "shh", "command-delivery", []string{string(auth.DeviceRead)})
	v := NewEnqueueValidator(client, dm.URL)

	err := v.ValidateEnqueue(core.WithTenant(context.Background(), "tenant-a"), "dev-1", "drive", nil)
	if err == nil {
		t.Fatal("a GraphQL error must fail closed, not pass the command through")
	}
	var rejected *model.EnqueueRejected
	if errors.As(err, &rejected) {
		t.Fatal("an authorization failure must not be reported as a command rejection")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Logf("note: error text was %q", err.Error())
	}
}
