// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
)

// A failure to ANSWER carries a machine-readable code, so a caller can tell "try again, nothing
// was decided" from a plain server error without reading prose. It stays an error and never
// becomes a rejection (a rejection is a verdict about the command), and it must reach the
// GraphQL layer unwrapped: graphql-go copies Extensions() only from the error a resolver
// returns directly.

type failingEnqueueValidator struct{}

func (failingEnqueueValidator) ValidateEnqueue(context.Context, string, string, []byte) ([]string, error) {
	return nil, errors.New("device-management is unreachable at 10.0.3.7:8080")
}

type failingBatchValidator struct{}

func (failingBatchValidator) ValidateEnqueueBatch(context.Context, []string, string, []byte) ([]BatchDeviceRefusal, error) {
	return nil, errors.New("device-management is unreachable at 10.0.3.7:8080")
}

type failingScheduler struct{}

func (failingScheduler) ScheduleDispatch(context.Context, []string, string, sql.NullTime) ([]DispatchHold, []BatchDeviceRefusal, error) {
	return nil, nil, errors.New("window lookup failed at 10.0.3.7:8080")
}

type passingEnqueueValidator struct{}

func (passingEnqueueValidator) ValidateEnqueue(context.Context, string, string, []byte) ([]string, error) {
	return nil, nil
}

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	// Direct type assertion, as graphql-go does it: a wrapped error would lose its extensions.
	u, ok := err.(*UnavailableError)
	if !ok {
		t.Fatalf("error is %T (%v), want *UnavailableError returned directly", err, err)
	}
	if got := u.Extensions()["code"]; got != UnavailableCode {
		t.Fatalf("extensions.code = %v, want %s", got, UnavailableCode)
	}
	var rejected *EnqueueRejected
	if errors.As(err, &rejected) {
		t.Fatalf("a failure to answer must not be a rejection: %v", err)
	}
	if msg := err.Error(); msg == "" || containsAny(msg, "10.0.3.7", "unreachable") {
		t.Fatalf("the message must stay sanitized, got %q", msg)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}

func TestUnavailableCodeIsTheSignInOne(t *testing.T) {
	if UnavailableCode != credential.CodeUnavailable {
		t.Fatalf("%q is not the code the rest of the platform answers an unavailable check with (%q)",
			UnavailableCode, credential.CodeUnavailable)
	}
}

func TestSingleEnqueueValidationUnavailableCarriesTheCode(t *testing.T) {
	api := newTestApi(t)
	api.EnqueueValidator = failingEnqueueValidator{}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "t", DeviceToken: "d", Name: "reboot"})
	assertUnavailable(t, err)
}

func TestSingleEnqueueWindowLookupFailureCarriesTheCode(t *testing.T) {
	api := newTestApi(t)
	api.EnqueueValidator = passingEnqueueValidator{}
	api.DispatchScheduler = failingScheduler{}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateCommand(ctx, &CommandCreateRequest{Token: "t", DeviceToken: "d", Name: "reboot"})
	assertUnavailable(t, err)
}

func TestBatchValidationUnavailableCarriesTheCode(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = failingBatchValidator{}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateCommandBatch(ctx, batchRequest("nightly", []string{"pump-1"}))
	assertUnavailable(t, err)
}
