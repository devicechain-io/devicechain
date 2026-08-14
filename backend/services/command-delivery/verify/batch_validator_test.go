// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// newBatchValidatorAgainst wires a BatchValidator to a stub device-management that
// answers every query with the supplied raw JSON body.
//
// The body is raw rather than a struct so a test can stage a MALFORMED or INCOMPLETE
// response — which is the whole point of the fail-open test below, and impossible to
// express through a well-typed fixture.
func newBatchValidatorAgainst(t *testing.T, body string) *BatchValidator {
	t.Helper()
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(dm.Close)
	return NewBatchValidator(newSvcClient(t), dm.URL)
}

func batchCtx() context.Context {
	return core.WithTenant(context.Background(), "acme")
}

// TestBatchRefusalsAreRelayedWithTheirCodes pins the relay: the owner's classification
// must arrive verbatim, because device-management owns the command vocabulary and
// therefore owns the reasons a command can be refused.
func TestBatchRefusalsAreRelayedWithTheirCodes(t *testing.T) {
	validator := newBatchValidatorAgainst(t, `{"data":{"validateCommandEnqueueBatch":[
	  {"deviceToken":"pump-a","code":"COMMAND_NOT_IN_VOCABULARY","reason":"no such command"},
	  {"deviceToken":"pump-b","code":"DEVICE_NOT_FOUND","reason":"gone"}
	]}}`)

	refusals, err := validator.ValidateEnqueueBatch(batchCtx(),
		[]string{"pump-a", "pump-b", "pump-c"}, "reboot", nil)
	if err != nil {
		t.Fatalf("a well-formed verdict must not be an error: %v", err)
	}
	if len(refusals) != 2 {
		t.Fatalf("expected 2 refusals, got %d: %+v", len(refusals), refusals)
	}
	if refusals[0].DeviceToken != "pump-a" || refusals[0].Code != "COMMAND_NOT_IN_VOCABULARY" {
		t.Fatalf("the first refusal did not survive the relay: %+v", refusals[0])
	}
	if refusals[1].Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("the second refusal's code did not survive: %+v", refusals[1])
	}
}

// TestUnknownRefusalCodeIsRelayedNotDropped guards the open-vocabulary contract. A
// translation table here would silently drop any code device-management adds after this
// was written — and a dropped code becomes an unclassified refusal, or worse, silence.
func TestUnknownRefusalCodeIsRelayedNotDropped(t *testing.T) {
	validator := newBatchValidatorAgainst(t, `{"data":{"validateCommandEnqueueBatch":[
	  {"deviceToken":"pump-a","code":"SOME_FUTURE_CODE","reason":"invented tomorrow"}
	]}}`)

	refusals, err := validator.ValidateEnqueueBatch(batchCtx(), []string{"pump-a"}, "reboot", nil)
	if err != nil {
		t.Fatalf("an unrecognized code is still a verdict, not an error: %v", err)
	}
	if len(refusals) != 1 || refusals[0].Code != "SOME_FUTURE_CODE" {
		t.Fatalf("an unrecognized code must be relayed unchanged, got %+v", refusals)
	}
}

// TestHealthyFleetIsAnEmptyList is the counterweight to the fail-open test below: an
// empty refusal list is the NORMAL answer and must not be mistaken for a broken one.
func TestHealthyFleetIsAnEmptyList(t *testing.T) {
	validator := newBatchValidatorAgainst(t, `{"data":{"validateCommandEnqueueBatch":[]}}`)

	refusals, err := validator.ValidateEnqueueBatch(batchCtx(), []string{"pump-a"}, "reboot", nil)
	if err != nil {
		t.Fatalf("an empty refusal list is the healthy answer, not an error: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("expected no refusals, got %+v", refusals)
	}
}

// TestAbsentVerdictIsAnErrorNotUnanimousApproval is the fail-open guard.
//
// 🔴 THE FIXTURE IS A 200 WITH NO `errors` AND NO DATA FOR THE FIELD, AND THAT SHAPE IS
// LOAD-BEARING. svcclient already turns a non-200, a malformed body, an oversized body
// and any GraphQL `errors` array into errors, so a fixture using any of those would pass
// through a path this code does not own — the test would be green for the wrong reason
// and would keep passing if the guard below were deleted. The one shape that reaches this
// function silently is a 200 whose `data` is absent: svcclient skips the decode entirely,
// leaving the slice at its nil zero value.
//
// On this call that is a fail-OPEN, and uniquely so. Everywhere else an empty answer is
// unusual enough to be noticed; here the empty list means "every device may receive this
// command", so a lost field reads as unanimous approval of an entire fleet.
func TestAbsentVerdictIsAnErrorNotUnanimousApproval(t *testing.T) {
	for name, body := range map[string]string{
		"data absent":           `{}`,
		"data null":             `{"data":null}`,
		"field missing":         `{"data":{}}`,
		"field explicitly null": `{"data":{"validateCommandEnqueueBatch":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			validator := newBatchValidatorAgainst(t, body)

			refusals, err := validator.ValidateEnqueueBatch(batchCtx(),
				[]string{"pump-a", "pump-b"}, "reboot", nil)
			if err == nil {
				t.Fatalf("a response carrying no verdict must be an ERROR so the caller "+
					"fails closed; it was read as %d refusals, i.e. approval of the whole "+
					"fleet", len(refusals))
			}
		})
	}
}

// TestBatchPayloadIsSentAsNullWhenAbsent pins the distinction the owner depends on: a
// null payload means "no arguments supplied" (valid unless a required parameter is
// declared), while "" would be a present-but-unparseable body.
func TestBatchPayloadIsSentAsNullWhenAbsent(t *testing.T) {
	var captured map[string]any
	dm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		captured = request.Variables
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"validateCommandEnqueueBatch":[]}}`))
	}))
	t.Cleanup(dm.Close)
	validator := NewBatchValidator(newSvcClient(t), dm.URL)

	if _, err := validator.ValidateEnqueueBatch(batchCtx(), []string{"pump-a"}, "reboot", nil); err != nil {
		t.Fatalf("validate: %v", err)
	}
	payload, present := captured["payload"]
	if !present {
		t.Fatal("payload must be SENT as an explicit null, not omitted — an omitted " +
			"variable and a null one are different documents to the owner")
	}
	if payload != nil {
		t.Fatalf("an absent payload must travel as null, got %#v", payload)
	}
}
