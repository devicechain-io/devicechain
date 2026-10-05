// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/userclient"
)

// A fake device-management GraphQL that models only the geofence operations. Any other
// operation answers 501, so an unexpected query cannot decode as "absent" and drive a
// create.
type fakeFenceServer struct {
	mu      sync.Mutex
	stored  map[string]map[string]any // token -> {name, description, geometry}
	creates []map[string]any
	updates []fenceUpdate
}

type fenceUpdate struct {
	token   string
	request map[string]any
}

func newFakeFenceServer(t *testing.T) (*fakeFenceServer, *Runtime) {
	t.Helper()
	f := &fakeFenceServer{stored: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, &Runtime{
		Endpoints:  Endpoints{DeviceMgmtGraphQL: srv.URL, UserGraphQL: srv.URL, EventProcessingGraphQL: srv.URL},
		InstanceId: "dc",
		Tenant:     "acme",
		HTTPClient: srv.Client(),
		Session:    userclient.NewTenantSession(srv.Client(), srv.URL, "sim@example.invalid", "pw", "acme"),
	}
}

func (f *fakeFenceServer) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var data map[string]any
	switch {
	case strings.Contains(req.Query, "login("):
		data = map[string]any{"login": map[string]any{"identityToken": "identity", "superuser": true}}
	case strings.Contains(req.Query, "selectTenant("):
		data = map[string]any{"selectTenant": map[string]any{"accessToken": "access", "refreshToken": "refresh"}}
	case strings.Contains(req.Query, "geoFencesByToken"):
		out := []any{}
		tokens, _ := req.Variables["tokens"].([]any)
		for _, tk := range tokens {
			if g, ok := f.stored[tk.(string)]; ok {
				out = append(out, g)
			}
		}
		data = map[string]any{"geoFencesByToken": out}
	case strings.Contains(req.Query, "createGeoFence"):
		rq := requestArg(req.Variables)
		f.creates = append(f.creates, rq)
		f.stored[str(rq, "token")] = map[string]any{
			"token": str(rq, "token"), "name": rq["name"], "description": rq["description"], "geometry": rq["geometry"],
		}
		data = map[string]any{"createGeoFence": map[string]any{"token": str(rq, "token")}}
	case strings.Contains(req.Query, "updateGeoFence"):
		rq, token := updateArgs(req.Variables)
		f.updates = append(f.updates, fenceUpdate{token: token, request: rq})
		g := f.stored[token]
		for k, v := range rq {
			g[k] = v
		}
		data = map[string]any{"updateGeoFence": map[string]any{"token": token}}
	default:
		http.Error(w, "fake server has no handler for: "+req.Query, http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func testFence(t *testing.T) GeoFenceSpec {
	t.Helper()
	return GeoFenceSpec{
		Token: "gf-test", Name: "Test", Description: "a test fence",
		Ring: [][2]float64{{-117, 39}, {-116.999, 39}, {-116.999, 39.001}, {-117, 39.001}, {-117, 39}},
	}
}

// jsonbRendering re-renders a geometry document the way a stored jsonb column prints it:
// a space after each separator. The digits are the platform's canonical form, which is the
// same as the document's.
func jsonbRendering(t *testing.T, doc string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	s := strings.NewReplacer(`":`, `": `, `,"`, `, "`).Replace(string(b))
	return strings.ReplaceAll(s, `],[`, `], [`)
}

func TestEnsureGeoFenceCreatesOnAFreshTenant(t *testing.T) {
	f, rt := newFakeFenceServer(t)
	g := testFence(t)
	if err := ensureGeoFence(context.Background(), rt, g); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 1 || len(f.updates) != 0 {
		t.Fatalf("creates %d updates %d, want 1 and 0", len(f.creates), len(f.updates))
	}
	rq := f.creates[0]
	want := map[string]bool{"token": true, "name": true, "description": true, "geometry": true}
	if len(rq) != len(want) {
		t.Errorf("create request %v, want exactly the keys %v", rq, want)
	}
	for k := range rq {
		if !want[k] {
			t.Errorf("unexpected create request key %q", k)
		}
	}
	doc, _ := g.geometryDocument()
	if !sameJSON(str(rq, "geometry"), doc) {
		t.Errorf("created geometry %s is not the declared document %s", str(rq, "geometry"), doc)
	}
}

func TestEnsureGeoFenceDoesNotRewriteAnUnchangedFence(t *testing.T) {
	f, rt := newFakeFenceServer(t)
	g := testFence(t)
	doc, _ := g.geometryDocument()
	f.stored[g.Token] = map[string]any{
		"token": g.Token, "name": g.Name, "description": g.Description,
		"geometry": jsonbRendering(t, doc),
	}
	if jsonbRendering(t, doc) == doc {
		t.Fatal("the fake's stored rendering equals the sent text, so this test cannot see a text compare")
	}
	if err := ensureGeoFence(context.Background(), rt, g); err != nil {
		t.Fatal(err)
	}
	if len(f.creates)+len(f.updates) != 0 {
		t.Fatalf("an unchanged fence was written: %d creates, %d updates", len(f.creates), len(f.updates))
	}

	// A null description reads as empty, so a fence declared with none is not drift.
	g.Description = ""
	f.stored[g.Token]["description"] = nil
	if err := ensureGeoFence(context.Background(), rt, g); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 0 {
		t.Fatalf("a null stored description against an empty declared one was rewritten")
	}
}

func TestEnsureGeoFenceConvergesADriftedFence(t *testing.T) {
	g := testFence(t)
	doc, _ := g.geometryDocument()
	moved := strings.Replace(doc, "-116.999,39.001", "-116.998,39.001", 1)
	if moved == doc {
		t.Fatal("test bug: the drifted document equals the declared one")
	}
	for _, c := range []struct {
		name   string
		stored map[string]any
	}{
		{"geometry moved", map[string]any{"name": g.Name, "description": g.Description, "geometry": moved}},
		{"renamed", map[string]any{"name": "Old", "description": g.Description, "geometry": doc}},
		{"description changed", map[string]any{"name": g.Name, "description": "old", "geometry": doc}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, rt := newFakeFenceServer(t)
			c.stored["token"] = g.Token
			f.stored[g.Token] = c.stored
			if err := ensureGeoFence(context.Background(), rt, g); err != nil {
				t.Fatal(err)
			}
			if len(f.creates) != 0 || len(f.updates) != 1 {
				t.Fatalf("creates %d updates %d, want 0 and 1", len(f.creates), len(f.updates))
			}
			u := f.updates[0]
			if u.token != g.Token {
				t.Errorf("update token argument %q, want %q", u.token, g.Token)
			}
			if _, has := u.request["token"]; has {
				t.Error("the update request carries a token; the update input has no such field")
			}
			if !sameJSON(str(u.request, "geometry"), doc) || u.request["name"] != g.Name ||
				u.request["description"] != g.Description {
				t.Errorf("update request %v does not carry the declared fence", u.request)
			}
			if !reflect.DeepEqual(f.stored[g.Token]["name"], g.Name) {
				t.Errorf("the stored name is %v after convergence", f.stored[g.Token]["name"])
			}
		})
	}
}

// The caller, not just the function: Provision must reach ensureGeoFence for every
// declared fence.
func TestProvisionProvisionsTheManifestsGeofences(t *testing.T) {
	f, rt := newFakeFenceServer(t)
	m := SimManifest{Name: "fence-only", Seed: 1, GeoFences: []GeoFenceSpec{testFence(t)}}
	if err := Provision(context.Background(), rt, m); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 1 || str(f.creates[0], "token") != "gf-test" {
		t.Fatalf("Provision did not create the declared fence: %+v", f.creates)
	}
}
