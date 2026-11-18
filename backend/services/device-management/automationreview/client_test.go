// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package automationreview

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

func TestCompareRuleVersionsClient(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		status      int
		unavailable bool
	}{
		{"refusal", `{"data":{"compareRuleVersions":{"ok":false,"refusal":{"reason":"BUSY","message":"no slot"}}}}`, 200, false},
		{"success", `{"data":{"compareRuleVersions":{"ok":true,"outcome":"PASSED","fidelity":{"status":"SUPPORTED","reasons":[],"label":"recorded"},"expectations":[{"index":0,"name":"catch","window":"INCIDENT","verdict":"PASS","baseline":"FAIL","reason":"matched","baselineReason":"absent"}],"windows":[{"role":"INCIDENT","start":"2026-11-10T08:00:00Z","end":"2026-11-10T09:00:00Z","leadInStart":"2026-11-10T07:55:00Z","stream":"resolved","firstSeq":"18446744073709551614","lastSeq":"18446744073709551615","inputDigest":"window-input","degraded":["aged"],"counts":{"added":2,"removed":3,"shifted":4,"contentChanged":5},"changesTruncated":true}],"inputDigest":"input","engineBuild":"engine","result":"{ \"outcome\": \"PASSED\" }","resultDigest":"digest"}}}`, 200, false},
		{"graphql error", `{"data":null,"errors":[{"message":"unknown field compareRuleVersions"}]}`, 200, true},
		{"http error", `down`, 500, true},
		{"missing", `{"data":{}}`, 200, true},
		{"empty object", `{"data":{"compareRuleVersions":{}}}`, 200, true},
		{"bad json", `{`, 200, true},
		{"bad time", `{"data":{"compareRuleVersions":{"ok":true,"windows":[{"start":"broken"}]}}}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var minted auth.ServiceTokenRequest
			mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&minted)
				_ = json.NewEncoder(w).Encode(auth.ServiceTokenResponse{Token: "service-token", ExpiresAt: 1 << 40})
			}))
			t.Cleanup(mint.Close)
			var tenant, bearer string
			var body struct {
				Query     string `json:"query"`
				Variables struct {
					Input model.CompareRequest `json:"input"`
				} `json:"variables"`
			}
			ep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tenant, bearer = r.Header.Get(auth.ServiceTenantHeader), r.Header.Get("Authorization")
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(ep.Close)
			host, portText, err := net.SplitHostPort(mint.Listener.Addr().String())
			require.NoError(t, err)
			port, err := strconv.Atoi(portText)
			require.NoError(t, err)
			client := svcclient.New(config.UserManagementConfiguration{Hostname: host, Port: uint32(port), InternalPort: uint32(port)}, "secret", "device-management", []string{string(auth.DeviceRead)})
			c := NewClient(client, ep.URL)
			at := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
			shift := int32(90)
			req := model.CompareRequest{ProfileToken: "profile", Base: &model.RuleEnvelope{Token: "r1", Enabled: true, Definition: `{}`},
				DeviceTokens: []string{"device"}, Windows: []model.ReviewWindow{{Role: "INCIDENT", Start: at, End: at.Add(time.Hour)}},
				Expectations: []model.ReviewExpectation{{Window: "INCIDENT", Name: "catch", Expect: `{}`}}, ShiftWindowSeconds: &shift, ConstantMetrics: []string{"scaled"}}
			res, err := c.CompareRuleVersions(core.WithTenant(context.Background(), "acme"), req)
			require.Equal(t, "acme", tenant)
			require.Equal(t, "Bearer service-token", bearer)
			require.Equal(t, auth.ServiceTokenRequest{Subject: "device-management", Authorities: []string{string(auth.DeviceRead)}}, minted)
			require.Equal(t, req, body.Variables.Input)
			require.Contains(t, body.Query, "compareRuleVersions(input: $input)")
			if tc.unavailable {
				require.True(t, errors.Is(err, model.ErrReviewUnavailable), "got %v", err)
				require.Nil(t, res)
				return
			}
			require.NoError(t, err)
			if tc.name == "refusal" {
				require.Equal(t, &model.CompareResponse{RefusalReason: "BUSY", RefusalMessage: "no slot"}, res)
				return
			}
			require.Equal(t, &model.CompareResponse{Ok: true, Outcome: "PASSED", Fidelity: model.ReviewFidelity{Status: "SUPPORTED", Reasons: []string{}, Label: "recorded"},
				Expectations: []model.ReviewExpectationResult{{Index: 0, Name: "catch", Window: "INCIDENT", Verdict: "PASS", Baseline: "FAIL", Reason: "matched", BaselineReason: "absent"}},
				Windows:      []model.ReviewWindowResult{{Role: "INCIDENT", Start: at, End: at.Add(time.Hour), LeadInStart: at.Add(-5 * time.Minute), Stream: "resolved", FirstSeq: "18446744073709551614", LastSeq: "18446744073709551615", InputDigest: "window-input", Degraded: []string{"aged"}, Counts: model.ReviewChangeCounts{Added: 2, Removed: 3, Shifted: 4, ContentChanged: 5}, ChangesTruncated: true}},
				InputDigest:  "input", EngineBuild: "engine", Result: `{ "outcome": "PASSED" }`, ResultDigest: "digest"}, res)
		})
	}
}
