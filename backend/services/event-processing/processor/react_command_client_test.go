// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

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

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
)

// newCommandSink wires a real send-command sink — real svcclient, real HTTP, real JSON
// decode — against a stub command-delivery that answers with the supplied createCommand
// payload. The transport is not faked because the transport is where the answer is
// DECODED, and the decode is what this change turned into a decision: a payload shape
// the client cannot read is exactly the failure mode a hand-built fake would hide.
func newCommandSink(t *testing.T, payload map[string]any) (react.CommandSink, *string) {
	t.Helper()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)

	sentQuery := new(string)
	cd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*sentQuery = body.Query
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"createCommand": payload},
		})
	}))
	t.Cleanup(cd.Close)

	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port)},
		"shh", "event-processing", []string{string(auth.CommandWrite)})
	return NewCommandClient(client, cd.URL), sentQuery
}

func sendOne(t *testing.T, sink react.CommandSink) error {
	t.Helper()
	return sink.Send(context.Background(), react.CommandRequest{
		Tenant: "acme", Token: "det-1-act-1", DeviceToken: "device-1", Command: "setMode",
	})
}

// TestSendClassifiesAPermanentRejection proves the sink turns command-delivery's typed
// rejection into the one error the dispatcher drops instead of retrying.
//
// It asserts on the TYPE, not on the message: the dispatcher branches with errors.As,
// so a rejection reported as a formatted string would retry to the poison cap exactly
// as before — a regression no message assertion could see.
func TestSendClassifiesAPermanentRejection(t *testing.T) {
	for _, code := range []string{
		"DEVICE_NOT_FOUND", "COMMAND_NOT_IN_VOCABULARY", "PAYLOAD_SCHEMA_VIOLATION",
		"PAYLOAD_NOT_JSON", "METADATA_NOT_JSON", "EXPIRES_AT_INVALID",
	} {
		t.Run(code, func(t *testing.T) {
			sink, _ := newCommandSink(t, map[string]any{
				"command":   nil,
				"rejection": map[string]any{"code": code, "reason": "the command is invalid"},
			})
			err := sendOne(t, sink)
			var permanent *react.PermanentRejection
			if !errors.As(err, &permanent) {
				t.Fatalf("%s must be reported as a *react.PermanentRejection so the dispatcher can "+
					"drop it; got %T: %v", code, err, err)
			}
			if permanent.Code != code {
				t.Fatalf("the code must be relayed for the log/metric, got %q", permanent.Code)
			}
		})
	}
}

// TestSendRetriesATemporaryRejection is the counterweight that decides whether an
// offline fleet's backlog survives.
//
// HELD_CEILING_EXCEEDED says the tenant is at its limit of commands withheld for
// ABSENT devices right now — a limit that frees as those devices return. Classifying
// it as permanent would drop precisely the commands the fleet is waiting for, at
// precisely the moment it is unreachable.
func TestSendRetriesATemporaryRejection(t *testing.T) {
	sink, _ := newCommandSink(t, map[string]any{
		"command": nil,
		"rejection": map[string]any{
			"code":   "HELD_CEILING_EXCEEDED",
			"reason": "the tenant is already holding 10000 commands for absent devices",
		},
	})
	err := sendOne(t, sink)
	if err == nil {
		t.Fatal("a rejection must never read as success")
	}
	var permanent *react.PermanentRejection
	if errors.As(err, &permanent) {
		t.Fatal("HELD_CEILING_EXCEEDED is TEMPORARY — the ceiling frees as held commands drain. " +
			"Dropping it discards the backlog an offline fleet is waiting for")
	}
	if !strings.Contains(err.Error(), "HELD_CEILING_EXCEEDED") {
		t.Fatalf("the code belongs in the retryable error's message too, for the operator: %v", err)
	}
}

// TestSendRetriesAnUnrecognizedRejectionCode pins the DEFAULT, which is the safety
// property of the whole classification: permanence is opt-in.
//
// command-delivery owns the code vocabulary and may add to it. A code this build has
// never heard of must behave exactly as everything behaved before — retried, bounded
// by the consumer's redelivery cap. The opposite default would drop an actuation on a
// code nobody had taught this build about yet, with no retry and no recovery.
func TestSendRetriesAnUnrecognizedRejectionCode(t *testing.T) {
	for _, code := range []string{"COMMAND_REJECTED", "SOME_FUTURE_CODE", ""} {
		t.Run("code="+code, func(t *testing.T) {
			sink, _ := newCommandSink(t, map[string]any{
				"command":   nil,
				"rejection": map[string]any{"code": code, "reason": "refused"},
			})
			err := sendOne(t, sink)
			if err == nil {
				t.Fatal("a rejection must never read as success")
			}
			var permanent *react.PermanentRejection
			if errors.As(err, &permanent) {
				t.Fatalf("an unrecognized code (%q) must fall back to RETRY — today's behaviour — "+
					"never to a drop", code)
			}
		})
	}
}

// TestSendSucceedsOnACommand is the success counterweight: every assertion above is
// about failure, and a client that returned an error unconditionally would satisfy
// them all while never enqueuing anything.
//
// It also pins that the mutation selects BOTH arms. A document asking only for the
// command would decode a rejection as a null command and — before the neither-arm
// guard — report success for a command that was refused.
func TestSendSucceedsOnACommand(t *testing.T) {
	sink, sentQuery := newCommandSink(t, map[string]any{
		"command":   map[string]any{"token": "det-1-act-1"},
		"rejection": nil,
	})
	if err := sendOne(t, sink); err != nil {
		t.Fatalf("an accepted enqueue must succeed: %v", err)
	}
	if !strings.Contains(*sentQuery, "rejection") || !strings.Contains(*sentQuery, "code") {
		t.Fatalf("the mutation must select the rejection arm, or a refusal arrives as a null "+
			"command with no way to say why: %s", *sentQuery)
	}
}

// TestSendFailsWhenNeitherArmIsPresent guards the impossible answer.
//
// The schema says exactly one arm is non-null, so neither means a broken or
// intercepted response — not a verdict. It must be an error: reading it as success
// acks a derived event whose command was never enqueued, which loses the actuation
// silently and permanently.
func TestSendFailsWhenNeitherArmIsPresent(t *testing.T) {
	sink, _ := newCommandSink(t, map[string]any{"command": nil, "rejection": nil})
	err := sendOne(t, sink)
	if err == nil {
		t.Fatal("a response carrying neither a command nor a rejection must NOT read as success")
	}
	var permanent *react.PermanentRejection
	if errors.As(err, &permanent) {
		t.Fatal("an unanswerable response is a failure to ANSWER, not a decided rejection; " +
			"it must stay retryable")
	}
}
