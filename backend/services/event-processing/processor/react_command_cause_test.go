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
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/react"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/svcclient"
	gql "github.com/graph-gophers/graphql-go"
)

// These tests put a REAL graphql-go schema behind the sink, because the rolling-upgrade fallback
// hangs on the exact refusal graphql-go writes for an input field the schema does not declare. A
// hand-written error string would pin this file's guess, not the library's behaviour.

// cdSDL is command-delivery's createCommand in miniature. withCause adds the new input field, as the
// upgraded command-delivery declares it; without it the schema is the shape an older build serves.
func cdSDL(withCause bool) string {
	cause := ""
	if withCause {
		cause = "\n  cause: CommandCauseInput"
	}
	return `schema { query: Query mutation: Mutation }
type Query { ping: String! }
type Mutation { createCommand(request: CommandCreateRequest!): CreateCommandResult! }
input CommandCreateRequest {
  token: String!
  deviceToken: String!
  name: String!
  payload: String
  expiresAt: String
  metadata: String` + cause + `
}
input CommandCauseInput {
  kind: String!
  ruleId: String!
  detectionKind: String!
  detectedAt: String!
  value: Float
  alarmKey: String
  positionDerived: Boolean!
}
type CreateCommandResult { command: Command rejection: Rejection }
type Command { token: String! }
type Rejection { code: String! reason: String! }`
}

type cdCause struct {
	Kind            string
	RuleId          string
	DetectionKind   string
	DetectedAt      string
	Value           *float64
	AlarmKey        *string
	PositionDerived bool
}

type cdRequest struct {
	Token       string
	DeviceToken string
	Name        string
	Payload     *string
	ExpiresAt   *string
	Metadata    *string
	Cause       *cdCause
}

// cdResolver records every request that reached it and refuses a cause when told to.
type cdResolver struct {
	mu           sync.Mutex
	got          []cdRequest
	refuseCause  bool
	rejectAlways string
}

type cdCommand struct{ token string }

func (c *cdCommand) Token() string { return c.token }

type cdRejection struct{ code string }

func (r *cdRejection) Code() string   { return r.code }
func (r *cdRejection) Reason() string { return "refused" }

type cdResult struct {
	command   *cdCommand
	rejection *cdRejection
}

func (r *cdResult) Command() *cdCommand     { return r.command }
func (r *cdResult) Rejection() *cdRejection { return r.rejection }

func (r *cdResolver) Ping() string { return "pong" }

func (r *cdResolver) CreateCommand(args struct{ Request cdRequest }) *cdResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, args.Request)
	if r.rejectAlways != "" {
		return &cdResult{rejection: &cdRejection{code: r.rejectAlways}}
	}
	if r.refuseCause && args.Request.Cause != nil {
		return &cdResult{rejection: &cdRejection{code: "CAUSE_INVALID"}}
	}
	return &cdResult{command: &cdCommand{token: args.Request.Token}}
}

// causeSink wires the real sink against a command-delivery serving schema sdl through graphql-go.
func causeSink(t *testing.T, sdl string, resolver *cdResolver) (react.CommandSink, *[]string) {
	t.Helper()
	schema := gql.MustParseSchema(sdl, resolver)
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "svc-token", ExpiresAt: 1 << 40})
	}))
	t.Cleanup(mint.Close)
	cd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(schema.Exec(r.Context(), body.Query, "", body.Variables))
	}))
	t.Cleanup(cd.Close)
	host, portStr, _ := net.SplitHostPort(mint.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)},
		"shh", "event-processing", []string{string(auth.CommandWrite)})
	omitted := &[]string{}
	return NewCommandClient(client, cd.URL, func(reason string) { *omitted = append(*omitted, reason) }), omitted
}

func causedRequest() react.CommandRequest {
	v := 92.5
	return react.CommandRequest{
		Tenant: "acme", Token: "det-1-act-1", DeviceToken: "pump-01", Command: "derate",
		Cause: &react.CommandCause{
			Kind: react.CauseKindAlarm, RuleID: "acme/p@1/r1", DetectionKind: "threshold",
			DetectedAt: time.Date(2026, 11, 20, 9, 15, 30, 123456789, time.UTC), Value: &v,
			AlarmKey: "pump-overheat", PositionDerived: true,
		},
	}
}

// An upgraded command-delivery receives the cause, field for field, with the detection time to the
// nanosecond, in one request.
func TestTheSinkSendsTheCause(t *testing.T) {
	cd := &cdResolver{}
	sink, omitted := causeSink(t, cdSDL(true), cd)
	if err := sink.Send(context.Background(), causedRequest()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(cd.got) != 1 || cd.got[0].Cause == nil {
		t.Fatalf("want one request carrying the cause, got %+v", cd.got)
	}
	c := cd.got[0].Cause
	if c.Kind != "ALARM" || c.RuleId != "acme/p@1/r1" || c.DetectionKind != "threshold" ||
		c.DetectedAt != "2026-11-20T09:15:30.123456789Z" || c.Value == nil || *c.Value != 92.5 ||
		c.AlarmKey == nil || *c.AlarmKey != "pump-overheat" || !c.PositionDerived {
		t.Fatalf("cause on the wire = %+v", c)
	}
	if len(*omitted) != 0 {
		t.Fatalf("nothing was omitted, got %v", *omitted)
	}

	// A RULE cause sends no alarm key and no value when it has none.
	cd = &cdResolver{}
	sink, _ = causeSink(t, cdSDL(true), cd)
	req := causedRequest()
	req.Cause.Kind, req.Cause.AlarmKey, req.Cause.Value = react.CauseKindRule, "", nil
	if err := sink.Send(context.Background(), req); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if c := cd.got[0].Cause; c.AlarmKey != nil || c.Value != nil {
		t.Fatalf("absent fields must not be sent: %+v", c)
	}
}

// 🔴 THE ROLLING-UPGRADE CASE. An older command-delivery refuses the whole request because it does
// not declare `cause`. The command must still be enqueued — the same token, without the cause —
// and the omission counted, rather than the actuation retried to the poison cap.
func TestAnOlderCommandDeliveryGetsTheCommandWithoutItsCause(t *testing.T) {
	cd := &cdResolver{}
	sink, omitted := causeSink(t, cdSDL(false), cd)
	if err := sink.Send(context.Background(), causedRequest()); err != nil {
		t.Fatalf("an older command-delivery must still receive the command: %v", err)
	}
	if len(cd.got) != 1 {
		t.Fatalf("the older peer must have executed exactly one request (the refused one never ran), got %d", len(cd.got))
	}
	if cd.got[0].Token != "det-1-act-1" || cd.got[0].Name != "derate" {
		t.Fatalf("the resend must be the same command under the same token: %+v", cd.got[0])
	}
	if len(*omitted) != 1 || (*omitted)[0] != CauseOmittedPeerLacksCause {
		t.Fatalf("omitted = %v, want one %s", *omitted, CauseOmittedPeerLacksCause)
	}
}

// A refused cause costs the cause, never the command.
func TestARefusedCauseStillSendsTheCommand(t *testing.T) {
	cd := &cdResolver{refuseCause: true}
	sink, omitted := causeSink(t, cdSDL(true), cd)
	if err := sink.Send(context.Background(), causedRequest()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(cd.got) != 2 || cd.got[1].Cause != nil {
		t.Fatalf("want the refused request then one without the cause, got %+v", cd.got)
	}
	if len(*omitted) != 1 || (*omitted)[0] != CauseOmittedCauseInvalid {
		t.Fatalf("omitted = %v, want one %s", *omitted, CauseOmittedCauseInvalid)
	}
}

// The counterweights: the fallback fires for those two answers ONLY. Any other refusal keeps its
// own meaning (a permanent one is still dropped, a temporary one retried) after a single request.
func TestOtherRefusalsAreNotTreatedAsACauseProblem(t *testing.T) {
	for code, permanent := range map[string]bool{"DEVICE_NOT_FOUND": true, "HELD_CEILING_EXCEEDED": false} {
		t.Run(code, func(t *testing.T) {
			cd := &cdResolver{rejectAlways: code}
			sink, omitted := causeSink(t, cdSDL(true), cd)
			err := sink.Send(context.Background(), causedRequest())
			var rejection *react.PermanentRejection
			if errors.As(err, &rejection) != permanent || err == nil {
				t.Fatalf("err = %v, want permanent=%v", err, permanent)
			}
			if len(cd.got) != 1 || len(*omitted) != 0 {
				t.Fatalf("one request and no omission expected, got %d requests, omitted %v", len(cd.got), *omitted)
			}
		})
	}

	// A CAUSE_INVALID on a request that carried NO cause is not a cause problem: it is an ordinary
	// retryable error, never a silent success.
	cd := &cdResolver{rejectAlways: "CAUSE_INVALID"}
	sink, _ := causeSink(t, cdSDL(true), cd)
	req := causedRequest()
	req.Cause = nil
	if err := sink.Send(context.Background(), req); err == nil {
		t.Fatal("a refusal must never read as success")
	}
}

// peerLacksCommandCause matches graphql-go's refusal of this field on this type and nothing else.
func TestPeerLacksCommandCauseIsExact(t *testing.T) {
	gqlErr := func(msgs ...string) error { return &svcclient.GraphQLError{Messages: msgs} }
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"the refusal":   {gqlErr("Variable \"request\" has invalid value.\nField \"cause\" is not defined by type \"CommandCreateRequest\"."), true},
		"another field": {gqlErr("Variable \"request\" has invalid value.\nField \"bogus\" is not defined by type \"CommandCreateRequest\"."), false},
		"another type":  {gqlErr("Variable \"r\" has invalid value.\nField \"cause\" is not defined by type \"BatchRequest\"."), false},
		"quoted inside": {gqlErr("upstream said: Field \"cause\" is not defined by type \"CommandCreateRequest\". retrying"), false},
		"not graphql":   {errors.New("Field \"cause\" is not defined by type \"CommandCreateRequest\"."), false},
		"nil":           {nil, false},
	} {
		if got := peerLacksCommandCause(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}
