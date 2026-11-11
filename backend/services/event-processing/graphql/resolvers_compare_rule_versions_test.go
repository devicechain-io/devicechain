// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/internal/nightshift"
	"github.com/devicechain-io/dc-event-processing/internal/review"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/auth"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/golang-jwt/jwt/v5"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

func compareServiceClaims() *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{string(auth.DeviceRead)}, RegisteredClaims: jwt.RegisteredClaims{Subject: "device-management"}}
}
func compareContext(c *auth.Claims) context.Context {
	return core.WithTenant(auth.WithClaims(context.Background(), c), nightshift.Tenant)
}
func TestCompareRuleVersionsServiceIdentityGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims *auth.Claims
	}{
		{"person", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.DeviceWrite)}}},
		{"scoped", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:one"}}},
		{"agent", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{string(auth.DeviceRead)}, ClientId: "agent"}},
		{"other service", &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{string(auth.DeviceRead)}, RegisteredClaims: jwt.RegisteredClaims{Subject: "event-sources"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// With no stores or replay source, this also proves authorization comes first.
			_, err := (&SchemaResolver{}).CompareRuleVersions(compareContext(tc.claims), struct{ Input compareRuleVersionsInput }{})
			require.ErrorIs(t, err, auth.ErrForbidden)
		})
	}
	_, err := (&SchemaResolver{}).CompareRuleVersions(auth.WithClaims(context.Background(), compareServiceClaims()), struct{ Input compareRuleVersionsInput }{})
	require.ErrorIs(t, err, core.ErrNoTenant)
}

const compareQuery = `query($input: CompareRuleVersionsInput!) { compareRuleVersions(input:$input) {
 ok refusal {reason message} outcome fidelity {status reasons label}
 expectations {index name window verdict baseline reason baselineReason}
 windows {role start end leadInStart stream firstSeq lastSeq inputDigest degraded changeCounts {added removed shifted contentChanged} changesTruncated}
 inputDigest engineBuild result resultDigest
} }`

func compareFixture(t *testing.T) (*SchemaResolver, map[string]any, *nightshift.Capture) {
	t.Helper()
	c := nightshift.Load(t)
	store, _ := evStores(t)
	// Matching command provenance establishes the closed-loop divergence point.
	require.NoError(t, store.Upsert(compareContext(compareServiceClaims()), []model.DetectRule{{RuleId: nightshift.Tenant + "/" + c.Header.ProfileVersion + "/" + c.Header.R1.Token, Tenant: nightshift.Tenant, ProfileVersionToken: c.Header.ProfileVersion, RuleToken: c.Header.R1.Token, Definition: string(c.Header.R1.Definition)}}))
	r := &SchemaResolver{Replay: c, DetectRules: store, EvaluationLateness: config.DefaultWatermarkLatenessSeconds * time.Second}
	base := map[string]any{"token": c.Header.R1.Token, "enabled": true, "definition": string(c.Header.R1.Definition)}
	var windows []any
	for _, role := range []string{"INCIDENT", "NORMAL"} {
		w := c.Header.Windows[role]
		windows = append(windows, map[string]any{"role": role, "start": w[0].Format(time.RFC3339), "end": w[1].Format(time.RFC3339)})
	}
	devices := make([]any, len(c.Header.Devices))
	for i, d := range c.Header.Devices {
		devices[i] = d
	}
	return r, map[string]any{"profileToken": c.Header.ProfileToken, "base": base, "draft": base, "deviceTokens": devices, "windows": windows}, c
}

func TestCompareRuleVersionsRecordedHistoryAndDigests(t *testing.T) {
	r, input, c := compareFixture(t)
	input["draft"] = map[string]any{"token": c.Header.R1.Token, "enabled": true, "definition": strings.Replace(string(c.Header.R1.Definition), `"threshold":26`, `"threshold":25.5`, 1)}
	input["expectations"] = []any{map[string]any{"window": "INCIDENT", "expect": `raised: {device: tp-vav-tower-f1-south, within: 30m}`}, map[string]any{"window": "NORMAL", "expect": `neverIntent: {kind: sendCommand, command: boost-cooling, device: tp-vav-tower-f3-north}`}}
	s := gql.MustParseSchema(SchemaContent, r)
	resp := s.Exec(compareContext(compareServiceClaims()), compareQuery, "", map[string]any{"input": input})
	require.Empty(t, resp.Errors)
	var wire struct {
		CompareRuleVersions struct {
			Ok                                                      bool
			Outcome, InputDigest, EngineBuild, Result, ResultDigest string
			Windows                                                 []json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &wire))
	out := wire.CompareRuleVersions
	require.True(t, out.Ok)
	require.Equal(t, "FAILED", out.Outcome)
	require.Len(t, out.Windows, 2)
	require.Equal(t, 2, c.Opens)
	require.Len(t, out.InputDigest, 64)
	require.NotEmpty(t, out.EngineBuild)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(out.Result))), out.ResultDigest)
	var result review.Result
	require.NoError(t, json.Unmarshal([]byte(out.Result), &result))
	require.Equal(t, out.InputDigest, result.InputDigest)
}

type neverCompareOpener struct{ t *testing.T }

func (n neverCompareOpener) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	n.t.Fatal("unsupported comparison opened history")
	return nil, time.Time{}, nil
}
func TestCompareRuleVersionsUnsupportedNeverOpensHistory(t *testing.T) {
	r, input, c := compareFixture(t)
	r.Replay = neverCompareOpener{t}
	input["draft"] = map[string]any{"token": c.Header.R1.Token, "enabled": true, "definition": `{"name":"position","type":"threshold","when":{"cel":"geo.inFence(\"yard\")"}}`}
	resp := gql.MustParseSchema(SchemaContent, r).Exec(compareContext(compareServiceClaims()), compareQuery, "", map[string]any{"input": input})
	require.Empty(t, resp.Errors)
	var wire struct {
		CompareRuleVersions struct {
			Ok              bool
			Outcome, Result string
			Fidelity        struct{ Status string }
			Windows         []any
		}
	}
	require.NoError(t, json.Unmarshal(resp.Data, &wire))
	out := wire.CompareRuleVersions
	require.True(t, out.Ok)
	require.Equal(t, "INCONCLUSIVE", out.Outcome)
	require.Equal(t, "UNSUPPORTED", out.Fidelity.Status)
	require.Empty(t, out.Windows)
	var result review.Result
	require.NoError(t, json.Unmarshal([]byte(out.Result), &result))
	require.Empty(t, result.Windows)
}
