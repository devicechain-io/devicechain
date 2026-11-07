// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package physpr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	graphql "github.com/graph-gophers/graphql-go"
)

type recordedCall struct {
	query string
	vars  map[string]any
}

type recorder struct {
	calls []recordedCall
	reply string
}

func (r *recorder) Query(_ context.Context, _ string, q string, v map[string]any, out any) error {
	r.calls = append(r.calls, recordedCall{q, v})
	if r.reply == "" {
		return nil
	}
	return json.Unmarshal([]byte(r.reply), out)
}

const rule = `{"id":"r","name":"hot","type":"threshold","when":{"metric":"temperature","op":"gt","threshold":80}}`

func writeLedger(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "l.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := LoadLedger(writeLedger(t, `{"profile":"pump","versions":[
	 {"commit":"v1","author":"alice","publishedAt":"2026-10-01T00:00:00Z","ruleDefinition":`+rule+`},
	 {"commit":"v2","author":"agent:tuner","authorKind":"agent","message":"tune","publishedAt":"2026-10-02T00:00:00Z","ruleDefinition":`+rule+`}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerRejectsWhatWouldMisleadAReplay(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":  `{"profile":"p","versions":[],"oops":1}`,
		"no profile":   `{"versions":[{"commit":"a","author":"x","publishedAt":"2026-10-01T00:00:00Z","ruleDefinition":{}}]}`,
		"no versions":  `{"profile":"p","versions":[]}`,
		"no author":    `{"profile":"p","versions":[{"commit":"a","publishedAt":"2026-10-01T00:00:00Z","ruleDefinition":{}}]}`,
		"no rule":      `{"profile":"p","versions":[{"commit":"a","author":"x","publishedAt":"2026-10-01T00:00:00Z"}]}`,
		"bad time":     `{"profile":"p","versions":[{"commit":"a","author":"x","publishedAt":"soon","ruleDefinition":{}}]}`,
		"duplicate id": `{"profile":"p","versions":[{"commit":"a","author":"x","publishedAt":"2026-10-01T00:00:00Z","ruleDefinition":{}},{"commit":"a","author":"x","publishedAt":"2026-10-01T00:00:00Z","ruleDefinition":{}}]}`,
	} {
		if _, err := LoadLedger(writeLedger(t, body)); err == nil {
			t.Errorf("%s: a ledger like this must be refused", name)
		}
	}
	if _, err := LoadLedger(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestFindNamesTheValidCommits(t *testing.T) {
	l := goodLedger(t)
	if v, err := l.Find("v2"); err != nil || v.Author != "agent:tuner" {
		t.Fatalf("Find(v2) = %+v, %v", v, err)
	}
	if _, err := l.Find("v9"); err == nil || !strings.Contains(err.Error(), "v1, v2") {
		t.Fatalf("an unknown commit must list the known ones, got %v", err)
	}
}

func TestDiffSendsBothSidesAndReturnsTheReport(t *testing.T) {
	r := &recorder{reply: `{"experimentalPreviewRuleDiff":{"ok":true,"empty":false,"report":"R","summary":{"alarmsAdded":3}}}`}
	l := goodLedger(t)
	res, err := Diff(context.Background(), r, DiffOptions{Endpoint: "e", Profile: "pump",
		Base: SideOf(l.Versions[0]), Head: SideOf(l.Versions[1]),
		Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), ShiftSeconds: 600})
	if err != nil || res.Report != "R" || res.Summary.AlarmsAdded != 3 {
		t.Fatalf("got %+v, %v", res, err)
	}
	in := r.calls[0].vars["input"].(map[string]any)
	if in["shiftWindowSeconds"] != 600 || in["base"].(map[string]any)["author"] != "alice" {
		t.Fatalf("input = %+v", in)
	}
}

func TestRejectionBecomesAnError(t *testing.T) {
	r := &recorder{reply: `{"experimentalPreviewRuleDiff":{"ok":false,"diagnostics":[{"severity":"error","message":"head: bad rule"}]}}`}
	if _, err := Diff(context.Background(), r, DiffOptions{}); err == nil || !strings.Contains(err.Error(), "head: bad rule") {
		t.Fatalf("want the server's reason, got %v", err)
	}
}

func servedSchema(t *testing.T) *graphql.Schema {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "event-processing", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("classify %s: served=%v err=%v", dir, served, err)
	}
	schema, err := graphql.ParseSchema(sdl, nil, graphql.UseFieldResolvers(), graphql.MaxDepth(15), graphql.MaxQueryLength(100000))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return schema
}

// The documents are strings and the schema lives in another module, so they are validated against
// the served SDL with the exact variables the code paths send. CI runs with -count=1.
func TestDocumentsValidateAgainstTheServedSchema(t *testing.T) {
	schema := servedSchema(t)
	r := &recorder{}
	l := goodLedger(t)
	l.Versions[0].Message = "m"
	l.Versions[0].AuthorKind = "human"
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	_, _ = Diff(context.Background(), r, DiffOptions{Profile: "pump", Base: SideOf(l.Versions[0]), Head: SideOf(l.Versions[1]),
		Start: now.Add(-time.Hour), End: now, ShiftSeconds: 60})
	_, _ = Bisect(context.Background(), r, BisectOptions{Ledger: l, Series: "pump-1", At: now, SlackSeconds: 60, LookbackHours: 6})
	if len(r.calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(r.calls))
	}
	for _, c := range r.calls {
		if errs := schema.ValidateWithVariables(c.query, c.vars); len(errs) > 0 {
			t.Errorf("%s does not validate: %v", c.query, errs)
		}
	}
	// NEGATIVE CONTROL: a misnamed selection and a misnamed input key must be rejected by name.
	bad := strings.Replace(diffQuery, "alarmsMoved", "alarmsMovedX", 1)
	if errs := schema.ValidateWithVariables(bad, r.calls[0].vars); len(errs) == 0 || !strings.Contains(errs[0].Message, "alarmsMovedX") {
		t.Errorf("a misnamed field must be rejected by name, got %v", errs)
	}
	vars := map[string]any{"input": map[string]any{"profileToken": "p", "base": map[string]any{}, "head": map[string]any{}, "start": "a", "end": "b", "shiftWindow": 1}}
	if errs := schema.ValidateWithVariables(diffQuery, vars); len(errs) == 0 || !strings.Contains(errs[0].Message, "shiftWindow") {
		t.Errorf("a misnamed input key must be rejected by name, got %v", errs)
	}
}
