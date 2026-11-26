// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/graphql/schemaplane"
	"github.com/devicechain-io/dc-microservice/rdb"
	graphql "github.com/graph-gophers/graphql-go"
)

func sp(s string) *string { return &s }

// okLine is a finished, evidence-settled line of a device that was not deleted.
func okLine(o int, basis string) bcLine {
	return bcLine{Ordinal: o, TargetToken: sp(bcDeviceToken("bcdrill", o)), Posting: "POSTED",
		Settlement: sp("CONFIRMED"), Prior: sp(`{"present":false,"generation":0}`), PostedValue: sp(`{"present":true}`),
		EvidenceBasis: sp(basis)}
}

func erasedLine(o int, posting string) bcLine {
	l := bcLine{Ordinal: o, Redacted: true, Posting: posting}
	if posting == "POSTED" {
		l.Settlement, l.Code = sp("AMBIGUOUS"), sp("TARGET_ERASED")
	} else {
		l.Code = sp("TARGET_GONE")
	}
	return l
}

func finished(count int, deleted map[int]string) []bcLine {
	var lines []bcLine
	for o := 1; o <= count; o++ {
		if p, ok := deleted[o]; ok {
			lines = append(lines, erasedLine(o, p))
			continue
		}
		lines = append(lines, okLine(o, "REPORT_LOG"))
	}
	return lines
}

// The verdict passes the finished state, and names each way a finished state can be wrong.
func TestJudgeLinesPassesAFinishedChangeAndRefusesEveryDefect(t *testing.T) {
	del := map[int]bool{2: true, 9: true}
	good := finished(10, map[int]string{2: "POSTED", 9: "SKIPPED"})
	if v := judgeLines(good, 10, del, true); len(v.Problems) != 0 || v.Settled != 8 || v.ReportLog != 8 || v.Erased != 2 {
		t.Fatalf("a finished change was not passed: %+v", v)
	}

	cases := map[string]func([]bcLine) []bcLine{
		"a line missing": func(l []bcLine) []bcLine { return l[:9] },
		"a line twice":   func(l []bcLine) []bcLine { return append(l, l[0]) },
		"a deleted device's line not redacted": func(l []bcLine) []bcLine {
			l[1].Redacted, l[1].TargetToken = false, sp("bcdrill-00002")
			return l
		},
		"a redacted line keeps its prior": func(l []bcLine) []bcLine { l[1].Prior = sp("{}"); return l },
		"a deleted device's posted line settled CONFIRMED": func(l []bcLine) []bcLine {
			l[1].Settlement = sp("CONFIRMED")
			return l
		},
		"a line still awaiting evidence": func(l []bcLine) []bcLine { l[0].Settlement = nil; return l },
		"a line that never posted":       func(l []bcLine) []bcLine { l[0].Posting = "PLANNED"; return l },
		"a line settled FAILED":          func(l []bcLine) []bcLine { l[0].Settlement = sp("FAILED"); return l },
		"a line settled AMBIGUOUS":       func(l []bcLine) []bcLine { l[0].Settlement = sp("AMBIGUOUS"); return l },
		"a settled line with no basis":   func(l []bcLine) []bcLine { l[0].EvidenceBasis = nil; return l },
		"a LATEST_STATE line where capture shipped": func(l []bcLine) []bcLine {
			l[0].EvidenceBasis = sp("LATEST_STATE")
			return l
		},
		"a surviving device's line redacted": func(l []bcLine) []bcLine { l[0].Redacted = true; return l },
	}
	for name, mutate := range cases {
		lines := mutate(finished(10, map[int]string{2: "POSTED", 9: "SKIPPED"}))
		if v := judgeLines(lines, 10, del, true); len(v.Problems) == 0 {
			t.Errorf("%s: the verdict passed it", name)
		}
	}

	// LATEST_STATE is allowed when the baseline did not capture, and still counted.
	lines := finished(4, nil)
	lines[0].EvidenceBasis = sp("LATEST_STATE")
	if v := judgeLines(lines, 4, nil, false); len(v.Problems) != 0 || v.LatestState != 1 || v.ReportLog != 3 {
		t.Fatalf("a LATEST_STATE line was refused without the capture requirement: %+v", v)
	}
	// A change in which nothing settled judged nothing.
	if v := judgeLines([]bcLine{erasedLine(1, "SKIPPED")}, 1, map[int]bool{1: true}, false); len(v.Problems) == 0 {
		t.Fatal("a change with no evidence-settled line was passed")
	}
}

func TestJudgeRedactionTellsRedactedFromNot(t *testing.T) {
	lines := []bcLine{erasedLine(3, "POSTED"), okLine(4, "REPORT_LOG")}
	if p := judgeRedaction(lines, []int{3}); len(p) != 0 {
		t.Fatalf("a redacted line was reported: %v", p)
	}
	if p := judgeRedaction(lines, []int{3, 4}); len(p) != 1 {
		t.Fatalf("an unredacted line was not reported exactly once: %v", p)
	}
	if p := judgeRedaction(lines, []int{7}); len(p) != 1 {
		t.Fatalf("a missing line was not reported: %v", p)
	}
}

func TestRedactedCommandAnswersBothExpectations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.json")
	body, _ := json.Marshal([]bcLine{erasedLine(3, "POSTED"), okLine(4, "REPORT_LOG")})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bcRedactedCmd([]string{"--lines", path, "--ordinals", "3"}, io.Discard); err != nil {
		t.Fatalf("a redacted line failed the redacted expectation: %v", err)
	}
	assertCode(t, bcRedactedCmd([]string{"--lines", path, "--ordinals", "4"}, io.Discard), exitMismatch)
	// The control: the line must be unredacted, and a redacted one makes the control fail.
	if err := bcRedactedCmd([]string{"--lines", path, "--ordinals", "4", "--expect", "unredacted"}, io.Discard); err != nil {
		t.Fatalf("an unredacted line failed the control's expectation: %v", err)
	}
	assertCode(t, bcRedactedCmd([]string{"--lines", path, "--ordinals", "3", "--expect", "unredacted"}, io.Discard), exitMismatch)
}

func priorRow(o int, prior, before string) bcPriorRow {
	return bcPriorRow{Ordinal: o, Prior: prior,
		Posted: `{"present":true,"valueType":"STRING","value":"bulk-1","generation":3}`,
		DocAt:  `{"bcdrill-mode":"bulk-1","bcdrill-warm":"w"}`, DocBefore: before}
}

func TestJudgePriorAgainstTheRevisionBeforeThePost(t *testing.T) {
	const k = "bcdrill-mode"
	correct := []bcPriorRow{
		priorRow(1, `{"present":true,"valueType":"STRING","value":"seed-1","generation":2}`, `{"bcdrill-mode":"seed-1","bcdrill-warm":"w"}`),
		priorRow(2, `{"present":false,"generation":0}`, `{"bcdrill-warm":"w"}`),
		priorRow(3, `{"present":false,"generation":0}`, ``), // no revision before the post at all
		priorRow(4, `{"present":true,"valueType":"STRING","value":"old-17","generation":5}`, `{"bcdrill-mode":"old-17"}`),
	}
	for _, r := range correct {
		if ok, why, unj := judgePrior(k, r); !ok || unj {
			t.Errorf("line %d: a correct prior was refused: %s", r.Ordinal, why)
		}
	}
	wrong := []bcPriorRow{
		// The H01 case: the post read ABSENT while an unprotected writer inserted the key.
		priorRow(1, `{"present":false,"generation":0}`, `{"bcdrill-mode":"old-9"}`),
		priorRow(2, `{"present":true,"valueType":"STRING","value":"seed-2","generation":2}`, `{"bcdrill-warm":"w"}`),
		priorRow(3, `{"present":true,"valueType":"STRING","value":"seed-3","generation":2}`, `{"bcdrill-mode":"old-4"}`),
	}
	for _, r := range wrong {
		if ok, _, unj := judgePrior(k, r); ok || unj {
			t.Errorf("line %d: a wrong prior was passed (ok=%v unjudgeable=%v)", r.Ordinal, ok, unj)
		}
	}
	// The post's own revision does not carry the posted value: nothing can be said.
	r := priorRow(9, `{"present":false,"generation":0}`, ``)
	r.DocAt = `{"bcdrill-mode":"something-else"}`
	if _, _, unj := judgePrior(k, r); !unj {
		t.Error("a line whose own revision does not carry its posted value was judged")
	}
	s := judgePriors(k, append(correct, wrong...))
	if s.Correct != 4 || s.Wrong != 3 || s.Unjudgeable != 0 {
		t.Fatalf("totals: %+v", s)
	}
}

func TestPriorRowsAreSplitOnTheUnitSeparator(t *testing.T) {
	in := "7\x1f{\"present\":false}\x1f{\"present\":true,\"value\":\"a\\tb\"}\x1f{\"k\":\"a\\tb\"}\x1f\r\n\n"
	rows, err := parsePriorRows(strings.NewReader(in))
	if err != nil || len(rows) != 1 || rows[0].Ordinal != 7 || rows[0].DocBefore != "" || rows[0].DocAt != `{"k":"a\tb"}` {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := parsePriorRows(strings.NewReader("7\x1fonly-two\n")); err == nil {
		t.Fatal("a short row was accepted")
	}
}

func TestPriorsCommandControlNeedsAWrongPrior(t *testing.T) {
	dir := t.TempDir()
	files := 0
	write := func(rows ...bcPriorRow) string {
		files++
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "%d\x1f%s\x1f%s\x1f%s\x1f%s\n", r.Ordinal, r.Prior, r.Posted, r.DocAt, r.DocBefore)
		}
		p := filepath.Join(dir, strconv.Itoa(files)+".tsv")
		if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write(priorRow(1, `{"present":false,"generation":0}`, `{"bcdrill-warm":"w"}`))
	bad := write(priorRow(1, `{"present":false,"generation":0}`, `{"bcdrill-mode":"old-1"}`))
	args := func(p, expect string) []string {
		return []string{"--rows", p, "--key", "bcdrill-mode", "--expect", expect}
	}
	if err := bcPriorsCmd(args(good, "correct"), io.Discard); err != nil {
		t.Fatalf("correct priors failed: %v", err)
	}
	assertCode(t, bcPriorsCmd(args(bad, "correct"), io.Discard), exitMismatch)
	if err := bcPriorsCmd(args(bad, "wrong"), io.Discard); err != nil {
		t.Fatalf("the control did not see its wrong prior: %v", err)
	}
	assertCode(t, bcPriorsCmd(args(good, "wrong"), io.Discard), exitMismatch)
	empty := write()
	assertCode(t, bcPriorsCmd(args(empty, "correct"), io.Discard), exitShape)
}

func TestWriteVerdictRequiresWritesAndTheExpectedAnswers(t *testing.T) {
	base := bcWriteResult{Attempted: 10, Accepted: 10, ModeWrites: 6, FirstWrites: 2}
	if err := bcWriteVerdict(base, "accepted"); err != nil {
		t.Fatalf("all accepted with first writes failed: %v", err)
	}
	refusedOne := base
	refusedOne.Accepted, refusedOne.Refused = 9, 1
	if bcWriteVerdict(refusedOne, "accepted") == nil {
		t.Error("one refused write passed the accepted expectation")
	}
	noFirst := base
	noFirst.FirstWrites = 0
	if bcWriteVerdict(noFirst, "accepted") == nil {
		t.Error("a run with no first write of an absent key passed")
	}
	allRefused := bcWriteResult{Attempted: 10, Refused: 10, ModeWrites: 6}
	if err := bcWriteVerdict(allRefused, "refused"); err != nil {
		t.Fatalf("all refused failed the refused expectation: %v", err)
	}
	if bcWriteVerdict(base, "refused") == nil {
		t.Error("accepted writes passed the refused expectation")
	}
	if bcWriteVerdict(bcWriteResult{}, "any") == nil {
		t.Error("a run with no writes passed")
	}
	if bcWriteVerdict(bcWriteResult{Attempted: 3, Accepted: 3}, "any") == nil {
		t.Error("a run with no write of the mode key passed")
	}
	if bcWriteVerdict(bcWriteResult{Attempted: 3, Accepted: 1, ModeWrites: 1}, "any") == nil {
		t.Error("a result whose counts do not add up passed")
	}
}

func TestReportPayloadIsAValidAppliedReport(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	b, err := bcReportPayload(bcReportRow{Token: "d", Revision: 4, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m["kind"] != "config.status" || m["status"] != "APPLIED" || m["revision"] != float64(4) {
		t.Fatalf("payload %s", b)
	}
	if _, err := bcReportPayload(bcReportRow{Token: "d", Revision: 4, Digest: "nope"}); err == nil {
		t.Fatal("a report with a malformed digest was built")
	}
	if _, err := bcReportPayload(bcReportRow{Token: "d", Revision: 0, Digest: digest}); err == nil {
		t.Fatal("a report for revision 0 was built")
	}
	rows, err := parseReportRows(strings.NewReader("d1\t3\t" + digest + "\r\n\nd2\t4\t" + digest + "\n"))
	if err != nil || len(rows) != 2 || rows[1].Revision != 4 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, err := parseReportRows(strings.NewReader("d1\tx\t" + digest)); err == nil {
		t.Fatal("a non-numeric revision was accepted")
	}
}

func TestPercentileIsNearestRank(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	s := summarizeLatency(d, 0, 0, 10*time.Second)
	if s.P50 != 50 || s.P99 != 99 || s.Max != 100 || s.Count != 100 || s.Rate != 10 {
		t.Fatalf("%+v", s)
	}
	if z := summarizeLatency(nil, 2, 0, time.Second); z.P99 != 0 || z.Errors != 2 {
		t.Fatalf("%+v", z)
	}
}

// bulkChangeSDL is the tenant schema device-management serves PLUS the bulk-change fragment while that
// fragment is still unserved, read from the Go literal exactly as dcctl's own document test reads it.
// Once the fragment moves into schema.graphql the literal is gone and the served schema alone is used,
// and it must then declare the operations itself.
func bulkChangeSDL(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "services", "device-management", "graphql")
	sdl, served, err := schemaplane.SDLAt(dir, schemaplane.MountTenant)
	if err != nil || !served {
		t.Fatalf("tenant schema: served=%v err=%v", served, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "fleet_operations_schema.go"), nil, 0)
	if os.IsNotExist(err) || (err == nil && !hasFragment(f)) {
		if !strings.Contains(sdl, "createBulkChange") {
			t.Fatal("the bulk-change fragment is gone and the served schema does not declare createBulkChange")
		}
		return sdl
	}
	if err != nil {
		t.Fatal(err)
	}
	var fragment string
	ast.Inspect(f, func(n ast.Node) bool {
		if v, ok := n.(*ast.ValueSpec); ok && len(v.Names) == 1 && v.Names[0].Name == "fleetOperationSchema" {
			lit, ok := v.Values[0].(*ast.BasicLit)
			if !ok {
				t.Fatal("the fragment is no longer a literal")
			}
			if fragment, err = strconv.Unquote(lit.Value); err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	return sdl + "\n" + fragment
}

func hasFragment(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if v, ok := n.(*ast.ValueSpec); ok && len(v.Names) == 1 && v.Names[0].Name == "fleetOperationSchema" {
			found = true
		}
		return !found
	})
	return found
}

// Every document the bulk-change drill sends, validated by the server's own validator.
func TestEveryBulkChangeDocumentValidates(t *testing.T) {
	schema, err := graphql.ParseSchema(bulkChangeSDL(t), nil, graphql.UseFieldResolvers())
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"token": "t", "name": "n", "kind": "config.set",
		"configSet":    map[string]any{"key": "k", "value": "v", "valueType": "STRING"},
		"deviceTokens": []any{"d"}, "safety": map[string]any{"maxPostsPerSecond": 50, "settleDeadlineSeconds": 3600}}
	docs := map[string]struct {
		doc  string
		vars map[string]any
	}{
		"create profile": {bcCreateProfileDoc, map[string]any{"req": map[string]any{"token": "p", "name": "n", "description": "d"}}},
		"declare": {bcDeclareDoc, map[string]any{"token": "p", "keys": []any{
			map[string]any{"key": "k", "valueType": "STRING", "description": "d"}}}},
		"publish":        {bcPublishDoc, map[string]any{"token": "p", "label": "l"}},
		"create type":    {bcCreateTypeDoc, map[string]any{"req": map[string]any{"token": "t", "name": "n", "profileToken": "p"}}},
		"create devices": {bcCreateDevicesDoc, map[string]any{"req": map[string]any{"deviceTypeToken": "t", "count": 2, "startIndex": 1, "tokenTemplate": "x-{n:05d}", "nameTemplate": "x {n}"}}},
		"set":            {attrWriteMutation, map[string]any{"req": map[string]any{"entityType": "device", "entity": "d", "scope": "SHARED", "attrKey": "k", "valueType": "STRING", "value": "v"}}},
		"unset":          {bcUnsetDoc, map[string]any{"entityType": "device", "entity": "d", "scope": "SHARED", "attrKey": "k"}},
		"delete device":  {bcDeleteDeviceDoc, map[string]any{"token": "d"}},
		"plan":           {bcPlanDoc, map[string]any{"request": request}},
		"create":         {bcCreateDoc, map[string]any{"request": request}},
		"start":          {bcStartDoc, map[string]any{"token": "t", "digest": "x"}},
		"status":         {bcStatusDoc, map[string]any{"token": "t"}},
		"abort":          {bcAbortDoc, map[string]any{"token": "t"}},
		"lines":          {bcLinesDoc, map[string]any{"token": "t", "criteria": map[string]any{"pageNumber": 1, "pageSize": 500}}},
	}
	for name, d := range docs {
		if errs := schema.ValidateWithVariables(d.doc, d.vars); len(errs) != 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// bcFakeDM is a device-management that answers the start sequence, recording what it was sent.
type bcFakeDM struct {
	admitted, refused int
	digest            string
	presented         atomic.Value
	createDispatch    string
}

func (f *bcFakeDM) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	change := func(dispatch string, n int) string {
		return fmt.Sprintf(`{"token":"t","dispatch":%q,"evidence":"OPEN","contractDigest":%q,"targetCount":%d,"evidenceCapability":{"reportLog":true,"brokerFailClosed":false},"progress":{}}`, dispatch, f.digest, n)
	}
	switch {
	case strings.Contains(req.Query, "planBulkChange"):
		_, _ = fmt.Fprintf(w, `{"data":{"plan":{"admittedCount":%d,"refusedCount":%d,"contractDigest":%q}}}`, f.admitted, f.refused, f.digest)
	case strings.Contains(req.Query, "createBulkChange"):
		_, _ = fmt.Fprintf(w, `{"data":{"change":%s}}`, change(f.createDispatch, f.admitted))
	case strings.Contains(req.Query, "startBulkChange"):
		f.presented.Store(fmt.Sprint(req.Variables["digest"]))
		_, _ = fmt.Fprintf(w, `{"data":{"change":%s}}`, change("RUNNING", f.admitted))
	default:
		_, _ = fmt.Fprint(w, `{"errors":[{"message":"unexpected"}]}`)
	}
}

func bcFixture(t *testing.T, dm http.HandlerFunc) (srvURL, receipt, bulk string) {
	t.Helper()
	um := &fakeInstance{t: t, selectOK: func(int) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user-management/graphql", um.userManagement)
	mux.HandleFunc("/dm/graphql", dm)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	receipt = filepath.Join(dir, "receipt.json")
	if err := writeReceipt(receipt, Receipt{Tenant: "apiprobe", Identity: Credential{Email: "probe@apiprobe.invalid", Password: "pw"},
		Written: time.Now(), Entities: []Recorded{{Name: "device", Area: "device-management", Token: "apiprobe-device"}}}); err != nil {
		t.Fatal(err)
	}
	b := bulkReceipt{Prefix: "bcdrill", ModeKey: "bcdrill-mode", WarmKey: "bcdrill-warm"}
	for n := 1; n <= 8; n++ {
		b.Devices = append(b.Devices, bcDeviceToken("bcdrill", n))
	}
	body, _ := json.Marshal(b)
	bulk = filepath.Join(dir, "bulk.json")
	if err := os.WriteFile(bulk, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.URL, receipt, bulk
}

func bcArgs(srvURL, receipt string, extra ...string) []string {
	return append([]string{"--receipt", receipt, "--server", strings.TrimPrefix(srvURL, "http://"), "--scheme", "http",
		"--url", srvURL + "/dm/graphql"}, extra...)
}

// start presents the PLAN's digest, and refuses a plan that did not admit every device.
func TestStartPresentsThePlansDigestAndRefusesAShortPlan(t *testing.T) {
	f := &bcFakeDM{admitted: 8, digest: "sha256:reviewed", createDispatch: "READY"}
	url, receipt, bulk := bcFixture(t, f.serve)
	var out bytes.Buffer
	if err := bcStart(context.Background(), bcArgs(url, receipt, "--bulk", bulk, "--token", "t", "--value", "v"), &out); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got, _ := f.presented.Load().(string); got != "sha256:reviewed" {
		t.Fatalf("start presented %q, want the plan's digest", got)
	}
	short := &bcFakeDM{admitted: 7, refused: 1, digest: "d", createDispatch: "READY"}
	url, receipt, bulk = bcFixture(t, short.serve)
	assertCode(t, bcStart(context.Background(), bcArgs(url, receipt, "--bulk", bulk, "--token", "t", "--value", "v"), io.Discard), exitMismatch)
	notReady := &bcFakeDM{admitted: 8, digest: "d", createDispatch: "RUNNING"}
	url, receipt, bulk = bcFixture(t, notReady.serve)
	assertCode(t, bcStart(context.Background(), bcArgs(url, receipt, "--bulk", bulk, "--token", "t", "--value", "v"), io.Discard), exitMismatch)
}

// oldwrite stops on the stop file, classifies every answer, and judges the run against its expectation.
func TestOldWriteClassifiesAnswersAgainstTheExpectation(t *testing.T) {
	run := func(t *testing.T, refuse bool, expect string) (bcWriteResult, error) {
		var n atomic.Int32
		url, receipt, bulk := bcFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) > 40 {
				time.Sleep(5 * time.Millisecond)
			}
			if refuse {
				_, _ = fmt.Fprint(w, `{"errors":[{"message":"internal error"}]}`)
				return
			}
			writeOK(w)
		})
		dir := t.TempDir()
		modeFile, warmFile := filepath.Join(dir, "mode"), filepath.Join(dir, "warm")
		_ = os.WriteFile(modeFile, []byte("bcdrill-00004\nbcdrill-00005\n"), 0o600) // 4 starts without the key
		_ = os.WriteFile(warmFile, []byte("bcdrill-00006\n"), 0o600)
		stop, out := filepath.Join(dir, "stop"), filepath.Join(dir, "out.json")
		go func() {
			for n.Load() < 30 {
				time.Sleep(time.Millisecond)
			}
			_ = os.WriteFile(stop, nil, 0o600)
		}()
		err := bcOldWrite(context.Background(), bcArgs(url, receipt, "--bulk", bulk, "--mode-devices", modeFile,
			"--warm-devices", warmFile, "--expect", expect, "--stop-file", stop, "--out", out, "--workers", "3",
			"--max-duration", "30s"), io.Discard)
		var res bcWriteResult
		body, rerr := os.ReadFile(out)
		if rerr != nil {
			t.Fatalf("no result written: %v", rerr)
		}
		_ = json.Unmarshal(body, &res)
		return res, err
	}
	res, err := run(t, false, "accepted")
	if err != nil || res.Accepted == 0 || res.FirstWrites == 0 || res.Refused != 0 {
		t.Fatalf("accepted run: %+v err=%v", res, err)
	}
	_, err = run(t, false, "refused")
	assertCode(t, err, exitMismatch)
	res, err = run(t, true, "refused")
	if err != nil || res.Refused == 0 || res.Accepted != 0 {
		t.Fatalf("refused run: %+v err=%v", res, err)
	}
	_, err = run(t, true, "accepted")
	assertCode(t, err, exitMismatch)
}

func TestDeleteAnsweringFalseIsMissing(t *testing.T) {
	url, receipt, _ := bcFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"deleteDevice":false}}`)
	})
	assertCode(t, bcDelete(context.Background(), bcArgs(url, receipt, "--device", "x"), io.Discard), exitMissing)
}

func TestBulkReceiptRules(t *testing.T) {
	for n, want := range map[int]bool{1: true, 3: true, 4: false, 8: false, 9: true} {
		if bcModeSeeded(n) != want {
			t.Errorf("bcModeSeeded(%d) = %v", n, !want)
		}
	}
	if bcDeviceToken("bcdrill", 7) != "bcdrill-00007" {
		t.Fatal(bcDeviceToken("bcdrill", 7))
	}
	for s, want := range map[string]bool{"bcdrill": true, "p1-cap": true, "": false, "Upper": false, "a b": false, strings.Repeat("a", 41): false} {
		if validToken(s) != want {
			t.Errorf("validToken(%q) = %v", s, !want)
		}
	}
	if o, err := parseOrdinals(" 9,3 ,,1"); err != nil || fmt.Sprint(o) != "[1 3 9]" {
		t.Fatalf("%v %v", o, err)
	}
	if _, err := parseOrdinals("0"); err == nil {
		t.Fatal("ordinal 0 was accepted")
	}
}

func TestLockKeyIsThePlatformsOwn(t *testing.T) {
	var out bytes.Buffer
	if err := bcLockKey([]string{"--name", "device-management-bulk-change-maintenance"}, &out); err != nil {
		t.Fatal(err)
	}
	// FNV-1a 64 of the name, as a signed 64-bit integer: what pg_try_advisory_lock is handed.
	if got := strings.TrimSpace(out.String()); got != strconv.FormatInt(rdb.AdvisoryLockKey("device-management-bulk-change-maintenance"), 10) {
		t.Fatalf("lockkey printed %s", got)
	}
	assertCode(t, bcLockKey(nil, io.Discard), exitSetup)
}

// The maintenance pass still takes its singleton lock under the name the rig's control holds. A rename
// would leave the control holding a lock nothing asks for, and the repair would run straight through it.
func TestTheMaintenancePassLockNameIsTheOneTheRigHolds(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "services", "device-management", "model", "api_fleet_retention.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `SingletonPass(ctx, "device-management-bulk-change-maintenance"`) {
		t.Fatal("the bulk-change maintenance pass no longer takes the lock the upgrade drill's repair control holds")
	}
}
