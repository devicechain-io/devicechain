// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

// idBase is the instant every test identity is offset from.
var idBase = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC).UnixMicro()

// The four identities of the issue's example, A to D, a millisecond apart.
var (
	idA = idBase + 1000
	idB = idBase + 2000
	idC = idBase + 3000
	idD = idBase + 4000
)

// key renders an identity the way the report's samples do.
func key(dev string, us int64) string { return IdentityKey{Device: dev, OccurredMicros: us}.String() }

// ledgerOfIDs builds a ledger snapshot whose counters agree with its identities.
func ledgerOfIDs(devices map[string]sim.DeviceIdentities) sim.IdentitySnapshot {
	s := sim.IdentitySnapshot{Devices: devices}
	s.All = s.Measurements()
	return s
}

// readOf wraps stored rows as a complete, stable read.
func readOf(times map[string][]int64) storedIdentities {
	return storedIdentities{Times: times, Pages: len(times)}
}

// total is the number of stored rows.
func total(times map[string][]int64) int64 {
	var n int64
	for _, t := range times {
		n += int64(len(t))
	}
	return n
}

// 🔴 The issue's case, by value. Accepting A,B,C,D and storing A,A,C,D leaves the totals
// equal, and the count reconcile passes every invariant on it; the identity
// reconciliation must fail it naming B missing and A stored twice. The pair is asserted
// in one test so it pins both halves: the count is blind here, identity is not.
func TestIdentityEqualTotalLossAndDuplicate(t *testing.T) {
	for _, inv := range Reconcile(4, 0, 4, 4) {
		if !inv.Passed {
			t.Fatalf("the count pre-check should pass equal totals (that is the blind spot): %s failed: %s", inv.Name, inv.Detail)
		}
	}

	led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB, idC, idD}}})
	stored := map[string][]int64{"d1": {idA, idA, idC, idD}}
	rep, inv := ReconcileIdentity(led, readOf(stored), 4, sim.Snapshot{Emitted: 4}, []string{"d1"})

	if inv.Passed || rep.Reconciled || inv.Name != InvIdentity {
		t.Fatalf("identity passed a loss offset by a duplicate: %+v", inv)
	}
	if rep.Missing != 1 || !reflect.DeepEqual(rep.Samples.Missing, []string{key("d1", idB)}) {
		t.Errorf("missing = %d %v; want 1 [%s]", rep.Missing, rep.Samples.Missing, key("d1", idB))
	}
	if rep.DuplicateKeys != 1 || rep.ExtraCopies != 1 || !reflect.DeepEqual(rep.Samples.Duplicate, []string{key("d1", idA) + " x2"}) {
		t.Errorf("duplicates = %d keys, %d extra, %v; want 1, 1, [%s x2]", rep.DuplicateKeys, rep.ExtraCopies, rep.Samples.Duplicate, key("d1", idA))
	}
	if rep.Unexpected != 0 || rep.Unattributed != 0 || len(rep.Inconclusive) != 0 {
		t.Errorf("unexpected %d, unattributed %d, inconclusive %v; want none", rep.Unexpected, rep.Unattributed, rep.Inconclusive)
	}
	if rep.Accepted != 4 || rep.Persisted != 4 || rep.TenantTotal != 4 {
		t.Errorf("accepted %d persisted %d tenant total %d; want 4/4/4", rep.Accepted, rep.Persisted, rep.TenantTotal)
	}
	for _, want := range []string{"1 missing", key("d1", idB), "1 duplicated", key("d1", idA) + " x2"} {
		if !strings.Contains(inv.Detail, want) {
			t.Errorf("detail %q does not carry %q", inv.Detail, want)
		}
	}
}

// A row that should not be there, in its three shapes. The first is the equal-total one:
// accepted A,B,C and stored A,B,X, which the count cannot see.
func TestIdentityUnexpected(t *testing.T) {
	t.Run("a loss offset by a row in no ledger", func(t *testing.T) {
		for _, inv := range Reconcile(3, 0, 3, 3) {
			if !inv.Passed {
				t.Fatalf("the count pre-check should pass equal totals: %s", inv.Detail)
			}
		}
		x := idBase + 9000
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB, idC}}})
		stored := map[string][]int64{"d1": {idA, idB, x}}
		rep, inv := ReconcileIdentity(led, readOf(stored), 3, sim.Snapshot{Emitted: 3}, []string{"d1"})
		if inv.Passed {
			t.Fatal("identity passed a loss offset by an unexpected row")
		}
		if rep.Missing != 1 || !reflect.DeepEqual(rep.Samples.Missing, []string{key("d1", idC)}) {
			t.Errorf("missing = %d %v; want 1 [%s]", rep.Missing, rep.Samples.Missing, key("d1", idC))
		}
		if rep.Unexpected != 1 || !reflect.DeepEqual(rep.Samples.Unexpected, []string{key("d1", x)}) {
			t.Errorf("unexpected = %d %v; want 1 [%s]", rep.Unexpected, rep.Samples.Unexpected, key("d1", x))
		}
		if rep.DuplicateKeys != 0 || rep.RefusedStored != 0 {
			t.Errorf("duplicates %d refused stored %d; want 0 and 0", rep.DuplicateKeys, rep.RefusedStored)
		}
	})

	t.Run("a refused event that was stored", func(t *testing.T) {
		r := idBase + 7000
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB}, Refused: []int64{r}}})
		stored := map[string][]int64{"d1": {idA, idB, r}}
		rep, inv := ReconcileIdentity(led, readOf(stored), 3, sim.Snapshot{Emitted: 2, Shed: 1}, []string{"d1"})
		if inv.Passed {
			t.Fatal("identity passed a stored refusal")
		}
		if rep.RefusedStored != 1 || rep.Unexpected != 1 || !reflect.DeepEqual(rep.Samples.Unexpected, []string{"refused:" + key("d1", r)}) {
			t.Errorf("refused stored %d, unexpected %d %v; want 1, 1, [refused:%s]", rep.RefusedStored, rep.Unexpected, rep.Samples.Unexpected, key("d1", r))
		}
		if rep.Missing != 0 || rep.DuplicateKeys != 0 {
			t.Errorf("missing %d duplicates %d; want 0 and 0", rep.Missing, rep.DuplicateKeys)
		}
	})

	t.Run("a refused event that is absent is the passing case", func(t *testing.T) {
		r := idBase + 7000
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB}, Refused: []int64{r}}})
		stored := map[string][]int64{"d1": {idA, idB}}
		rep, inv := ReconcileIdentity(led, readOf(stored), 2, sim.Snapshot{Emitted: 2, Shed: 1}, []string{"d1"})
		if !inv.Passed || rep.Refused != 1 {
			t.Fatalf("an absent refusal must pass (refused %d): %s", rep.Refused, inv.Detail)
		}
	})

	t.Run("a row under no device the reads saw", func(t *testing.T) {
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB}}})
		stored := map[string][]int64{"d1": {idA, idB}}
		rep, inv := ReconcileIdentity(led, readOf(stored), 3, sim.Snapshot{Emitted: 2}, []string{"d1"})
		if inv.Passed {
			t.Fatal("identity passed with a row in the window that no device read accounts for")
		}
		if rep.Unattributed != 1 || rep.Unexpected != 1 {
			t.Errorf("unattributed %d unexpected %d; want 1 and 1", rep.Unattributed, rep.Unexpected)
		}
		if len(rep.Samples.Unexpected) != 1 || !strings.Contains(rep.Samples.Unexpected[0], "not seen by the per-device reads") {
			t.Errorf("unexpected samples = %v; want the unattributed note", rep.Samples.Unexpected)
		}
	})

	t.Run("a tenant total below the rows read", func(t *testing.T) {
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB}}})
		stored := map[string][]int64{"d1": {idA, idB}}
		rep, inv := ReconcileIdentity(led, readOf(stored), 1, sim.Snapshot{Emitted: 2}, []string{"d1"})
		if inv.Passed || rep.Unattributed != -1 || !strings.Contains(inv.Detail, "inconclusive") {
			t.Fatalf("a total below the rows read must fail inconclusive (unattributed %d): %s", rep.Unattributed, inv.Detail)
		}
	})
}

// An event whose outcome was unknown may be stored once or not at all, and never twice.
func TestIdentityAmbiguous(t *testing.T) {
	m := idBase + 5000
	led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA}, Ambiguous: []int64{m}}})
	drive := sim.Snapshot{Emitted: 1, Failed: 1}

	rep, inv := ReconcileIdentity(led, readOf(map[string][]int64{"d1": {idA}}), 1, drive, []string{"d1"})
	if !inv.Passed || rep.AmbiguousAbsent != 1 || rep.AmbiguousStored != 0 || !reflect.DeepEqual(rep.Samples.AmbiguousAbsent, []string{key("d1", m)}) {
		t.Errorf("absent ambiguous: passed=%v absent=%d stored=%d samples %v; want pass, 1, 0, [%s]",
			inv.Passed, rep.AmbiguousAbsent, rep.AmbiguousStored, rep.Samples.AmbiguousAbsent, key("d1", m))
	}

	rep, inv = ReconcileIdentity(led, readOf(map[string][]int64{"d1": {idA, m}}), 2, drive, []string{"d1"})
	if !inv.Passed || rep.AmbiguousStored != 1 || rep.AmbiguousAbsent != 0 || !reflect.DeepEqual(rep.Samples.AmbiguousStored, []string{key("d1", m)}) {
		t.Errorf("stored-once ambiguous: passed=%v stored=%d absent=%d samples %v; want pass, 1, 0, [%s]",
			inv.Passed, rep.AmbiguousStored, rep.AmbiguousAbsent, rep.Samples.AmbiguousStored, key("d1", m))
	}

	// Stored twice: equal totals (2 + the accepted one = 3 rows, against 1 accepted and 1
	// ambiguous) are not what makes this fail; the second copy is.
	rep, inv = ReconcileIdentity(led, readOf(map[string][]int64{"d1": {idA, m, m}}), 3, drive, []string{"d1"})
	if inv.Passed || rep.DuplicateKeys != 1 || rep.ExtraCopies != 1 || !reflect.DeepEqual(rep.Samples.Duplicate, []string{key("d1", m) + " x2"}) {
		t.Errorf("twice-stored ambiguous: passed=%v duplicates=%d extra=%d samples %v; want fail, 1, 1, [%s x2]",
			inv.Passed, rep.DuplicateKeys, rep.ExtraCopies, rep.Samples.Duplicate, key("d1", m))
	}
}

// The counterweight: intact truth must pass, and say what it showed.
func TestIdentityCleanRunPasses(t *testing.T) {
	ids := map[string]sim.DeviceIdentities{}
	stored := map[string][]int64{}
	var driven []string
	for d := 0; d < 3; d++ {
		dev := fmt.Sprintf("d%d", d)
		driven = append(driven, dev)
		for i := 0; i < 5; i++ {
			us := idBase + int64(i*1000+d)
			di := ids[dev]
			di.Accepted = append(di.Accepted, us)
			ids[dev] = di
			// Stored newest first, the server's order: the comparison must not depend on it.
			stored[dev] = append([]int64{us}, stored[dev]...)
		}
	}
	rep, inv := ReconcileIdentity(ledgerOfIDs(ids), readOf(stored), 15, sim.Snapshot{Emitted: 15}, driven)
	if !inv.Passed || !rep.Reconciled {
		t.Fatalf("a clean run failed identity: %s", inv.Detail)
	}
	if !strings.Contains(inv.Detail, "exactly once") || !strings.Contains(inv.Detail, "15 accepted") {
		t.Errorf("pass detail %q does not say what it showed", inv.Detail)
	}
	if rep.Accepted != 15 || rep.Persisted != 15 || rep.DevicesRead != 3 || rep.Method == "" {
		t.Errorf("accepted %d persisted %d devices %d method %q", rep.Accepted, rep.Persisted, rep.DevicesRead, rep.Method)
	}
}

// Every harness defect makes the evidence unable to decide the run: it fails, and says
// it is inconclusive rather than reporting a platform finding.
func TestIdentityHarnessDefectsFailInconclusive(t *testing.T) {
	clean := func() (sim.IdentitySnapshot, storedIdentities, int64, sim.Snapshot, []string) {
		led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB, idC, idD}}})
		return led, readOf(map[string][]int64{"d1": {idA, idB, idC, idD}}), 4, sim.Snapshot{Emitted: 4}, []string{"d1"}
	}
	cases := []struct {
		name   string
		mutate func(*sim.IdentitySnapshot, *storedIdentities, *int64, *sim.Snapshot, *[]string)
		want   string
	}{
		{"an identity filed accepted twice", func(l *sim.IdentitySnapshot, s *storedIdentities, _ *int64, d *sim.Snapshot, _ *[]string) {
			l.Devices["d1"] = sim.DeviceIdentities{Accepted: []int64{idA, idA, idC, idD}}
		}, "filed more than once"},
		{"an identity filed accepted and refused", func(l *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, d *sim.Snapshot, _ *[]string) {
			l.Devices["d1"] = sim.DeviceIdentities{Accepted: []int64{idA, idB, idC, idD}, Refused: []int64{idA}}
			l.All.Refused = 1
			d.Shed = 1
		}, "filed more than once"},
		{"an unparsed stamp", func(l *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, _ *sim.Snapshot, _ *[]string) {
			l.Unparsed = 1
		}, "did not parse"},
		{"a sub-microsecond stamp", func(l *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, _ *sim.Snapshot, _ *[]string) {
			l.SubMicro = 1
		}, "below the microsecond"},
		{"ledger accepted differs from the driver's", func(_ *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, d *sim.Snapshot, _ *[]string) {
			d.Emitted = 5
		}, "disagree"},
		{"ledger refusals below the driver's sheds", func(_ *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, d *sim.Snapshot, _ *[]string) {
			d.Shed = 2
		}, "fewer than the driver's"},
		{"ledger refused and ambiguous beyond what the driver did not accept", func(l *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, _ *sim.Snapshot, _ *[]string) {
			l.All.Ambiguous = 1
		}, "more than the driver's"},
		{"an empty ledger", func(l *sim.IdentitySnapshot, s *storedIdentities, tot *int64, d *sim.Snapshot, _ *[]string) {
			*l = ledgerOfIDs(map[string]sim.DeviceIdentities{})
			s.Times = map[string][]int64{"d1": nil}
			*tot = 0
			d.Emitted = 0
		}, "nothing to reconcile"},
		{"a driven device that was not read", func(_ *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, _ *sim.Snapshot, dr *[]string) {
			*dr = append(*dr, "d2")
		}, `"d2" was not read`},
		{"a ledger device the run did not drive", func(l *sim.IdentitySnapshot, _ *storedIdentities, _ *int64, _ *sim.Snapshot, dr *[]string) {
			*dr = nil
		}, "did not drive"},
		{"a device whose set kept moving", func(_ *sim.IdentitySnapshot, s *storedIdentities, _ *int64, _ *sim.Snapshot, _ *[]string) {
			delete(s.Times, "d1")
			s.Unstable = []string{"d1"}
		}, "changed during the read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			led, st, tot, drive, driven := clean()
			c.mutate(&led, &st, &tot, &drive, &driven)
			rep, inv := ReconcileIdentity(led, st, tot, drive, driven)
			if inv.Passed || rep.Reconciled {
				t.Fatalf("passed despite %s: %s", c.name, inv.Detail)
			}
			if !strings.HasPrefix(inv.Detail, "inconclusive: ") || !strings.Contains(inv.Detail, c.want) {
				t.Errorf("detail %q; want it inconclusive and naming %q", inv.Detail, c.want)
			}
		})
	}
	// The counterweight: the unmodified fixture passes, so every case above failed for
	// its own reason.
	led, st, tot, drive, driven := clean()
	if _, inv := ReconcileIdentity(led, st, tot, drive, driven); !inv.Passed {
		t.Fatalf("the clean fixture failed: %s", inv.Detail)
	}
}

// Samples are bounded; counts are not.
func TestIdentitySamplesBounded(t *testing.T) {
	var accepted []int64
	for i := 0; i < 50; i++ {
		accepted = append(accepted, idBase+int64(i))
	}
	led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: accepted}})
	rep, _ := ReconcileIdentity(led, readOf(map[string][]int64{"d1": nil}), 0, sim.Snapshot{Emitted: 50}, []string{"d1"})
	if rep.Missing != 50 || len(rep.Samples.Missing) != identitySampleLimit {
		t.Errorf("missing %d with %d samples; want 50 with %d", rep.Missing, len(rep.Samples.Missing), identitySampleLimit)
	}
	if rep.Samples.Missing[0] != key("d1", idBase) {
		t.Errorf("first sample %q; want the earliest, %q", rep.Samples.Missing[0], key("d1", idBase))
	}
}

// identityServer stands in for event-management: it serves the login handshake and
// answers each events query from page(pageNumber, deviceToken).
func identityServer(t *testing.T, page func(n int, device string) string) (*graphqlIdentityReader, *[]string) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies []string
	)
	// A reader that stops recognising the end of a device's rows would page forever and
	// surface only as go test's package timeout. Cap the requests one test may send, so
	// that regression fails by name instead.
	const maxEventRequests = 20
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	pageRe := regexp.MustCompile(`"pageNumber":(\d+)`)
	devRe := regexp.MustCompile(`"deviceToken":"([^"]*)"`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, "login("):
			_, _ = w.Write([]byte(`{"data":{"login":{"identityToken":"id","expiresAt":"` + future + `","superuser":true,"memberships":[{"tenant":"t","roles":["r"]}]}}}`))
		case strings.Contains(body, "selectTenant("):
			_, _ = w.Write([]byte(`{"data":{"selectTenant":{"accessToken":"acc","refreshToken":"ref","expiresAt":"` + future + `"}}}`))
		case strings.Contains(body, "events("):
			mu.Lock()
			bodies = append(bodies, body)
			sent := len(bodies)
			mu.Unlock()
			if sent > maxEventRequests {
				t.Errorf("the reader sent more than %d events requests: it does not stop paging", maxEventRequests)
				w.WriteHeader(http.StatusTeapot)
				return
			}
			pm, dm := pageRe.FindStringSubmatch(body), devRe.FindStringSubmatch(body)
			if pm == nil || dm == nil {
				t.Errorf("events query without a page number or device token: %s", body)
				return
			}
			n, _ := strconv.Atoi(pm[1])
			_, _ = w.Write([]byte(page(n, dm[1])))
		default:
			t.Errorf("unexpected request body: %s", body)
		}
	}))
	t.Cleanup(srv.Close)
	session := userclient.NewTenantSession(srv.Client(), srv.URL, "e@x", "pw", "t")
	return &graphqlIdentityReader{session: session, endpoint: srv.URL}, &bodies
}

// eventsPage renders one events response: rows for the given device and times, newest
// first as the server orders them, and a totalRecords (raw JSON, so null is expressible).
func eventsPage(device string, times []int64, totalRecords string) string {
	var rows []string
	for i := len(times) - 1; i >= 0; i-- {
		at := time.UnixMicro(times[i]).UTC().Format(time.RFC3339Nano)
		rows = append(rows, `{"deviceToken":"`+device+`","occurredTime":"`+at+`"}`)
	}
	return `{"data":{"events":{"results":[` + strings.Join(rows, ",") + `],"pagination":{"totalRecords":` + totalRecords + `}}}}`
}

// The reader pages a device's events through the real tenant API until it holds the
// total, sends the device and Measurement filters on every page, and returns every
// identity — and it fails closed on every way a response can be wrong.
func TestGraphqlIdentityReader(t *testing.T) {
	ctx := context.Background()
	w := Window{Start: time.Unix(idBase/1e6-60, 0), End: time.Unix(idBase/1e6+3600, 0)}
	all := make([]int64, 2500)
	for i := range all {
		all[i] = idBase + int64(i)*1000
	}
	// pageOf returns the n-th 1000-row slice of the device's rows (in server order).
	pageOf := func(rows []int64, n int) []int64 {
		lo, hi := (n-1)*identityPageSize, n*identityPageSize
		if lo > len(rows) {
			return nil
		}
		if hi > len(rows) {
			hi = len(rows)
		}
		return rows[lo:hi]
	}

	t.Run("pages to the total and returns every identity", func(t *testing.T) {
		r, bodies := identityServer(t, func(n int, dev string) string {
			return eventsPage(dev, pageOf(all, n), "2500")
		})
		got, pages, err := r.DeviceIdentities(ctx, "d1", w)
		if err != nil {
			t.Fatal(err)
		}
		if pages != 3 || len(*bodies) != 3 {
			t.Fatalf("read %d pages (%d requests); want 3", pages, len(*bodies))
		}
		seen := map[int64]int{}
		for _, us := range got {
			seen[us]++
		}
		if len(got) != 2500 || len(seen) != 2500 {
			t.Fatalf("returned %d identities (%d distinct); want all 2500", len(got), len(seen))
		}
		for _, us := range all {
			if seen[us] != 1 {
				t.Fatalf("identity %d returned %d times; want once", us, seen[us])
			}
		}
		for i, b := range *bodies {
			for _, want := range []string{`"deviceToken":"d1"`, `"eventTypes":[2]`, fmt.Sprintf(`"pageNumber":%d`, i+1), `"pageSize":1000`} {
				if !strings.Contains(b, want) {
					t.Errorf("page %d request does not carry %s: %s", i+1, want, b)
				}
			}
		}
	})

	moved := []struct {
		name string
		page func(n int, dev string) string
	}{
		{"the total changes between pages", func(n int, dev string) string {
			tot := "2500"
			if n > 1 {
				tot = "2501"
			}
			return eventsPage(dev, pageOf(all, n), tot)
		}},
		{"a page comes back empty before the total", func(n int, dev string) string {
			if n == 2 {
				return eventsPage(dev, nil, "2500")
			}
			return eventsPage(dev, pageOf(all, n), "2500")
		}},
		{"a single page holds fewer rows than its total", func(n int, dev string) string {
			return eventsPage(dev, all[:10], "11")
		}},
		{"a single page holds more rows than its total", func(n int, dev string) string {
			return eventsPage(dev, all[:12], "11")
		}},
	}
	for _, c := range moved {
		t.Run(c.name, func(t *testing.T) {
			r, _ := identityServer(t, c.page)
			if _, _, err := r.DeviceIdentities(ctx, "d1", w); !errors.Is(err, errSetMoved) {
				t.Fatalf("err = %v; want a set that moved", err)
			}
		})
	}

	broken := []struct {
		name string
		page func(n int, dev string) string
	}{
		{"a row for another device", func(int, string) string { return eventsPage("d9", all[:3], "3") }},
		{"a row with no occurredTime", func(int, string) string {
			return `{"data":{"events":{"results":[{"deviceToken":"d1","occurredTime":null}],"pagination":{"totalRecords":1}}}}`
		}},
		{"an unparseable occurredTime", func(int, string) string {
			return `{"data":{"events":{"results":[{"deviceToken":"d1","occurredTime":"yesterday"}],"pagination":{"totalRecords":1}}}}`
		}},
		{"a null totalRecords", func(n int, dev string) string { return eventsPage(dev, all[:3], "null") }},
	}
	for _, c := range broken {
		t.Run(c.name, func(t *testing.T) {
			r, _ := identityServer(t, c.page)
			_, _, err := r.DeviceIdentities(ctx, "d1", w)
			if err == nil || errors.Is(err, errSetMoved) || errors.Is(err, errReadTransport) {
				t.Fatalf("err = %v; want a hard error, not a pass, not a retryable move and not a retryable transport failure", err)
			}
		})
	}

	// A request that fails (here a 502 from a proxy) is a transport failure, which the
	// caller retries, not a malformed response and not a set that moved.
	t.Run("a request that fails is a transport failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), "events(") {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(string(raw), "login(") {
				_, _ = w.Write([]byte(`{"data":{"login":{"identityToken":"id","expiresAt":"` + future + `","superuser":true,"memberships":[{"tenant":"t","roles":["r"]}]}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"selectTenant":{"accessToken":"acc","refreshToken":"ref","expiresAt":"` + future + `"}}}`))
		}))
		t.Cleanup(srv.Close)
		r := &graphqlIdentityReader{session: userclient.NewTenantSession(srv.Client(), srv.URL, "e@x", "pw", "t"), endpoint: srv.URL}
		_, _, err := r.DeviceIdentities(ctx, "d1", w)
		if !errors.Is(err, errReadTransport) || errors.Is(err, errSetMoved) {
			t.Fatalf("err = %v; want a transport failure", err)
		}
	})
}

// scriptedReader answers per device: a number of errSetMoved before a stable read, or a
// hard error.
type scriptedReader struct {
	mu    sync.Mutex
	rows  map[string][]int64
	moves map[string]int // errSetMoved answers before a stable one; -1 = always
	// transport: errReadTransport answers before a stable one; -1 = always
	transport map[string]int
	hard      map[string]error
	calls     map[string]int
	before    func() // runs on every call (used to observe ordering)
}

func (s *scriptedReader) DeviceIdentities(_ context.Context, dev string, _ Window) ([]int64, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.before != nil {
		s.before()
	}
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[dev]++
	if err := s.hard[dev]; err != nil {
		return nil, 1, err
	}
	if m := s.transport[dev]; m < 0 || s.calls[dev] <= m {
		return nil, 0, fmt.Errorf("%s: %w: connection reset", dev, errReadTransport)
	}
	if m := s.moves[dev]; m < 0 || s.calls[dev] <= m {
		return nil, 1, fmt.Errorf("%s: %w", dev, errSetMoved)
	}
	return append([]int64(nil), s.rows[dev]...), 2, nil
}

// A set that moves once is re-read and counts; one that keeps moving is reported as
// unstable after the bounded attempts, not as an error that would lose the report; any
// other error stops the read.
func TestReadStoredIdentities(t *testing.T) {
	ctx := context.Background()
	r := &scriptedReader{
		rows:  map[string][]int64{"d1": {idA}, "d2": {idB}, "d3": {idC}},
		moves: map[string]int{"d1": 1, "d2": -1},
	}
	got, err := readStoredIdentities(ctx, r, []string{"d1", "d2", "d3"}, Window{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Times, map[string][]int64{"d1": {idA}, "d3": {idC}}) {
		t.Errorf("times = %v; want d1 and d3 read", got.Times)
	}
	if !reflect.DeepEqual(got.Unstable, []string{"d2"}) || r.calls["d2"] != identityReadAttempts {
		t.Errorf("unstable = %v after %d attempts; want [d2] after %d", got.Unstable, r.calls["d2"], identityReadAttempts)
	}
	if r.calls["d1"] != 2 {
		t.Errorf("d1 was read %d times; want 2 (one move, then stable)", r.calls["d1"])
	}
	// Pages are counted across every attempt: d1 1+2, d2 3×1, d3 2.
	if got.Pages != 3+3+2 {
		t.Errorf("pages = %d; want 8", got.Pages)
	}

	boom := errors.New("boom")
	r = &scriptedReader{rows: map[string][]int64{"d1": {idA}}, hard: map[string]error{"d2": boom}}
	if _, err := readStoredIdentities(ctx, r, []string{"d1", "d2"}, Window{}); !errors.Is(err, boom) {
		t.Fatalf("err = %v; want the hard error", err)
	}
	if r.calls["d2"] != 1 {
		t.Errorf("d2's hard error was read %d times; want once (a malformed response is not retried)", r.calls["d2"])
	}
}

// One transient request failure in a read-back of millions of rows must not cost the run
// its evidence: it is retried after a pause. One that outlasts the attempts is an error.
func TestReadStoredIdentitiesRetriesTransportFailures(t *testing.T) {
	saved := identityRetryDelay
	identityRetryDelay = time.Millisecond
	t.Cleanup(func() { identityRetryDelay = saved })
	ctx := context.Background()

	r := &scriptedReader{
		rows:      map[string][]int64{"d1": {idA}, "d2": {idB}},
		transport: map[string]int{"d1": identityReadAttempts - 1},
	}
	got, err := readStoredIdentities(ctx, r, []string{"d1", "d2"}, Window{})
	if err != nil {
		t.Fatalf("a transport failure that clears within the attempts must not fail the read: %v", err)
	}
	if !reflect.DeepEqual(got.Times, map[string][]int64{"d1": {idA}, "d2": {idB}}) || len(got.Unstable) != 0 {
		t.Errorf("times = %v unstable = %v; want both devices read", got.Times, got.Unstable)
	}
	if r.calls["d1"] != identityReadAttempts {
		t.Errorf("d1 was read %d times; want %d", r.calls["d1"], identityReadAttempts)
	}

	r = &scriptedReader{
		rows:      map[string][]int64{"d1": {idA}},
		transport: map[string]int{"d1": -1},
	}
	got, err = readStoredIdentities(ctx, r, []string{"d1"}, Window{})
	if !errors.Is(err, errReadTransport) {
		t.Fatalf("err = %v; want the transport failure once the attempts ran out", err)
	}
	if r.calls["d1"] != identityReadAttempts || len(got.Unstable) != 0 {
		t.Errorf("d1 read %d times, unstable %v; want %d attempts and not reported as unstable", r.calls["d1"], got.Unstable, identityReadAttempts)
	}
}
