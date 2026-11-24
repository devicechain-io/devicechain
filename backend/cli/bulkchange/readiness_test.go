// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeClient struct {
	enforcement string
	orphans     *int
	err         error
	wantQuery   string
}

func (c fakeClient) Query(_ context.Context, endpoint, query string, _ map[string]any, out any) error {
	if endpoint != "test" || !strings.Contains(query, "fleetOperationReadiness") {
		return errors.New("wrong query")
	}
	if c.wantQuery != "" && query != c.wantQuery {
		return errors.New("wrong readiness spelling: " + query)
	}
	if c.err != nil {
		return c.err
	}
	b, _ := json.Marshal(map[string]any{"fleetOperationReadiness": Readiness{ProtocolVersion: 1, Enforcement: c.enforcement, ExecutableKinds: []string{}, OrphanedAttributeRows: c.orphans}})
	return json.Unmarshal(b, out)
}

func TestReadinessQuerySpellings(t *testing.T) {
	const fields = " { protocolVersion enforcement executableKinds orphanedAttributeRows } }"
	zero := 0
	for _, tc := range []struct {
		name  string
		query string
		run   func(context.Context, Client, string, io.Writer, bool) error
	}{
		{"bulk-change", "query { fleetOperationReadiness: bulkChangeReadiness" + fields, RunReadiness},
		{"fleet-op", "query { fleetOperationReadiness" + fields, RunLegacyReadiness},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tc.run(context.Background(), fakeClient{enforcement: "ENFORCED", orphans: &zero, wantQuery: tc.query}, "test", &out, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadinessExitAndJSON(t *testing.T) {
	for _, state := range []string{"NOT_INSTALLED", "DISABLED", "UNREADABLE", "ENFORCED", "FUTURE"} {
		for _, n := range []int{0, 1} {
			orphans := n
			var out bytes.Buffer
			err := RunReadiness(context.Background(), fakeClient{enforcement: state, orphans: &orphans}, "test", &out, true)
			if (err == nil) != (state == "ENFORCED" && orphans == 0) {
				t.Fatalf("%s/%d: %v", state, orphans, err)
			}
			var got Readiness
			if e := json.Unmarshal(out.Bytes(), &got); e != nil {
				t.Fatal(e)
			}
			if got.Enforcement != state || got.OrphanedAttributeRows == nil || *got.OrphanedAttributeRows != orphans || got.ProtocolVersion != 1 || got.ExecutableKinds == nil {
				t.Fatalf("lost facts: %+v", got)
			}
		}
	}
	var out bytes.Buffer
	err := RunReadiness(context.Background(), fakeClient{enforcement: "NOT_INSTALLED"}, "test", &out, false)
	if err == nil || !strings.Contains(err.Error(), "expected until the release that installs enforcement") {
		t.Fatalf("missing expectation: %v", err)
	}
	for _, fact := range []string{"protocolVersion", "enforcement", "executableKinds", "orphanedAttributeRows"} {
		if !strings.Contains(out.String(), fact) {
			t.Fatalf("missing %s", fact)
		}
	}
	if err := RunReadiness(context.Background(), fakeClient{err: errors.New("offline")}, "test", &out, true); err == nil {
		t.Fatal("query failure succeeded")
	}
}

// A count the server could not read is answered as null; the command reports it as unknown and
// is not ready, even when enforcement is in place.
func TestReadinessUnknownOrphanCountIsNotReady(t *testing.T) {
	var out bytes.Buffer
	err := RunReadiness(context.Background(), fakeClient{enforcement: "ENFORCED"}, "test", &out, false)
	if err == nil || !strings.Contains(out.String(), "unknown") {
		t.Fatalf("an unknown orphan count must not be ready: %v; output: %s", err, out.String())
	}
}
