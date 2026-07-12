// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authedReq builds a CallToolRequest carrying a verified token (as the RS
// middleware would have attached), so a tool's callerToken succeeds.
func authedReq(token string) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{
		Extra: map[string]any{extraTokenKey: token, extraTenantKey: "acme"},
	}}}
}

// toolsAgainst builds a Tools whose GraphQL client returns the given JSON body for
// every query.
func toolsAgainst(t *testing.T, body string) (*Tools, func()) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	return NewTools(testClient(ts.URL)), ts.Close
}

func TestGetDevice(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"devicesByToken":[{"token":"d1","name":"D1","externalId":"VIN1","deviceType":{"token":"truck"}}]}}`)
	defer done()

	_, out, err := tools.GetDevice(context.Background(), authedReq("tok"), GetDeviceInput{Tokens: []string{"d1"}})
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if len(out.Devices) != 1 || out.Devices[0].Token != "d1" || out.Devices[0].DeviceType != "truck" || out.Devices[0].ExternalId != "VIN1" {
		t.Errorf("unexpected devices: %+v", out.Devices)
	}
}

func TestGetDevice_RequiresTokens(t *testing.T) {
	tools := NewTools(NewGraphQLClient())
	if _, _, err := tools.GetDevice(context.Background(), authedReq("tok"), GetDeviceInput{}); err == nil {
		t.Errorf("empty tokens should error before any call")
	}
}

func TestGetDevice_Unauthenticated(t *testing.T) {
	tools := NewTools(NewGraphQLClient())
	if _, _, err := tools.GetDevice(context.Background(), &mcp.CallToolRequest{}, GetDeviceInput{Tokens: []string{"d1"}}); err == nil {
		t.Errorf("missing token should fail closed")
	}
}

func TestGetDeviceState(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"deviceStatesByDeviceToken":[{"deviceToken":"d1","active":true,"inactivityTimeout":600}]}}`)
	defer done()

	_, out, err := tools.GetDeviceState(context.Background(), authedReq("tok"), GetDeviceStateInput{DeviceTokens: []string{"d1"}})
	if err != nil {
		t.Fatalf("GetDeviceState: %v", err)
	}
	if len(out.States) != 1 || !out.States[0].Active || out.States[0].InactivityTimeout != 600 {
		t.Errorf("unexpected states: %+v", out.States)
	}
}

func TestGetLatestMeasurements(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"latestMeasurements":[{"name":"temp","value":21.5,"unit":"C","dataType":"FLOAT","occurredTime":"2026-07-12T00:00:00Z"}]}}`)
	defer done()

	_, out, err := tools.GetLatestMeasurements(context.Background(), authedReq("tok"), GetLatestMeasurementsInput{DeviceToken: "d1"})
	if err != nil {
		t.Fatalf("GetLatestMeasurements: %v", err)
	}
	if len(out.Measurements) != 1 || out.Measurements[0].Name != "temp" || out.Measurements[0].Value == nil || *out.Measurements[0].Value != 21.5 {
		t.Errorf("unexpected measurements: %+v", out.Measurements)
	}

	if _, _, err := tools.GetLatestMeasurements(context.Background(), authedReq("tok"), GetLatestMeasurementsInput{}); err == nil {
		t.Errorf("missing deviceToken should error")
	}
}

func TestGetDeviceCapabilities(t *testing.T) {
	body := `{"data":{"devicesByToken":[{"token":"d1","deviceType":{"token":"truck","profile":{"token":"p1","activeVersion":3,` +
		`"metricDefinitions":[{"metricKey":"temp","name":"Temperature","unit":"C","dataType":"FLOAT"}],` +
		`"commandDefinitions":[{"commandKey":"reboot","name":"Reboot"}]}}}]}}`
	tools, done := toolsAgainst(t, body)
	defer done()

	_, out, err := tools.GetDeviceCapabilities(context.Background(), authedReq("tok"), GetDeviceCapabilitiesInput{DeviceToken: "d1"})
	if err != nil {
		t.Fatalf("GetDeviceCapabilities: %v", err)
	}
	if out.Profile != "p1" || out.ActiveVersion == nil || *out.ActiveVersion != 3 {
		t.Errorf("unexpected profile/version: %+v", out)
	}
	if len(out.Metrics) != 1 || out.Metrics[0].MetricKey != "temp" {
		t.Errorf("unexpected metrics: %+v", out.Metrics)
	}
	if len(out.Commands) != 1 || out.Commands[0].CommandKey != "reboot" {
		t.Errorf("unexpected commands: %+v", out.Commands)
	}
}

func TestGetDeviceCapabilities_NotFound(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"devicesByToken":[]}}`)
	defer done()
	if _, _, err := tools.GetDeviceCapabilities(context.Background(), authedReq("tok"), GetDeviceCapabilitiesInput{DeviceToken: "nope"}); err == nil {
		t.Errorf("a missing device should error")
	}
}

// A device whose type has adopted no profile returns empty (non-nil) capability
// lists and no active version — never a nil-deref.
func TestGetDeviceCapabilities_NoProfile(t *testing.T) {
	tools, done := toolsAgainst(t, `{"data":{"devicesByToken":[{"token":"d1","deviceType":{"token":"truck","profile":null}}]}}`)
	defer done()
	_, out, err := tools.GetDeviceCapabilities(context.Background(), authedReq("tok"), GetDeviceCapabilitiesInput{DeviceToken: "d1"})
	if err != nil {
		t.Fatalf("GetDeviceCapabilities: %v", err)
	}
	if out.Profile != "" || out.ActiveVersion != nil {
		t.Errorf("no-profile device should have empty profile/version: %+v", out)
	}
	if out.Metrics == nil || out.Commands == nil || len(out.Metrics) != 0 || len(out.Commands) != 0 {
		t.Errorf("capabilities should be empty non-nil slices: %+v", out)
	}
}

// A blank token in a multi-token input is rejected before any downstream call.
func TestGetDevice_RejectsBlankToken(t *testing.T) {
	tools := NewTools(NewGraphQLClient())
	if _, _, err := tools.GetDevice(context.Background(), authedReq("tok"), GetDeviceInput{Tokens: []string{"ok", ""}}); err == nil {
		t.Errorf("a blank token should be rejected")
	}
}
