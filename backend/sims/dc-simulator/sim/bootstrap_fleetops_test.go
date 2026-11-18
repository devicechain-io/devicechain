// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// Remember state across provisioning runs, and refuse a create over an existing token.
type fakeFleetServer struct {
	mu        sync.Mutex
	edges     map[string]map[string]any
	positions map[string]any
	writes    []string
	failOp    string
	lookups   int
	refuse    bool
}

func newFakeFleetServer(t *testing.T) (*fakeFleetServer, *Runtime) {
	t.Helper()
	f := &fakeFleetServer{edges: map[string]map[string]any{}, positions: map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		q, v := req.Query, req.Variables
		data := map[string]any{}
		if f.failOp != "" && strings.Contains(q, f.failOp+"(") {
			if f.refuse {
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{f.failOp: false}})
			} else {
				http.Error(w, "injected failure", http.StatusInternalServerError)
			}
			return
		}
		switch {
		case strings.Contains(q, "login("), strings.Contains(q, "selectTenant("):
			data, _ = (&fakeProfileServer{}).dispatch(q, v)
		case strings.Contains(q, "entityRelationshipsByToken("):
			rows := []any{}
			for _, token := range v["tokens"].([]any) {
				if edge, ok := f.edges[token.(string)]; ok {
					row := map[string]any{"token": token}
					for _, key := range []string{"sourceType", "source", "targetType", "target", "relationshipType"} {
						if strings.Contains(q, key) {
							row[key] = edge[key]
						}
					}
					rows = append(rows, row)
				}
			}
			data["entityRelationshipsByToken"] = rows
		case strings.Contains(q, "removeEntityRelationship("):
			token := str(v, "token")
			delete(f.edges, token)
			f.writes = append(f.writes, "remove:"+token)
			data["removeEntityRelationship"] = true
		case strings.Contains(q, "createEntityRelationships("):
			rows := []any{}
			for _, value := range v["requests"].([]any) {
				r := value.(map[string]any)
				token := str(r, "token")
				if _, exists := f.edges[token]; exists {
					http.Error(w, "relationship already exists", http.StatusConflict)
					return
				}
				f.edges[token] = map[string]any{"token": token, "sourceType": r["sourceType"],
					"source": map[string]any{"token": r["source"]}, "targetType": r["targetType"],
					"target": map[string]any{"token": r["target"]}, "relationshipType": map[string]any{"token": r["relationshipType"]}}
				f.writes = append(f.writes, "create:"+token)
				rows = append(rows, map[string]any{"token": token})
			}
			data["createEntityRelationships"] = rows
		case strings.Contains(q, "devicesByToken("):
			if n := len(v["tokens"].([]any)); n > positionLookupBatch {
				http.Error(w, "lookup keys exceeded", http.StatusBadRequest)
				return
			}
			f.lookups++
			rows := []any{}
			for _, token := range v["tokens"].([]any) {
				rows = append(rows, map[string]any{"token": token, "installedPosition": f.positions[token.(string)]})
			}
			data["devicesByToken"] = rows
		case strings.Contains(q, "setInstalledPosition("):
			token := str(v, "token")
			if v["family"] != "DEVICE" {
				http.Error(w, "wrong position family", http.StatusBadRequest)
				return
			}
			f.positions[token] = v["position"]
			f.writes = append(f.writes, "set:"+token)
			data["setInstalledPosition"] = v["position"]
		case strings.Contains(q, "clearInstalledPosition("):
			token := str(v, "token")
			if v["family"] != "DEVICE" {
				http.Error(w, "wrong position family", http.StatusBadRequest)
				return
			}
			delete(f.positions, token)
			f.writes = append(f.writes, "clear:"+token)
			data["clearInstalledPosition"] = true
		default:
			http.Error(w, "unhandled query: "+q, http.StatusNotImplemented)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return f, &Runtime{
		Endpoints: Endpoints{DeviceMgmtGraphQL: srv.URL},
		Session:   userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme"),
	}
}

func TestRelationshipsConvergeAcrossHarborpulseDefects(t *testing.T) {
	f, rt := newFakeFleetServer(t)
	clean := harborWith(t).Manifest().Relationships
	broken := harborWith(t, HarborpulseDefectFeederMiswired).Manifest().Relationships
	for _, rels := range [][]RelationshipSpec{clean, broken, clean} {
		if err := ensureRelationships(context.Background(), rt, rels); err != nil {
			t.Fatal(err)
		}
		for _, r := range rels {
			if got := f.edges[r.Token]["source"].(map[string]any)["token"]; got != r.Source {
				t.Fatalf("%s source = %v, want %s", r.Token, got, r.Source)
			}
		}
	}
}

func TestRelationshipsReplaceOnlyChangedShape(t *testing.T) {
	base := RelationshipSpec{Token: "edge", Type: "feeds", SourceType: "asset", Source: "feeder", TargetType: "device", Target: "lamp"}
	for _, field := range []string{"matching", "type", "sourceType", "source", "targetType", "target"} {
		t.Run(field, func(t *testing.T) {
			f, rt := newFakeFleetServer(t)
			if err := ensureRelationships(context.Background(), rt, []RelationshipSpec{base}); err != nil {
				t.Fatal(err)
			}
			want := base
			switch field {
			case "type":
				want.Type = "other"
			case "sourceType":
				want.SourceType = "device"
			case "source":
				want.Source = "other-feeder"
			case "targetType":
				want.TargetType = "asset"
			case "target":
				want.Target = "other-lamp"
			}
			f.writes = nil
			if err := ensureRelationships(context.Background(), rt, []RelationshipSpec{want}); err != nil {
				t.Fatal(err)
			}
			var writes []string
			if field != "matching" {
				writes = []string{"remove:edge", "create:edge"}
			}
			if !reflect.DeepEqual(f.writes, writes) {
				t.Fatalf("writes = %v, want %v", f.writes, writes)
			}
			edge := f.edges[base.Token]
			if edge["sourceType"] != want.SourceType || edge["targetType"] != want.TargetType ||
				edge["source"].(map[string]any)["token"] != want.Source || edge["target"].(map[string]any)["token"] != want.Target ||
				edge["relationshipType"].(map[string]any)["token"] != want.Type {
				t.Fatalf("stored edge = %v, want %+v", edge, want)
			}
		})
	}
}

func TestInstalledPositionsClearRemovedDeclaration(t *testing.T) {
	f, rt := newFakeFleetServer(t)
	device := DeviceInstance{Token: "lamp", Position: &PositionSpec{Latitude: 51, Longitude: -1}}
	for _, positioned := range []bool{true, true, false, false, true} {
		d := device
		if !positioned {
			d.Position = nil
		}
		_, had := f.positions[d.Token]
		f.writes = nil
		if err := ensureInstalledPositions(context.Background(), rt, []DeviceInstance{d}); err != nil {
			t.Fatal(err)
		}
		_, has := f.positions[d.Token]
		if has != positioned {
			t.Fatalf("stored position present = %v, want %v", has, positioned)
		}
		var writes []string
		if had != positioned {
			op := "clear:"
			if positioned {
				op = "set:"
			}
			writes = []string{op + d.Token}
		}
		if !reflect.DeepEqual(f.writes, writes) {
			t.Fatalf("writes = %v, want %v", f.writes, writes)
		}
	}
}

func TestFleetConvergenceFailsClosedOnRemovalFailure(t *testing.T) {
	for _, op := range []string{"removeEntityRelationship", "clearInstalledPosition"} {
		for _, refuse := range []bool{false, true} {
			t.Run(op+map[bool]string{false: "/error", true: "/refused"}[refuse], func(t *testing.T) {
				f, rt := newFakeFleetServer(t)
				r := RelationshipSpec{Token: "edge", Type: "feeds", SourceType: "asset", Source: "feeder", TargetType: "device", Target: "lamp"}
				d := DeviceInstance{Token: "lamp", Position: &PositionSpec{Latitude: 51, Longitude: -1}}
				if err := ensureRelationships(context.Background(), rt, []RelationshipSpec{r}); err != nil {
					t.Fatal(err)
				}
				if err := ensureInstalledPositions(context.Background(), rt, []DeviceInstance{d}); err != nil {
					t.Fatal(err)
				}
				f.writes, f.failOp, f.refuse = nil, op, refuse
				var err error
				token := "edge"
				if op == "removeEntityRelationship" {
					r.Source = "other-feeder"
					err = ensureRelationships(context.Background(), rt, []RelationshipSpec{r})
				} else {
					token, d.Position = "lamp", nil
					err = ensureInstalledPositions(context.Background(), rt, []DeviceInstance{d})
				}
				if err == nil || !strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), op) {
					t.Fatalf("want %s failure naming %s, got %v", op, token, err)
				}
				if len(f.writes) != 0 {
					t.Fatalf("writes after failed removal: %v", f.writes)
				}
			})
		}
	}
}

// A fleet larger than one lookup is read in batches, and a stored position is cleared even when
// no device in the fleet declares one.
func TestInstalledPositionsReadLargeFleetsInBatches(t *testing.T) {
	f, rt := newFakeFleetServer(t)
	devices := make([]DeviceInstance, 2*positionLookupBatch+1)
	for i := range devices {
		devices[i] = DeviceInstance{Token: fmt.Sprintf("device-%d", i)}
	}
	last := devices[len(devices)-1].Token
	f.positions[last] = map[string]any{"latitude": 51.0, "longitude": -1.0}
	if err := ensureInstalledPositions(context.Background(), rt, devices); err != nil {
		t.Fatal(err)
	}
	if f.lookups != 3 {
		t.Fatalf("lookups = %d, want 3", f.lookups)
	}
	if !reflect.DeepEqual(f.writes, []string{"clear:" + last}) {
		t.Fatalf("writes = %v", f.writes)
	}
}
