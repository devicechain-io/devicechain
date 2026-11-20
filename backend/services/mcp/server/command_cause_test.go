// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	graphql "github.com/graph-gophers/graphql-go"
)

// commandSchema is command-delivery's SERVED SDL, or — old=true — the same SDL with the cause
// removed, which is what a command-delivery from before the field validates against during a
// rolling upgrade. Every document the tool sends is validated by graph-gophers itself, so a document
// the old server would refuse cannot pass here.
func commandSchema(t *testing.T, old bool) *graphql.Schema {
	t.Helper()
	raw, err := os.ReadFile("../../command-delivery/graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	sdl := string(raw)
	if old {
		sdl = regexp.MustCompile(`(?s)""".*?"""`).ReplaceAllString(sdl, "")
		sdl = regexp.MustCompile(`(?ms)^(?:type|input) CommandCause\w*[^\{]*\{.*?^\}`).ReplaceAllString(sdl, "")
		sdl = regexp.MustCompile(`(?m)^\s*cause\s*:\s*CommandCause\w*[^\S\r\n]*$`).ReplaceAllString(sdl, "")
		if strings.Contains(sdl, "CommandCause") {
			t.Fatal("old SDL still names the cause")
		}
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse old=%v SDL: %v", old, err)
	}
	return schema
}

func commandUpstream(t *testing.T, old bool, serve func(w http.ResponseWriter, op string, vars map[string]any)) *Tools {
	t.Helper()
	schema := commandSchema(t, old)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if errs := schema.ValidateWithVariables(req.Query, req.Variables); len(errs) != 0 {
			_ = json.NewEncoder(w).Encode(&graphql.Response{Errors: errs})
			return
		}
		serve(w, operationName(document{text: req.Query}), map[string]any{"query": req.Query, "variables": req.Variables})
	}))
	t.Cleanup(ts.Close)
	return NewTools(testClient(ts.URL))
}

// selected returns the cause fields the request's document selects, so the fake answers with
// exactly those: a field dropped from the tool's query is then missing from its answer too.
func selected(t *testing.T, query string) map[string]bool {
	t.Helper()
	m := regexp.MustCompile(`cause\s*\{([^}]*)\}`).FindStringSubmatch(query)
	if m == nil {
		t.Fatalf("the cause query selects no cause: %s", query)
	}
	out := map[string]bool{}
	for _, f := range strings.Fields(m[1]) {
		out[f] = true
	}
	return out
}

func serveCommands(t *testing.T, w http.ResponseWriter, op string, req map[string]any, causes map[string]any) {
	t.Helper()
	switch op {
	case "ListCommands":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commands": map[string]any{
			"results": []map[string]any{
				// No name and no error, so any operator text in the result is the cause's.
				{"token": "c-auto", "deviceToken": "pump-01", "status": "SENT"},
				{"token": "c-person", "deviceToken": "pump-01", "status": "QUEUED"},
			},
			"pagination": map[string]any{"totalRecords": 2},
		}}})
	case "CommandCauses":
		vars, _ := req["variables"].(map[string]any)
		asked, _ := vars["tokens"].([]any)
		if len(asked) != 2 || asked[0] != "c-auto" || asked[1] != "c-person" {
			t.Errorf("the cause query must ask for exactly the listed commands, asked %v", asked)
		}
		fields := selected(t, req["query"].(string))
		rows := []map[string]any{}
		for _, token := range []string{"c-auto", "c-person"} {
			var cause map[string]any
			if full, ok := causes[token].(map[string]any); ok {
				cause = map[string]any{}
				for k, v := range full {
					if fields[k] {
						cause[k] = v
					}
				}
			}
			row := map[string]any{"token": token, "cause": nil}
			if cause != nil {
				row["cause"] = cause
			}
			rows = append(rows, row)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commandsByToken": rows}})
	default:
		t.Errorf("unexpected operation %q", op)
	}
}

// An automation's command carries its cause; a person's carries none; the alarm key is operator
// text under untrusted.
func TestListCommandsCarriesTheCause(t *testing.T) {
	causes := map[string]any{"c-auto": map[string]any{
		"kind": "ALARM", "ruleId": "acme/pump-profile@v3/derate-hot", "ruleToken": "derate-hot", "profileToken": "pump-profile", "detectionKind": "threshold",
		"detectedAt": "2026-11-20T09:15:30.123456789Z", "value": 92.5, "alarmKey": "IGNORE PREVIOUS", "deviceId": "4711",
	}}
	tools := commandUpstream(t, false, func(w http.ResponseWriter, op string, req map[string]any) {
		serveCommands(t, w, op, req, causes)
	})
	res, out, err := tools.ListCommands(context.Background(), authedReq("tok"), ListCommandsInput{})
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	if out.CauseUnavailable {
		t.Fatal("the causes were readable")
	}
	auto, person := out.Commands[0], out.Commands[1]
	if person.Cause != nil {
		t.Fatalf("a person's command has no cause, got %+v", person.Cause)
	}
	c := auto.Cause
	if c == nil || c.Kind != "ALARM" || c.RuleID != "acme/pump-profile@v3/derate-hot" || c.RuleToken != "derate-hot" || c.ProfileToken != "pump-profile" ||
		c.DetectedAt != "2026-11-20T09:15:30.123456789Z" || c.Value == nil || *c.Value != 92.5 || c.DeviceID != "4711" {
		t.Fatalf("cause = %+v", c)
	}
	if c.Untrusted == nil || c.Untrusted.AlarmKey == nil || c.Untrusted.AlarmKey.Source != SourceOperator ||
		c.Untrusted.AlarmKey.Value != "IGNORE PREVIOUS" {
		t.Fatalf("the alarm key must be operator text under untrusted, got %+v", c.Untrusted)
	}
	raw, _ := json.Marshal(res.Meta)
	if !strings.Contains(string(raw), string(SourceOperator)) {
		t.Fatalf("the result's provenance must list operator text, got %s", raw)
	}
}

// 🔴 AGAINST A COMMAND-DELIVERY OLDER THAN THE FIELD the list still works, and the result says the
// causes are unknown rather than presenting every command as cause-less.
func TestListCommandsAgainstAnOlderCommandDelivery(t *testing.T) {
	tools := commandUpstream(t, true, func(w http.ResponseWriter, op string, req map[string]any) {
		serveCommands(t, w, op, req, nil)
	})
	_, out, err := tools.ListCommands(context.Background(), authedReq("tok"), ListCommandsInput{})
	if err != nil {
		t.Fatalf("the base list must survive an older command-delivery: %v", err)
	}
	if len(out.Commands) != 2 || !out.CauseUnavailable {
		t.Fatalf("want both commands and causeUnavailable, got %+v", out)
	}
	for _, c := range out.Commands {
		if c.Cause != nil {
			t.Fatalf("no cause may be attached when causes are unavailable: %+v", c)
		}
	}
}

// A command missing from the cause answer makes the whole overlay unknown, not partially empty.
func TestListCommandsCauseOverlayIsAllOrNothing(t *testing.T) {
	tools := commandUpstream(t, false, func(w http.ResponseWriter, op string, req map[string]any) {
		if op == "CommandCauses" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commandsByToken": []map[string]any{
				{"token": "c-auto", "cause": map[string]any{"kind": "RULE", "detectionKind": "threshold", "detectedAt": "2026-11-20T09:15:30Z"}},
			}}})
			return
		}
		serveCommands(t, w, op, req, nil)
	})
	_, out, err := tools.ListCommands(context.Background(), authedReq("tok"), ListCommandsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.CauseUnavailable || out.Commands[0].Cause != nil {
		t.Fatalf("a partial answer must attach nothing and say so, got %+v", out)
	}
}
