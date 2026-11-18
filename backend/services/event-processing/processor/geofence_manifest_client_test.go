// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/svcclient"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestArchiveSkewClassification(t *testing.T) {
	unknown := &svcclient.GraphQLError{Messages: []string{`Cannot query field "geoFenceGeometry" on type "Query".`}}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transport with trigger words", errors.New(`proxy: Cannot query field "geoFenceGeometry" on type "Query".`), false},
		{"separate messages", &svcclient.GraphQLError{Messages: []string{
			`Cannot query field "widgets" on type "Query".`,
			`Failed to resolve geoFenceGeometry.`,
		}}, false},
		{"unknown field", unknown, true},
		{"wrapped unknown field", fmt.Errorf("read archive: %w", unknown), true},
		{"near miss", &svcclient.GraphQLError{Messages: []string{`Cannot query field "geoFenceGeometryExtra" on type "Query".`}}, false},
		{"suggestion", &svcclient.GraphQLError{Messages: []string{`Cannot query field "geoFenceGeometr" on type "Query". Did you mean "geoFenceGeometry"?`}}, false},
		{"unrelated refusal in same message", &svcclient.GraphQLError{Messages: []string{`Cannot query field "widgets" on type "Query". See geoFenceGeometry.`}}, false},
		{"URL with trigger words", &svcclient.GraphQLError{URL: `Cannot query field "geoFenceGeometry" on type "Query".`, Messages: []string{"unavailable"}}, false},
		{"wrapper with trigger words", fmt.Errorf(`Cannot query field "geoFenceGeometry" on type "Query": %w`, &svcclient.GraphQLError{Messages: []string{"unavailable"}}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerLacksQuery(tc.err, "geoFenceGeometry"); got != tc.want {
				t.Errorf("peerLacksQuery = %v, want %v", got, tc.want)
			}
			if got := isArchiveSkew(tc.err); got != tc.want {
				t.Errorf("isArchiveSkew = %v, want %v", got, tc.want)
			}
			c := &fenceSetClient{metrics: testFenceGeometryMetrics()}
			got := c.classifyArchiveError(tc.err)
			wantCount := float64(0)
			if tc.want {
				wantCount = 1
				if !errors.Is(got, errArchiveSkew) {
					t.Errorf("classification = %v, want errArchiveSkew", got)
				}
			} else if got != tc.err {
				t.Errorf("classification = %v, want original error %v", got, tc.err)
			}
			if count := testutil.ToFloat64(c.metrics.fenceArchiveSkew); count != wantCount {
				t.Errorf("skew counter = %v, want %v", count, wantCount)
			}
		})
	}
}

type peerLacksQueryResolver struct{}

func (*peerLacksQueryResolver) Ping() string { return "pong" }

// Use the server library's actual validation messages, including its suggestions.
// Codes are intentionally absent: older peers do not provide a skew error code.
func TestPeerLacksQueryServerValidation(t *testing.T) {
	schema := graphql.MustParseSchema(`type Query { ping: String! }`, &peerLacksQueryResolver{})
	for _, field := range []string{
		"geoFenceSetManifest", "currentGeoFenceSetManifest", "geoFenceGeometry",
		"activeProfileRules", "deviceRosterPage", "deviceThresholdAttributePage", "deviceConstantsPage",
	} {
		t.Run(field, func(t *testing.T) {
			response := schema.Exec(context.Background(), "{ "+field+" }", "", nil)
			if len(response.Errors) != 1 {
				t.Fatalf("validation errors = %v, want one unknown-field refusal", response.Errors)
			}
			err := fmt.Errorf("peer: %w", &svcclient.GraphQLError{Messages: []string{response.Errors[0].Message}})
			if !peerLacksQuery(err, field) {
				t.Errorf("server's unknown-field refusal was not recognized: %v", err)
			}
			if peerLacksQuery(err, field+"Extra") {
				t.Error("a different field was classified as missing")
			}
		})
	}
}
