// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
)

// servedTree is the real, current services tree: it serves the STRING producer.
const servedTree = "../../services"

// legacyTree writes a services tree that looks like a v1.9 target: the same schemas with the
// textValue field of the doors named in strip renamed away. Real files, read by the real loader.
func legacyTree(t *testing.T, strip ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, area := range []string{"device-management", "device-state", "event-management"} {
		src, err := os.ReadFile(filepath.Join(servedTree, area, "graphql", "schema.graphql"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, s := range strip {
			if s == area {
				text = strings.ReplaceAll(text, "textValue: String", "legacyText: String")
			}
		}
		if err := os.MkdirAll(filepath.Join(dir, area, "graphql"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, area, "graphql", "schema.graphql"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stringTarget is a fake instance whose behaviour is chosen per test.
type stringTarget struct {
	// declareErr, when set, is the message createMetricDefinition is refused with.
	declareErr string
	// latest and events are the rows the read doors answer (raw JSON for the list).
	latest, events string
	// aggregate answers the aggregate: "" means refuse it with NOT_AGGREGATABLE, else the data.
	aggregate string
	// down makes device-management answer 500.
	down bool

	ingress atomic.Int32
	posted  atomic.Value // the last event body
}

const textRow = `[{"name":"mode","value":null,"textValue":"heating"}]`

func (s *stringTarget) run(t *testing.T) (out string, err error) {
	return s.runWith(t, servedTree)
}

func (s *stringTarget) runWith(t *testing.T, schemas string, extra ...string) (out string, err error) {
	t.Helper()
	um := &fakeInstance{t: t, selectOK: func(int) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", um.userManagement)
	graph := func(handle func(q string, w http.ResponseWriter)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			handle(req.Query, w)
		}
	}
	mux.HandleFunc("/api/device-management/graphql", graph(func(q string, w http.ResponseWriter) {
		switch {
		case s.down:
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.Contains(q, "createMetricDefinition") && s.declareErr != "":
			_, _ = w.Write([]byte(`{"errors":[{"message":` + quote(s.declareErr) + `,"extensions":{"code":"INVALID_VALUE"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"ok":{"token":"x"}}}`))
		}
	}))
	mux.HandleFunc("/api/device-state/graphql", graph(func(q string, w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"data":{"latestMeasurements":` + s.latest + `}}`))
	}))
	mux.HandleFunc("/api/event-management/graphql", graph(func(q string, w http.ResponseWriter) {
		if strings.Contains(q, "bucketedMeasurements") {
			if s.aggregate == "" {
				_, _ = w.Write([]byte(`{"errors":[{"message":"not aggregatable","extensions":{"code":"NOT_AGGREGATABLE"}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"bucketedMeasurements":` + s.aggregate + `}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"measurementEvents":{"results":` + s.events + `}}}`))
	}))
	mux.HandleFunc("/probe-instance/apiprobe/events", func(w http.ResponseWriter, r *http.Request) {
		s.ingress.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.posted.Store(body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	oldT, oldI := stringSettleTimeout, stringSettleInterval
	stringSettleTimeout, stringSettleInterval = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { stringSettleTimeout, stringSettleInterval = oldT, oldI })

	path := filepath.Join(t.TempDir(), "receipt.json")
	if werr := writeReceipt(path, Receipt{
		Instance: "probe-instance",
		Tenant:   "apiprobe",
		Identity: Credential{Email: "probe@apiprobe.invalid", Password: "pw"},
		Written:  time.Now(),
		Entities: []Recorded{{Name: "device", Area: "device-management", Token: "apiprobe-device"}},
	}); werr != nil {
		t.Fatal(werr)
	}
	var buf bytes.Buffer
	argv := append([]string{
		"--receipt", path, "--schemas", schemas, "--server", strings.TrimPrefix(srv.URL, "http://"),
		"--scheme", "http", "--events-url", srv.URL,
	}, extra...)
	err = stringMetricTo(context.Background(), argv, &buf)
	return buf.String(), err
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func healthy() *stringTarget { return &stringTarget{latest: textRow, events: textRow} }

// A target that serves the producer: the text is posted, and read back as text through both doors,
// with a null value, and the aggregate is refused.
func TestStringMetricServedTargetPasses(t *testing.T) {
	s := healthy()
	out, err := s.run(t)
	if err != nil {
		t.Fatalf("a healthy producer must pass: %v", err)
	}
	if s.ingress.Load() != 1 {
		t.Fatalf("the text reading was posted %d times, want once", s.ingress.Load())
	}
	body := s.posted.Load().(map[string]any)
	entry := body["payload"].(map[string]any)["entries"].([]any)[0].(map[string]any)
	if entry["measurements"].(map[string]any)["mode"] != "heating" {
		t.Fatalf("the posted measurement is not the text reading: %v", body)
	}
	if !strings.Contains(out, "read back as textValue") {
		t.Fatalf("the report does not say what was proven:\n%s", out)
	}
}

// 🔴 THE NEGATIVE CONTROL. The planted defect is a probe that reads `value`: a text reading has a
// null value, so against a perfectly healthy target the phase must go RED. A green here would mean
// the phase never looked at the field that distinguishes a text reading.
func TestStringMetricProbeReadingValueGoesRed(t *testing.T) {
	_, err := healthy().runWith(t, servedTree, "--control", "read-value")
	assertCode(t, err, exitMismatch)
	if stringReadField != "textValue" {
		t.Fatalf("the control leaked: stringReadField is %q after the run", stringReadField)
	}
	// The counterweight: the same target, no control, is green.
	if _, err := healthy().run(t); err != nil {
		t.Fatalf("the healthy target must still pass without the control: %v", err)
	}
}

// A producer that stored the text as a number or lost it is red on EACH door.
func TestStringMetricBrokenProducerGoesRedOnEachDoor(t *testing.T) {
	broken := `[{"name":"mode","value":0,"textValue":null}]`
	both := `[{"name":"mode","value":0,"textValue":"heating"}]`
	for name, s := range map[string]*stringTarget{
		"latest drops the text":    {latest: broken, events: textRow},
		"events drops the text":    {latest: textRow, events: broken},
		"latest also has a value":  {latest: both, events: textRow},
		"events also has a value":  {latest: textRow, events: both},
		"latest never has the row": {latest: `[]`, events: textRow},
		"events never has the row": {latest: textRow, events: `[]`},
	} {
		_, err := s.run(t)
		if err == nil {
			t.Errorf("%s: the phase passed", name)
		}
	}
}

// An aggregate that answers (even with nothing) is a defect, and so is one refused with another code.
func TestStringMetricAggregateMustBeRefusedAsNotAggregatable(t *testing.T) {
	s := healthy()
	s.aggregate = `[]`
	_, err := s.run(t)
	assertCode(t, err, exitMismatch)
}

// On a target that serves the producer, a refused declaration is a finding about the platform.
func TestStringMetricServedTargetRefusingTheDeclarationIsRefused(t *testing.T) {
	s := healthy()
	s.declareErr = "unsupported data type STRING"
	_, err := s.run(t)
	assertCode(t, err, exitRefused)
}

// v1.9: the baseline REFUSES the declaration, and the phase asserts that instead of skipping. No text
// reading is posted.
func TestStringMetricV19TargetMustRefuseTheDeclaration(t *testing.T) {
	s := &stringTarget{declareErr: "dataType must be DOUBLE, INT or BOOLEAN"}
	out, err := s.runWith(t, legacyTree(t, "device-state", "event-management"))
	if err != nil {
		t.Fatalf("a v1.9 target that refuses STRING must pass: %v", err)
	}
	if !strings.Contains(out, "refused as expected") || s.ingress.Load() != 0 {
		t.Fatalf("out=%q ingress=%d", out, s.ingress.Load())
	}
}

// The counterweight: a v1.9 target that ACCEPTS the declaration is wrong, and a platform that did
// not answer proves nothing (setup, never a pass).
func TestStringMetricV19TargetAcceptingTheDeclarationIsRed(t *testing.T) {
	legacy := legacyTree(t, "device-state", "event-management")
	_, err := (&stringTarget{}).runWith(t, legacy)
	assertCode(t, err, exitMismatch)

	_, err = (&stringTarget{down: true}).runWith(t, legacy)
	if err == nil {
		t.Fatal("a target that did not answer must not pass")
	}
}

// One door without the other is a half-shipped producer, not a version.
func TestStringMetricHalfShippedProducerIsAShapeFinding(t *testing.T) {
	_, err := healthy().runWith(t, legacyTree(t, "device-state"))
	assertCode(t, err, exitShape)
	_, err = healthy().runWith(t, legacyTree(t, "event-management"))
	assertCode(t, err, exitShape)
}

// Every document the phase sends validates against the schema that serves it, with the variables it
// sends, and clears the root-field ceiling.
func TestStringMetricDocumentsValidateAgainstTheServedSchemas(t *testing.T) {
	p := &stringProbe{c: &connection{scheme: "http", server: "x", tenant: "apiprobe"}, suffix: "t"}
	for _, d := range append(p.setupDocs(), p.readDocs()...) {
		schema := servedSchema(t, d.area, schemaplane.MountTenant)
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) > 0 {
			t.Errorf("%s does not validate against %s: %v\n  document: %s", d.what, d.area, errs, d.doc)
		}
		if err := gqlcore.CheckWork(d.doc); err != nil {
			t.Errorf("%s exceeds the root-field ceiling: %v", d.what, err)
		}
	}
}
