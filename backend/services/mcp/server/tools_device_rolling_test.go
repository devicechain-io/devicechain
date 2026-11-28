// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

// textValueField is the textValue definition, with its description, on LatestMeasurement.
var textValueField = regexp.MustCompile(`\n\s*"""[^"]*"""\s*textValue: String`)

// deviceStateBeforeText is the served device-state SDL as it was before textValue: the current
// one with that field removed, so it differs from the release before it in nothing else.
func deviceStateBeforeText(t *testing.T) string {
	t.Helper()
	sdl, served, err := schemaplane.SDLAt("../../device-state/graphql", schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("device-state SDL: served=%v err=%v", served, err)
	}
	if n := len(textValueField.FindAllStringIndex(sdl, -1)); n != 1 {
		t.Fatalf("want exactly one textValue field to remove, found %d", n)
	}
	return textValueField.ReplaceAllString(sdl, "")
}

// deviceStateServer answers like a device-state running sdl: a document is validated by the
// server's own validator first, and a refused one gets graphql-go's own error body (no data).
func deviceStateServer(t *testing.T, sdl string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if errs := schema.ValidateWithVariables(body.Query, body.Variables); len(errs) > 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": errs})
			return
		}
		rows := `{"name":"temp","value":21.5,"unit":"C","dataType":"DOUBLE","occurredTime":"2026-07-12T00:00:00Z"}`
		if strings.Contains(body.Query, "textValue") {
			rows += `,{"name":"mode","value":null,"textValue":"eco","unit":"","dataType":"STRING","occurredTime":"2026-07-12T00:00:00Z"}`
		} else {
			rows += `,{"name":"mode","value":null,"unit":"","dataType":"STRING","occurredTime":"2026-07-12T00:00:00Z"}`
		}
		_, _ = w.Write([]byte(`{"data":{"latestMeasurements":[` + rows + `]}}`))
	}))
}

func TestGetLatestMeasurementsAgainstBothDeviceStateReleases(t *testing.T) {
	current, _, err := schemaplane.SDLAt("../../device-state/graphql", schemaplane.MountTenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		sdl       string
		wantCalls int32
		wantText  bool
	}{
		{"current", current, 1, true},
		{"before textValue", deviceStateBeforeText(t), 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			ts := deviceStateServer(t, tc.sdl, &calls)
			defer ts.Close()
			tools := NewTools(testClient(ts.URL))

			_, out, err := tools.GetLatestMeasurements(context.Background(), authedReq("tok"), GetLatestMeasurementsInput{DeviceToken: "d1"})
			if err != nil {
				t.Fatalf("GetLatestMeasurements: %v", err)
			}
			if calls.Load() != tc.wantCalls {
				t.Errorf("want %d requests, got %d", tc.wantCalls, calls.Load())
			}
			if len(out.Measurements) != 2 || out.Measurements[0].Value == nil || *out.Measurements[0].Value != 21.5 {
				t.Fatalf("the numeric reading must be returned: %+v", out.Measurements)
			}
			text := out.Measurements[1].Untrusted
			if gotText := text != nil && text.Text != nil; gotText != tc.wantText {
				t.Errorf("text present = %v, want %v", gotText, tc.wantText)
			}
			if out.TextUnavailable == tc.wantText {
				t.Errorf("textUnavailable = %v, want %v", out.TextUnavailable, !tc.wantText)
			}
		})
	}
}

// Only a validation refusal NAMING textValue falls back; any other failure is the tool's
// failure, with no second request.
func TestGetLatestMeasurementsFallsBackOnlyForTheTextValueRefusal(t *testing.T) {
	for _, body := range []string{
		`{"errors":[{"message":"Cannot query field \"unit\" on type \"LatestMeasurement\".","locations":[{"line":3,"column":5}]}]}`,
		`{"errors":[{"message":"textValue resolver failed","path":["latestMeasurements"]}],"data":null}`,
		`{"errors":[{"message":"Cannot query field \"textValue\" on type \"LatestMeasurement\".","locations":[{"line":3,"column":5}],"extensions":{"code":"FORBIDDEN"}}]}`,
	} {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(body))
		}))
		tools := NewTools(testClient(ts.URL))
		_, _, err := tools.GetLatestMeasurements(context.Background(), authedReq("tok"), GetLatestMeasurementsInput{DeviceToken: "d1"})
		ts.Close()
		if err == nil || calls.Load() != 1 {
			t.Errorf("%s: want one failed request, got err=%v calls=%d", body, err, calls.Load())
		}
	}
}
