// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// These are WIRE tests of a command's cause: the request goes in as a variable (the path every real
// client uses) and the cause comes back through the schema, because both halves of the contract —
// who may assert a cause and who may read one — are decided in resolvers.

const causeRuleID = "A/pump-profile@v3/derate-hot"

func causeVars(token string, cause map[string]any) map[string]any {
	request := map[string]any{"token": token, "deviceToken": "device-1", "name": "derate"}
	if cause != nil {
		request["cause"] = cause
	}
	return map[string]any{"request": request}
}

func alarmCause() map[string]any {
	return map[string]any{
		"kind": "ALARM", "ruleId": causeRuleID, "detectionKind": "threshold",
		"detectedAt": "2026-11-20T10:15:30.123456789+01:00", "value": 92.5,
		"alarmKey": "pump-overheat", "positionDerived": false,
	}
}

const readCauseQuery = `query($tokens: [String!]!) {
  commandsByToken(tokens: $tokens) {
    token
    cause { kind ruleId ruleToken profileToken detectionKind detectedAt value alarmKey deviceId }
  }
}`

type causeRead struct {
	Token string `json:"token"`
	Cause *struct {
		Kind          string   `json:"kind"`
		RuleID        string   `json:"ruleId"`
		RuleToken     *string  `json:"ruleToken"`
		ProfileToken  *string  `json:"profileToken"`
		DetectionKind string   `json:"detectionKind"`
		DetectedAt    string   `json:"detectedAt"`
		Value         *float64 `json:"value"`
		AlarmKey      *string  `json:"alarmKey"`
		DeviceID      *string  `json:"deviceId"`
	} `json:"cause"`
}

func readCause(t *testing.T, ctx context.Context, token string) causeRead {
	t.Helper()
	data := exec(t, ctx, readCauseQuery, map[string]any{"tokens": []any{token}})
	var out struct {
		CommandsByToken []causeRead `json:"commandsByToken"`
	}
	require.NoError(t, json.Unmarshal(data, &out))
	require.Len(t, out.CommandsByToken, 1)
	return out.CommandsByToken[0]
}

// reader is a caller holding command:read plus the given authorities.
func reader(ctx context.Context, authorities ...auth.Authority) context.Context {
	return withServiceAuthorities(ctx, append([]auth.Authority{auth.CommandRead}, authorities...)...)
}

// A service asserts a cause and a reader allowed everything it names gets all of it back, with the
// detection time exactly as event-processing stamped it (nanoseconds, in UTC).
func TestAServiceRecordsACauseAndItReadsBack(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	out := decodeCreate(t, exec(t, ctx, createCommandMutation, causeVars("caused", alarmCause())))
	require.Nil(t, out.CreateCommand.Rejection)
	require.NotNil(t, out.CreateCommand.Command)

	got := readCause(t, reader(ctx, auth.DeviceRead, auth.AlarmRead), "caused")
	require.NotNil(t, got.Cause, "a reader holding device:read and alarm:read must see an ALARM cause")
	require.Equal(t, "ALARM", got.Cause.Kind)
	require.Equal(t, causeRuleID, got.Cause.RuleID)
	require.Equal(t, "derate-hot", *got.Cause.RuleToken)
	require.Equal(t, "pump-profile", *got.Cause.ProfileToken)
	require.Equal(t, "threshold", got.Cause.DetectionKind)
	require.Equal(t, "2026-11-20T09:15:30.123456789Z", got.Cause.DetectedAt)
	require.Equal(t, 92.5, *got.Cause.Value)
	require.Equal(t, "pump-overheat", *got.Cause.AlarmKey)
	require.Nil(t, got.Cause.DeviceID, "no enqueue gate answered, so the device's id is unknown")
}

// The device id is the incarnation the enqueue gate validated against, so a consumer can refuse an
// alarm of a later device that reused the token.
func TestACauseServesTheDeviceIncarnation(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	api.EnqueueValidator = incarnationValidator{id: 4711}
	exec(t, ctx, createCommandMutation, causeVars("caused-incarnation", alarmCause()))

	got := readCause(t, reader(ctx, auth.DeviceRead, auth.AlarmRead), "caused-incarnation")
	require.NotNil(t, got.Cause)
	require.NotNil(t, got.Cause.DeviceID)
	require.Equal(t, "4711", *got.Cause.DeviceID)
}

type incarnationValidator struct{ id uint64 }

func (v incarnationValidator) ValidateEnqueue(context.Context, string, string, []byte) ([]string, uint64, error) {
	return nil, v.id, nil
}

// 🔴 A PERSON MAY NOT SAY THE PLATFORM SENT THEIR COMMAND. A tenant user holding every authority
// ("*") is still refused, and nothing is written.
func TestAPersonCannotAssertACause(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	person := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(person, createCommandMutation, "", causeVars("forged", alarmCause()))
	require.NotEmpty(t, res.Errors, "a person's cause must be refused")
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrForbidden), "got %v", res.Errors[0])
	got, err := api.CommandsByToken(ctx, []string{"forged"})
	require.NoError(t, err)
	require.Empty(t, got, "a refused cause must create no command")

	// The counterweight: the same person's command WITHOUT a cause is accepted as always.
	res = schema.Exec(person, createCommandMutation, "", causeVars("personal", nil))
	require.Empty(t, res.Errors)
	require.Contains(t, string(res.Data), `"token":"personal"`)
}

// The forgery check runs before the replay lookup, so naming an existing token does not slip a
// person's cause past it.
func TestAForgedCauseIsRefusedEvenOnAReplay(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	exec(t, ctx, createCommandMutation, causeVars("existing", nil))
	person := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(person, createCommandMutation, "", causeVars("existing", alarmCause()))
	require.NotEmpty(t, res.Errors)
}

// A replay returns the ORIGINAL row whatever the retry carries: the first enqueue's cause (or its
// absence) stands. This is what makes REACT's fallback — resend without the cause — safe.
func TestAReplayKeepsTheOriginalCause(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	exec(t, ctx, createCommandMutation, causeVars("first-without", nil))
	exec(t, ctx, createCommandMutation, causeVars("first-without", alarmCause()))
	require.Nil(t, readCause(t, reader(ctx, auth.DeviceRead, auth.AlarmRead, auth.LocationRead), "first-without").Cause)

	exec(t, ctx, createCommandMutation, causeVars("first-with", alarmCause()))
	exec(t, ctx, createCommandMutation, causeVars("first-with", nil))
	require.NotNil(t, readCause(t, reader(ctx, auth.DeviceRead, auth.AlarmRead), "first-with").Cause)
}

// Who may read a cause. Each row withholds exactly one authority the cause needs, beside rows that
// prove the same cause is visible with it.
func TestTheCauseIsWithheldFromAReaderWhoMayNotSeeWhatItNames(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	ruleCause := alarmCause()
	ruleCause["kind"] = "RULE"
	delete(ruleCause, "alarmKey")
	geoCause := alarmCause()
	geoCause["positionDerived"] = true
	exec(t, ctx, createCommandMutation, causeVars("c-alarm", alarmCause()))
	exec(t, ctx, createCommandMutation, causeVars("c-rule", ruleCause))
	exec(t, ctx, createCommandMutation, causeVars("c-geo", geoCause))
	exec(t, ctx, createCommandMutation, causeVars("c-unknown-position", ruleCause))
	// A row whose position verdict is unknown (NULL) is treated as position-derived.
	require.NoError(t, api.RDB.Database.Exec(`UPDATE commands SET cause_position_derived = NULL WHERE token = ?`,
		"c-unknown-position").Error)

	scoped := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.AuthorityAll)}, ScopeAnchors: []string{"customer:acme"}})

	cases := []struct {
		name    string
		ctx     context.Context
		token   string
		visible bool
	}{
		{"alarm cause, device+alarm read", reader(ctx, auth.DeviceRead, auth.AlarmRead), "c-alarm", true},
		{"alarm cause, no alarm read", reader(ctx, auth.DeviceRead), "c-alarm", false},
		{"alarm cause, no device read", reader(ctx, auth.AlarmRead), "c-alarm", false},
		{"rule cause, device read only", reader(ctx, auth.DeviceRead), "c-rule", true},
		{"rule cause, no device read", reader(ctx), "c-rule", false},
		{"geofence cause, no location read", reader(ctx, auth.DeviceRead, auth.AlarmRead), "c-geo", false},
		{"geofence cause, location read", reader(ctx, auth.DeviceRead, auth.AlarmRead, auth.LocationRead), "c-geo", true},
		{"unknown position, no location read", reader(ctx, auth.DeviceRead), "c-unknown-position", false},
		{"unknown position, location read", reader(ctx, auth.DeviceRead, auth.LocationRead), "c-unknown-position", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readCause(t, tc.ctx, tc.token)
			require.Equal(t, tc.visible, got.Cause != nil)
		})
	}

	// A member limited to some customers never sees a cause, whatever it holds.
	cmd := model.Command{CauseKind: sql.NullString{String: "RULE", Valid: true},
		CausePositionDerived: sql.NullBool{Valid: true}}
	r := &CommandResolver{M: cmd}
	require.Nil(t, r.Cause(scoped), "a scoped member must never see a cause")
	require.NotNil(t, r.Cause(reader(ctx, auth.DeviceRead)), "the same row is visible to an unscoped reader")
}

// A malformed cause is a decided refusal with its own code, never a GraphQL error and never a
// stored half-cause.
func TestAMalformedCauseIsRefusedWithCauseInvalid(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	longKey := strings.Repeat("k", 1025)
	maxDefaultKey := strings.Repeat("p", 128) + "/" + strings.Repeat("r", 128)
	cases := map[string]func(map[string]any){
		"unknown kind":               func(c map[string]any) { c["kind"] = "WHIM" },
		"alarm without key":          func(c map[string]any) { delete(c, "alarmKey") },
		"alarm with empty key":       func(c map[string]any) { c["alarmKey"] = "" },
		"rule with key":              func(c map[string]any) { c["kind"] = "RULE" },
		"another tenant's rule":      func(c map[string]any) { c["ruleId"] = "B/pump-profile@v3/derate-hot" },
		"tenant prefix only":         func(c map[string]any) { c["ruleId"] = "A/" },
		"tenant as a prefix of name": func(c map[string]any) { c["ruleId"] = "AB/p@1/r" },
		"bad time":                   func(c map[string]any) { c["detectedAt"] = "yesterday" },
		"empty detection kind":       func(c map[string]any) { c["detectionKind"] = "" },
		"overlong detection kind":    func(c map[string]any) { c["detectionKind"] = strings.Repeat("x", 65) },
		"overlong alarm key":         func(c map[string]any) { c["alarmKey"] = longKey },
		"overlong rule id":           func(c map[string]any) { c["ruleId"] = "A/" + longKey },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cause := alarmCause()
			mutate(cause)
			token := "bad-" + strings.ReplaceAll(name, " ", "-")
			out := decodeCreate(t, exec(t, ctx, createCommandMutation, causeVars(token, cause)))
			require.NotNil(t, out.CreateCommand.Rejection, "a malformed cause must be refused")
			require.Equal(t, string(model.RejectCauseInvalid), out.CreateCommand.Rejection.Code)
			got, err := api.CommandsByToken(ctx, []string{token})
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}

	// The counterweight: the longest key the platform mints by default is accepted.
	cause := alarmCause()
	cause["alarmKey"] = maxDefaultKey
	out := decodeCreate(t, exec(t, ctx, createCommandMutation, causeVars("max-default-key", cause)))
	require.Nil(t, out.CreateCommand.Rejection)
}

func decodeCreate(t *testing.T, data json.RawMessage) createCommandResult {
	t.Helper()
	var out createCommandResult
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}
