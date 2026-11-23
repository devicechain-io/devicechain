// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

func s7TestMCP(t *testing.T, answer func(string) map[string]any) *s7MCP {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int
			Params struct{ Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"structuredContent": answer(req.Params.Name)}})
	}))
	t.Cleanup(srv.Close)
	return &s7MCP{http: srv.Client(), endpoint: srv.URL, token: "member"}
}

func TestS7AggregateBracketsLateIngestion(t *testing.T) {
	for _, mode := range []string{"settles", "never-settles", "wrong-answer"} {
		t.Run(mode, func(t *testing.T) {
			reads, calls := 0, 0
			buckets := func(n int) []s7Bucket {
				return []s7Bucket{{BucketStart: at(17).Format(time.RFC3339Nano), Name: sim.HarborpulsePumpRunningKey, Count: n}}
			}
			r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
				reads++
				n := 2
				if mode == "never-settles" || (mode == "settles" && reads == 1) {
					n = reads
				}
				return map[string]any{"bucketedMeasurements": buckets(n)}, 0
			})
			r.events = r.endpoints.UpdateMgmtGraphQL
			m := s7TestMCP(t, func(tool string) map[string]any {
				calls++
				n := 2
				if mode == "wrong-answer" {
					n = 99
				}
				return map[string]any{"buckets": buckets(n)}
			})
			pair, truth, actual, err := s7Aggregate(context.Background(), r, m, "pump", sim.HarborpulsePumpRunningKey, at(17), at(19))
			if mode == "never-settles" {
				if err == nil || reads != 8 || calls != 4 {
					t.Fatalf("unsettled aggregate: reads=%d tools=%d err=%v", reads, calls, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(truth, buckets(2)) {
				t.Fatalf("aggregate truth=%v err=%v", truth, err)
			}
			if mode == "settles" && (reads != 4 || calls != 2 || !reflect.DeepEqual(truth, actual)) {
				t.Fatalf("late ingestion was not retried: reads=%d tools=%d pair=%s/%s", reads, calls, pair.Expected, pair.Actual)
			}
			if mode == "wrong-answer" && (reads != 2 || calls != 1 || reflect.DeepEqual(pair.Expected, pair.Actual)) {
				t.Fatal("stable wrong MCP answer was hidden")
			}
		})
	}
}

func TestS7CommandsBracketLateSuccess(t *testing.T) {
	for _, moving := range []bool{false, true} {
		t.Run(map[bool]string{false: "settles", true: "never-settles"}[moving], func(t *testing.T) {
			reads, calls := 0, 0
			command := s7Command{Token: "c", DeviceToken: "pump", Name: sim.HarborpulseStartPumpKey, Status: "SUCCESSFUL", QueuedTime: at(18).Format(time.RFC3339Nano), RespondedTime: at(18.1).Format(time.RFC3339Nano)}
			r := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
				reads++
				rows := []s7Command{command}
				if reads == 1 || (moving && reads%2 == 1) {
					rows = []s7Command{}
				}
				return map[string]any{"commands": map[string]any{"results": rows, "pagination": map[string]any{"totalRecords": len(rows)}}}, 0
			})
			r.endpoints.CommandMgmtGraphQL = r.endpoints.UpdateMgmtGraphQL
			m := s7TestMCP(t, func(string) map[string]any {
				calls++
				return map[string]any{"commands": []s7Command{command}, "totalRecords": 1}
			})
			pair, truth, actual, err := s7CommandComparison(context.Background(), r, m, "pump", at(17), at(19))
			if moving {
				if err == nil || reads != 8 || calls != 4 {
					t.Fatalf("unsettled commands: reads=%d tools=%d err=%v", reads, calls, err)
				}
			} else if err != nil || reads != 4 || calls != 2 || !reflect.DeepEqual(truth, actual) || !reflect.DeepEqual(pair.Expected, pair.Actual) {
				t.Fatalf("late command success was not retried: reads=%d tools=%d err=%v pair=%s/%s", reads, calls, err, pair.Expected, pair.Actual)
			}
		})
	}
}

func TestS7ConnectivityRetriesCompleteSnapshot(t *testing.T) {
	for _, moving := range []bool{false, true} {
		t.Run(map[bool]string{false: "settles", true: "never-settles"}[moving], func(t *testing.T) {
			reads, peers, calls := 0, 0, 0
			row := s7State{DeviceToken: "charger", PresenceSource: "ASSERTED", LastConnectTime: at(10).Format(time.RFC3339Nano), LastActivityTime: at(12).Format(time.RFC3339Nano)}
			own := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
				reads++
				state := row
				state.Active = reads == 1
				if moving {
					state.InactivityTimeout = reads
				}
				return map[string]any{"deviceStatesByDeviceToken": []s7State{state}}, 0
			})
			other := s5ReaderServer(t, func(string, map[string]json.RawMessage) (any, int) {
				peers++
				state := row
				state.LastConnectTime = at(9).Format(time.RFC3339Nano)
				return map[string]any{"deviceStatesByDeviceToken": []s7State{state}}, 0
			})
			own.endpoints.DeviceStateGraphQL = own.endpoints.UpdateMgmtGraphQL
			other.endpoints.DeviceStateGraphQL = other.endpoints.UpdateMgmtGraphQL
			m := s7TestMCP(t, func(string) map[string]any {
				calls++
				return map[string]any{"states": []s7State{row}}
			})
			pair, err := s7StateComparison(context.Background(), own, other, m, []string{"charger"})
			if moving {
				if err == nil || reads != 8 || peers != 4 || calls != 4 {
					t.Fatalf("unsettled connectivity: own=%d peer=%d tools=%d err=%v", reads, peers, calls, err)
				}
			} else if err != nil || reads != 4 || peers != 2 || calls != 2 || !reflect.DeepEqual(pair.Expected, pair.Actual) {
				t.Fatalf("complete snapshot not retried: own=%d peer=%d tools=%d err=%v pair=%s/%s", reads, peers, calls, err, pair.Expected, pair.Actual)
			}
		})
	}
}

func TestS7InitializationFailureDeletesAllocatedSession(t *testing.T) {
	for _, failure := range []string{"malformed", "version", "notification", "http", "unallocated"} {
		t.Run(failure, func(t *testing.T) {
			deletes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes++
					if r.Header.Get("Mcp-Session-Id") != "allocated" || r.Header.Get("Authorization") != "Bearer member" {
						t.Error("cleanup lost allocated session or caller identity")
					}
					return
				}
				var req struct {
					ID     int
					Method string
				}
				json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "notifications/initialized" {
					http.Error(w, "notification failed", 500)
					return
				}
				if failure != "unallocated" {
					w.Header().Set("Mcp-Session-Id", "allocated")
				}
				if failure == "http" || failure == "unallocated" {
					http.Error(w, "initialization failed", 500)
					return
				}
				if failure == "malformed" {
					w.Write([]byte("{"))
					return
				}
				result := map[string]any{}
				if failure == "notification" {
					result["protocolVersion"] = "2025-06-18"
				}
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer srv.Close()
			m := &s7MCP{http: srv.Client(), endpoint: srv.URL, token: "member"}
			if err := m.initialize(context.Background()); err == nil {
				t.Fatal("failed initialization passed")
			}
			want := 1
			if failure == "unallocated" {
				want = 0
			}
			if deletes != want {
				t.Fatalf("deleted %d sessions after failed initialization, want %d", deletes, want)
			}
			m.close()
			if deletes != want {
				t.Fatal("cleanup deleted an unallocated or already closed session")
			}
		})
	}
}
