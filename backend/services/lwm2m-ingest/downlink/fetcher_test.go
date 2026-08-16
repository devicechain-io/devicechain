// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// fakeQuerier is a stand-in for svcclient.Client: it captures the query arguments and
// returns a canned set of rows.
//
// It keeps the WHOLE variables map and the query TEXT, not just the fields a particular
// assertion wants. A fake that copies out only the fields it knows about cannot see a field
// the caller stopped sending (or started sending under a new name) — the omission looks like
// a passing test.
//
// 🔴 It returns f.rows VERBATIM, in the order given, and that is deliberate: it cannot
// simulate a server-side ORDER BY. So no test here may claim to prove ordering — the
// oldest-first guarantee is command-delivery's, tested against a real database in its own
// model package. What this fake CAN prove is that the fetcher does not re-order what it is
// handed, which is exactly the property this layer now owns.
type fakeQuerier struct {
	rows []drainRow
	err  error

	gotTenant  string
	gotBaseURL string
	gotQuery   string
	gotVars    map[string]any
}

func (f *fakeQuerier) Query(ctx context.Context, baseURL, tenant, query string, vars map[string]any, out any) error {
	if f.err != nil {
		return f.err
	}
	f.gotTenant = tenant
	f.gotBaseURL = baseURL
	f.gotQuery = query
	f.gotVars = vars
	resp := out.(*drainResponse)
	resp.DrainableCommands = f.rows
	return nil
}

func strp(s string) *string { return &s }

// TestPendingPreservesServerOrder is what remains of the FOTA-ordering guard on THIS side of
// the wire. The oldest-first guarantee is now command-delivery's — drainableCommands ORDERs in
// the database — and is tested there, against a real one. What this layer owes is that it does
// not disturb that order: a firmware Write /5/0/1 handed to us before its Execute /5/0/2 must
// still come out in that order.
//
// The fixture is deliberately ANTI-SORTED by every proxy a re-introduced client sort could
// reach for (token, name), so a fetcher that sorted anything at all would fail here. It carries
// no ids at all — drainRow has none any more, which is the structural reason no sort can come
// back by accident.
func TestPendingPreservesServerOrder(t *testing.T) {
	// The order the SERVER chose (oldest first). Note it is neither token- nor name-ascending.
	q := &fakeQuerier{rows: []drainRow{
		{Token: "zeta", Name: "lwm2m.write", Payload: strp(`{"path":"/5/0/1","value":"coaps://fw"}`), Status: "PARKED"},
		{Token: "alpha", Name: "lwm2m.execute", Payload: strp(`{"path":"/5/0/2"}`), Status: "PARKED"},
		{Token: "mid", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "HELD"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "tenantA", "dev-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	wantOrder := []string{"zeta", "alpha", "mid"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d commands, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].Token != w {
			t.Fatalf("position %d: got token %q, want %q — the server's order was not preserved", i, got[i].Token, w)
		}
	}
	// Payload passes through as raw JSON bytes for the executor.
	if string(got[0].Payload) != `{"path":"/5/0/1","value":"coaps://fw"}` {
		t.Fatalf("payload not preserved: %q", string(got[0].Payload))
	}
	// Per-tenant scope rides on the client call, not the variables.
	if q.gotTenant != "tenantA" || q.gotBaseURL != "http://cd/graphql" {
		t.Fatalf("query args = tenant %q baseURL %q", q.gotTenant, q.gotBaseURL)
	}
}

// TestPendingCapsAtPerWakeMax proves the per-wake dispatch cap holds even if the server ever
// returned more rows than the `limit` it was sent (contract drift): a constrained radio must
// not take an unbounded burst at wake. The selection being the OLDEST N is the server's job —
// this fake cannot simulate an ORDER BY — so this asserts the COUNT and the pass-through
// order, nothing about which rows the database would have chosen.
func TestPendingCapsAtPerWakeMax(t *testing.T) {
	n := maxDrainPerWake + 5
	rows := make([]drainRow, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, drainRow{
			Token: "c" + strconv.Itoa(i), Name: "lwm2m.read",
			Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED",
		})
	}
	f := NewCommandFetcher(&fakeQuerier{rows: rows}, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != maxDrainPerWake {
		t.Fatalf("got %d, want the per-wake cap %d", len(got), maxDrainPerWake)
	}
	for i := 0; i < maxDrainPerWake; i++ {
		want := "c" + strconv.Itoa(i+1)
		if got[i].Token != want {
			t.Fatalf("position %d: got %q, want %q — the cap must take the HEAD of the server's page", i, got[i].Token, want)
		}
	}
}

// TestPendingCallsDrainableCommandsOnTheWire is the WIRE test for the drain query. It asserts
// on the query text and the variables map actually handed to the GraphQL client, against
// LITERAL strings — never against this package's own constants, because a test that compares a
// constant to itself passes no matter what either side is renamed to and proves only that Go
// assignment works.
//
// What it is defending: the drain's contract with command-delivery is now the dedicated
// `drainableCommands(deviceToken:, limit:)` query, which carries the status set, the expiry
// horizon, the ordering and the bound in the DATABASE. The forked graphql-go rejects an
// unknown field arriving through a variable, so a renamed argument fails loudly — but only if
// this package actually sends the argument by the agreed NAME.
//
// 🔴 The variable set is asserted EXHAUSTIVELY (its length, not just the presence of the two
// it wants). A non-exhaustive assertion would pass while a leftover `criteria`, `pageSize` or
// `statuses` rode along beside the new arguments — a rejected query for every waking device,
// discovered as "the drain broke".
func TestPendingCallsDrainableCommandsOnTheWire(t *testing.T) {
	q := &fakeQuerier{}
	f := NewCommandFetcher(q, "http://cd/graphql")

	if _, err := f.Pending(context.Background(), "tenantA", "dev-1"); err != nil {
		t.Fatalf("Pending: %v", err)
	}
	// The operation itself: the dedicated drain query, not the general paged `commands` one.
	if !strings.Contains(q.gotQuery, "drainableCommands(") {
		t.Fatalf("query does not call drainableCommands:\n%s", q.gotQuery)
	}
	// drainableCommands returns a BARE list. A `results` selection would mean the query was
	// written against the paged shape and every drain would fail at the server.
	if strings.Contains(q.gotQuery, "results") || strings.Contains(q.gotQuery, "criteria") {
		t.Fatalf("query still carries the paged `commands` shape (results/criteria):\n%s", q.gotQuery)
	}
	if q.gotVars == nil {
		t.Fatal("no variables were sent")
	}
	// Exhaustive: exactly deviceToken + limit, nothing else. The old paged variables
	// (criteria/pageNumber/pageSize/status/statuses) must be GONE, not merely unread — the
	// forked graphql-go rejects an argument the schema does not declare.
	if len(q.gotVars) != 2 {
		t.Fatalf("variables = %#v, want exactly deviceToken + limit", q.gotVars)
	}
	for _, stale := range []string{"criteria", "pageNumber", "pageSize", "status", "statuses"} {
		if _, ok := q.gotVars[stale]; ok {
			t.Fatalf("variables still carry the paged-query field %q: %#v", stale, q.gotVars)
		}
	}
	if dev, ok := q.gotVars["deviceToken"].(string); !ok || dev != "dev-1" {
		t.Fatalf("deviceToken = %#v, want the string \"dev-1\"", q.gotVars["deviceToken"])
	}
	// The limit sent IS the per-wake dispatch cap: there is no over-fetch any more, because the
	// server returns the oldest N rather than an arbitrary N.
	lim, ok := q.gotVars["limit"].(int)
	if !ok {
		t.Fatalf("limit is %T, want int (the schema declares Int)", q.gotVars["limit"])
	}
	if lim != maxDrainPerWake {
		t.Fatalf("limit = %d, want the per-wake cap %d", lim, maxDrainPerWake)
	}
	if lim != 32 {
		t.Fatalf("the per-wake cap is %d; the device-edge flood governor is 32", lim)
	}
}

// TestDrainableAcceptsExactlyHeldAndParked pins the status SET this package will dispatch out
// of, against LITERAL strings — deliberately not read from statusHeld / statusParked /
// drainStatuses, because a test that compares a constant to itself proves only that Go
// assignment works.
//
// The set used to travel on the wire as query criteria; the server owns that predicate now,
// so what remains here is the DEFENSIVE re-check — the thing standing between a drifted
// contract and a terminal command re-actuating a device.
//
// 🔴 SENT MUST NOT BE DRAINABLE. It used to be, and dispatching out of it without a claim was
// the platform's only unclaimed dispatch — a command that went nowhere and one the device
// genuinely holds were the same status, so the drain could not tell them apart. The first kind
// is PARKED now. Draining a SENT row would mean re-dispatching a command the device may
// already be running.
func TestDrainableAcceptsExactlyHeldAndParked(t *testing.T) {
	for _, s := range []string{"HELD", "PARKED"} {
		if !drainable(s) {
			t.Fatalf("%q is not drainable — a device's %s backlog would never drain", s, s)
		}
	}
	for _, s := range []string{"SENT", "QUEUED", "SUCCESSFUL", "FAILED", "TIMEOUT", ""} {
		if drainable(s) {
			t.Fatalf("%q is drainable, which risks re-actuating a device", s)
		}
	}
	if len(drainStatuses) != 2 {
		t.Fatalf("drainStatuses = %v, want exactly [HELD PARKED]", drainStatuses)
	}
}

// TestPendingDrainsHeldAndParkedCarryingStatus proves both halves of the backlog come through
// AND that each row's status rides on the DrainCommand.
//
// What a dropped status would cost is DIAGNOSIS, not delivery. Nothing branches on it — every
// drained row is claimed either way — but it is the field the no-claimer refusal logs, so
// losing it turns "which half of this backlog went undispatched" into a question the logs
// cannot answer, and no test of the dispatcher alone can see the field go missing.
//
// An earlier version of this comment justified the assertion differently: that the dispatcher
// reads the status to decide whether a claim is required, so dropping it would stall a whole
// fleet. That was true of the pre-PARKED claim EXEMPTION and has not been true since it was
// removed. The assertions below are unchanged; only the reason for them is.
func TestPendingDrainsHeldAndParkedCarryingStatus(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Token: "held", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "HELD"},
		{Token: "parked", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commands, want both the HELD and the PARKED row: %+v", len(got), got)
	}
	if got[0].Token != "held" || got[0].Status != "HELD" {
		t.Fatalf("row 0 = %+v, want the HELD command carrying its status", got[0])
	}
	if got[1].Token != "parked" || got[1].Status != "PARKED" {
		t.Fatalf("row 1 = %+v, want the PARKED command carrying its status", got[1])
	}
}

// TestPendingDropsNonDrainableStatus is the defensive guard: even if the server ever returned
// a row outside the drain's status set (contract drift), the fetcher must never hand a
// terminal command to dispatch (re-actuation). SUCCESSFUL is the case that matters — a
// command the device already answered.
//
// This drops rows, so it also trips the drift warn — deliberately uncaptured here, because
// what it owes is the SKIP. The warn's own properties (one line, the status names, the
// surviving rows) are asserted in TestPendingWarnsOnceNamingTheDriftedStatuses below.
func TestPendingDropsNonDrainableStatus(t *testing.T) {
	q := &fakeQuerier{rows: []drainRow{
		{Token: "done", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "SUCCESSFUL"},
		{Token: "live", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
		{Token: "queued", Name: "lwm2m.write", Payload: strp(`{"path":"/3/0/0"}`), Status: "QUEUED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "t", "d")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	// QUEUED is excluded on purpose as well: command-delivery's own sweep will publish it to
	// the LIVE path within a tick, so draining it here too would double-dispatch.
	if len(got) != 1 || got[0].Token != "live" {
		t.Fatalf("got %+v, want only the drainable (PARKED) command", got)
	}
}

// --- drift detection: the defensive guard must be AUDIBLE ---------------------
//
// 🔴 THE INVARIANT THESE TWO TESTS STAND IN FOR CANNOT BE COMPILED. statusHeld/statusParked
// are declared as literals because downlink does not import command-delivery, so nothing
// compares them to CommandHeld/CommandParked and nothing can. If command-delivery ever renamed
// a wire value, drainableCommands would return the renamed rows, drainable() would reject every
// one, and Pending would return an empty slice: a device whose entire backlog silently stops
// being delivered, indistinguishable from a device with nothing queued.
//
// The rejection is therefore reported, not just performed. Both directions are asserted below,
// and the SILENT one is not optional — without it, a warn wired to fire unconditionally would
// pass the loud test exactly as well as a correct one.

// drainLogBuffer captures the global zerolog output for the duration of a test. It is
// synchronized even though Pending is called from a single goroutine here: the drain runs on
// shard workers in production, and an instrument that is only safe in the test that first used
// it is the one that goes racy the day it is reused.
type drainLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *drainLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *drainLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// records returns the decoded log lines whose message contains want. It matches on the
// message's own distinctive phrase rather than on the level, so an unrelated warning from
// elsewhere cannot inflate the count and make a per-row warn look aggregated.
func (b *drainLogBuffer) records(want string) []map[string]any {
	var found []map[string]any
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["message"].(string); strings.Contains(msg, want) {
			found = append(found, rec)
		}
	}
	return found
}

// captureDrainLogs redirects the global logger into a buffer for the duration of a test.
//
// 🔴 It also FORCES the global level, which is not belt-and-braces. zerolog's global level is
// process-wide state any suite can leave Disabled, and a muted logger is the worst possible
// state for a test that counts log lines: "the warning fired zero times" is precisely what the
// control below asserts, so a muted logger reads as a PASS on the half of this pair that exists
// to catch an unconditional warn. Setting it here makes the instrument's state a property of
// this test rather than of whichever test ran before it.
func captureDrainLogs(t *testing.T) *drainLogBuffer {
	t.Helper()
	buf := &drainLogBuffer{}
	prevLogger, prevLevel := log.Logger, zerolog.GlobalLevel()
	log.Logger = zerolog.New(buf)
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() {
		log.Logger = prevLogger
		zerolog.SetGlobalLevel(prevLevel)
	})
	return buf
}

// driftWarnPhrase is the message fragment the drift warn is found by. It is a LITERAL, not a
// slice of the production string, because a test that reads the message off the code it is
// testing agrees with any rewording, including one that drops the point.
const driftWarnPhrase = "does not recognise as drainable"

// TestPendingWarnsOnceNamingTheDriftedStatuses is the loud half. A row reaching the
// drainable() guard means this build's status set and command-delivery's drainableCommands
// predicate disagree — the server applies the same predicate before returning a row, so there
// is no benign cause — and the whole failure mode is that the resulting non-delivery looks
// like an empty queue.
//
// Three properties are asserted, and each is a different way the report could be useless:
// ONE line (not one per row — on real drift this fires for every device on the instance, and a
// per-row warn buries the signal in itself), the STATUS NAMES (the only datum that says which
// side renamed what), and the surviving rows (drift must not cost the commands this build does
// understand).
func TestPendingWarnsOnceNamingTheDriftedStatuses(t *testing.T) {
	logs := captureDrainLogs(t)
	// Two distinct unknown statuses across three rows, so a per-row warn and a per-status warn
	// are both distinguishable from the one aggregated line, and so the dedupe of the reported
	// names is exercised rather than assumed.
	q := &fakeQuerier{rows: []drainRow{
		{Token: "keep-1", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "HELD"},
		{Token: "drift-1", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "STASHED"},
		{Token: "drift-2", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "STASHED"},
		{Token: "drift-3", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "WITHHELD"},
		{Token: "keep-2", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "tenantA", "dev-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	// Fail closed, not shut: the unknown rows are skipped and the understood ones still ship.
	// Returning an error instead would stop delivering the rows this build does recognise,
	// trading a partial outage for a total one.
	if len(got) != 2 || got[0].Token != "keep-1" || got[1].Token != "keep-2" {
		t.Fatalf("got %+v, want the HELD and PARKED rows from the SAME response to survive the drift", got)
	}

	recs := logs.records(driftWarnPhrase)
	if len(recs) != 1 {
		t.Fatalf("got %d drift warnings for 3 dropped rows, want exactly 1 aggregated line per "+
			"Pending call; real drift fires on every waking device at once, so a per-row warn "+
			"buries the signal in itself\n%s", len(recs), logs.String())
	}
	rec := recs[0]
	if lvl, _ := rec["level"].(string); lvl != "warn" {
		t.Errorf("the drift record is at level %q, want warn — a debug line here is not audible, "+
			"which is the entire defect being fixed", lvl)
	}
	if v, _ := rec["tenant"].(string); v != "tenantA" {
		t.Errorf("tenant = %q, want tenantA — without it an operator cannot tell whose fleet stalled", v)
	}
	if v, _ := rec["device"].(string); v != "dev-1" {
		t.Errorf("device = %q, want dev-1 — the device whose backlog was not delivered", v)
	}
	if v, _ := rec["dropped"].(float64); int(v) != 3 {
		t.Errorf("dropped = %v, want 3 — the count is what says how much of the backlog went nowhere", rec["dropped"])
	}
	// The offending NAMES, deduped. This is the whole diagnostic: "PARKED became WITHHELD" is
	// readable from the log alone, whereas a count says only that something is wrong.
	raw, ok := rec["statuses"].([]any)
	if !ok {
		t.Fatalf("statuses = %#v, want the list of offending status names", rec["statuses"])
	}
	var names []string
	for _, s := range raw {
		names = append(names, fmt.Sprint(s))
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "STASHED" || names[1] != "WITHHELD" {
		t.Errorf("statuses = %v, want exactly [STASHED WITHHELD] — each offending name once, "+
			"not once per row", names)
	}
}

// TestPendingIsSilentWhenEveryRowIsDrainable is the CONTROL, and it is what makes the test
// above mean anything: a warn wired to fire on every call would satisfy every assertion there.
// The overwhelmingly common wake — a backlog entirely within the understood status set — must
// produce no log line at all, or the drift warn is noise an operator learns to scroll past.
//
// It asserts the buffer is EMPTY rather than merely free of the drift phrase. Pending logs
// nothing on a clean path today, and asserting the stronger property is what catches a warn
// reworded past driftWarnPhrase. If a legitimate log line is ever added to Pending, narrow this
// to records(driftWarnPhrase) — do not delete it.
func TestPendingIsSilentWhenEveryRowIsDrainable(t *testing.T) {
	logs := captureDrainLogs(t)
	q := &fakeQuerier{rows: []drainRow{
		{Token: "held", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "HELD"},
		{Token: "parked", Name: "lwm2m.read", Payload: strp(`{"path":"/3/0/0"}`), Status: "PARKED"},
	}}
	f := NewCommandFetcher(q, "http://cd/graphql")

	got, err := f.Pending(context.Background(), "tenantA", "dev-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commands, want both drainable rows — the control proves nothing if the "+
			"call under it did no work", len(got))
	}
	if s := logs.String(); s != "" {
		t.Errorf("a drain with nothing to complain about logged:\n%s\nA drift warn that fires on a "+
			"clean wake is indistinguishable from no warn at all, because it fires on every wake", s)
	}
}

// TestPendingPropagatesError proves a fetch failure surfaces (the drain skips + retries
// next wake) rather than silently returning an empty queue.
func TestPendingPropagatesError(t *testing.T) {
	q := &fakeQuerier{err: errors.New("command-delivery unreachable")}
	f := NewCommandFetcher(q, "http://cd/graphql")

	_, err := f.Pending(context.Background(), "t", "d")
	if err == nil {
		t.Fatalf("expected the query error to propagate")
	}
}

// TestPendingEmptyIsNotAnError proves the common case — a device with nothing pending —
// returns an empty slice, so a wake with no queue is silent, not a logged failure.
func TestPendingEmptyIsNotAnError(t *testing.T) {
	f := NewCommandFetcher(&fakeQuerier{rows: nil}, "http://cd/graphql")
	got, err := f.Pending(context.Background(), "t", "d")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %d cmds, err %v; want 0/nil", len(got), err)
	}
}
