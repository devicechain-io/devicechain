// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/userclient"
	"github.com/devicechain-io/dc-simulator/sim"
)

// This is the identity reconciliation: the check behind "every accepted event was stored
// exactly once".
//
// ingest-completeness compares two TOTALS, and equal totals cannot tell "nothing was
// lost" from "one event was lost and another was stored twice" or "one was lost and an
// event that should not be there took its place". Accepting A,B,C,D and storing A,A,C,D
// passes it. So after the count pre-check the harness reads back every stored event of
// every driven device and compares them, ONE BY ONE and with multiplicity, against the
// identity ledger the simulator filled while it drove (sim.IdentityLedger).
//
// The identity is (device token, occurredTime in microseconds). The harness provisions
// the device tokens, unique within the tenant, and stamps occurredTime itself at the
// precision the store keeps, and a device emits at most one Measurement per tick with
// ticks never overlapping, so the pair is unique per emit. The ledger detects a pair filed
// twice anyway and the run then fails as inconclusive.
//
// 🔴 It is NOT an altId. A stored (altId, occurredTime) match is skipped by the event
// store BEFORE insert, so an altId on every emit would hide exactly the duplicates this
// check looks for, and it would also move every write onto the alt-id probe path and
// change the work being measured. Devices without an altId, the benchmark's and most real
// fleets', get no such protection, which is what this check has to see.
//
// What a pass proves: every event the ingress accepted is in the store exactly once under
// the device and instant it was sent with; every event the ingress refused is absent; an
// event whose outcome was unknown is there at most once; and nothing else is in the run's
// window. An event stored with a REWRITTEN occurred_time shows as one missing plus one
// unexpected, which fails closed: the stored event is not the one that was sent.
//
// BOUNDED OBSERVATION, the same as the count's: the check sees what was stored by the
// moment the final tenant total was read (observedUntilAfterDriveSeconds in the report).
// A redelivered copy can land up to the broker's ackWait (60s) after a lost ack, so a run
// whose evidence is to be published must settle past that (--quiesce-settle 60s or more).

// InvIdentity is the identity reconciliation's invariant key.
const InvIdentity = "ingest-identity"

const (
	// identitySampleLimit bounds the example identities each category reports.
	identitySampleLimit = 10
	// identityPageSize is the server's page ceiling (core/rdb's maximum page size).
	identityPageSize = 1000
	// identityReadWorkers bounds how many devices are read at once.
	identityReadWorkers = 8
	// identityReadAttempts is how many times one device is read before a stored set that
	// keeps moving under the read is reported as unstable.
	identityReadAttempts = 3
)

// identityMethod is the report's statement of what was compared and how it was read,
// including what the read costs, so a published figure can cite it.
const identityMethod = "per driven device: the tenant events query filtered to the device token, the " +
	"Measurement type and the run's occurred-time window, paged at 1000 rows and re-read until its " +
	"total holds; every row keyed (device token, occurredTime in microseconds) and compared with the " +
	"simulator's accepted/refused/ambiguous ledger with multiplicity; then the window's tenant total " +
	"re-counted to catch rows no device read saw. Cost: one COUNT and one page per 1000 rows per " +
	"device over the (device_token, occurred_time) index, which carries no tenant_id, so in " +
	"uncompressed chunks each COUNT also visits other tenants' rows under the same token."

// IdentityKey is one event's identity in the reconciliation.
type IdentityKey struct {
	Device         string
	OccurredMicros int64
}

// String renders the key as the report's samples do: "device@RFC3339Nano".
func (k IdentityKey) String() string {
	return k.Device + "@" + time.UnixMicro(k.OccurredMicros).UTC().Format(time.RFC3339Nano)
}

// IdentityReport is the identity reconciliation's evidence, carried in the run report.
type IdentityReport struct {
	// Method states what was compared and how it was read.
	Method string `json:"method"`

	// The Measurement ledger, by outcome.
	Accepted  int64 `json:"accepted"`
	Refused   int64 `json:"refused"`
	Ambiguous int64 `json:"ambiguous"`

	// Persisted is the number of rows the per-device reads returned; TenantTotal the
	// window's tenant count, read AFTER them; Unattributed the difference: rows in the
	// window no device read saw (an undriven device, or a row that arrived during the read).
	Persisted    int64 `json:"persisted"`
	TenantTotal  int64 `json:"tenantTotal"`
	Unattributed int64 `json:"unattributed"`

	// Missing counts accepted identities stored 0 times.
	Missing int64 `json:"missing"`
	// DuplicateKeys counts identities stored more often than allowed (an accepted or
	// ambiguous one more than once), and ExtraCopies the copies beyond the allowed one.
	DuplicateKeys int64 `json:"duplicateKeys"`
	ExtraCopies   int64 `json:"extraCopies"`
	// Unexpected counts stored rows that should not be there: identities in no ledger,
	// refused identities that were stored, and Unattributed rows.
	Unexpected int64 `json:"unexpected"`
	// RefusedStored counts refused identities found stored (also counted in Unexpected).
	RefusedStored int64 `json:"refusedStored"`
	// AmbiguousStored and AmbiguousAbsent split the ambiguous identities; both are allowed.
	AmbiguousStored int64 `json:"ambiguousStored"`
	AmbiguousAbsent int64 `json:"ambiguousAbsent"`

	// Harness defects, each of which makes the verdict inconclusive.
	Collisions      int64 `json:"harnessCollisions"`
	Unparsed        int64 `json:"unparsed"`
	SubMicro        int64 `json:"subMicrosecond"`
	UnstableDevices int   `json:"unstableDevices"`

	// The read.
	DevicesRead int     `json:"devicesRead"`
	PagesRead   int     `json:"pagesRead"`
	ReadSeconds float64 `json:"readSeconds"`

	// The observation horizon: the count's settle, when the identity read started, and
	// when the final tenant total was read, each relative to the end of the drive. A row
	// stored after observedUntil is outside what this report saw.
	SettleSeconds               float64 `json:"settleSeconds"`
	ReadStartedAfterDriveSecs   float64 `json:"readStartedAfterDriveSeconds"`
	ObservedUntilAfterDriveSecs float64 `json:"observedUntilAfterDriveSeconds"`

	Samples IdentitySamples `json:"samples"`
	// Inconclusive lists why the evidence could not decide the run, if it could not.
	Inconclusive []string `json:"inconclusive,omitempty"`
	// Reconciled is true only when every count that must be 0 is 0 and nothing made the
	// evidence inconclusive.
	Reconciled bool `json:"reconciled"`
}

// IdentitySamples holds up to identitySampleLimit example identities per category.
type IdentitySamples struct {
	Missing         []string `json:"missing,omitempty"`
	Duplicate       []string `json:"duplicate,omitempty"` // "device@time xN"
	Unexpected      []string `json:"unexpected,omitempty"`
	AmbiguousStored []string `json:"ambiguousStored,omitempty"`
	AmbiguousAbsent []string `json:"ambiguousAbsent,omitempty"`
	Unstable        []string `json:"unstable,omitempty"`
}

// sample appends s to *dst while dst holds fewer than the limit.
func sample(dst *[]string, s string) {
	if len(*dst) < identitySampleLimit {
		*dst = append(*dst, s)
	}
}

// storedIdentities is what the per-device reads returned.
type storedIdentities struct {
	// Times maps every driven device that was read stably to its stored occurred_times
	// (Unix microseconds), WITH repeats.
	Times map[string][]int64
	// Unstable lists the driven devices whose stored set kept moving under the read.
	Unstable []string
	Pages    int
	Elapsed  time.Duration
}

// keyCounts is one identity's multiplicity in each ledger and in the store.
type keyCounts struct{ a, r, m, s int }

// ReconcileIdentity is the pure identity verdict.
//
// led is the simulator's ledger; stored the per-device reads; tenantTotal the window's
// tenant count read after them; drive the driver's own counters; driven the device tokens
// the run drove, every one of which must have been read.
//
// Rules, per identity, with a, r, m its count in the accepted, refused and ambiguous
// ledgers and s its stored count:
//   - a + r + m > 1: the harness filed one identity twice; inconclusive.
//   - a == 1: s == 0 is missing; s > 1 is a duplicate with s-1 extra copies.
//   - m == 1: s == 0 or 1 is allowed (ambiguousAbsent / ambiguousStored); s > 1 is a duplicate.
//   - r == 1: s >= 1 is a stored refusal, counted unexpected too: a false refusal.
//   - in no ledger: s >= 1 is unexpected, every copy.
//
// The ledger is also checked against the driver's counters, which count every event type:
// accepted must equal what the driver counted accepted, refusals must cover its sheds and
// backpressure, and refused plus ambiguous cannot exceed everything it did not accept. The
// keyed identities are Measurements only, so the identity check is defined for the
// Measurement-only scenarios L1 and contention drive; a scenario that also emits another
// type still reconciles its counters, but its other events are not keyed.
func ReconcileIdentity(led sim.IdentitySnapshot, stored storedIdentities, tenantTotal int64, drive sim.Snapshot, driven []string) (IdentityReport, Invariant) {
	meas := led.Measurements()
	rep := IdentityReport{
		Method:          identityMethod,
		Accepted:        meas.Accepted,
		Refused:         meas.Refused,
		Ambiguous:       meas.Ambiguous,
		TenantTotal:     tenantTotal,
		Unparsed:        led.Unparsed,
		SubMicro:        led.SubMicro,
		UnstableDevices: len(stored.Unstable),
		DevicesRead:     len(stored.Times),
		PagesRead:       stored.Pages,
		ReadSeconds:     stored.Elapsed.Seconds(),
	}
	inconclusive := func(format string, args ...any) {
		rep.Inconclusive = append(rep.Inconclusive, fmt.Sprintf(format, args...))
	}

	// The ledger against the driver's own counters.
	if led.All.Accepted != drive.Emitted {
		inconclusive("the ledger and the driver's counters disagree: ledger accepted %d, driver accepted %d", led.All.Accepted, drive.Emitted)
	}
	if led.All.Refused < drive.Shed+drive.Backpressured {
		inconclusive("the ledger filed %d refusals, fewer than the driver's %d shed and %d backpressured", led.All.Refused, drive.Shed, drive.Backpressured)
	}
	if led.All.Refused+led.All.Ambiguous > drive.Shed+drive.Backpressured+drive.Failed {
		inconclusive("the ledger filed %d refused and %d ambiguous, more than the driver's %d shed, %d backpressured and %d failed",
			led.All.Refused, led.All.Ambiguous, drive.Shed, drive.Backpressured, drive.Failed)
	}
	if led.Unparsed > 0 {
		inconclusive("%d sent occurredTime value(s) did not parse back", led.Unparsed)
	}
	if led.SubMicro > 0 {
		inconclusive("%d sent occurredTime value(s) carry a part below the microsecond, which the store does not keep", led.SubMicro)
	}
	if meas.Accepted+meas.Refused+meas.Ambiguous == 0 {
		inconclusive("the ledger holds no Measurement emit, so there is nothing to reconcile")
	}

	// Every driven device must have been read (stably or not), and every device the ledger
	// names must have been driven: a device skipped by the read would otherwise report its
	// accepted events as missing and hide the real cause.
	drivenSet := make(map[string]bool, len(driven))
	for _, d := range driven {
		drivenSet[d] = true
	}
	unstable := make(map[string]bool, len(stored.Unstable))
	for _, d := range stored.Unstable {
		unstable[d] = true
		sample(&rep.Samples.Unstable, d)
	}
	devices := append([]string(nil), driven...)
	sort.Strings(devices)
	for _, d := range devices {
		if _, ok := stored.Times[d]; !ok && !unstable[d] {
			inconclusive("driven device %q was not read", d)
		}
	}
	for d := range led.Devices {
		if !drivenSet[d] {
			inconclusive("the ledger names device %q, which the run did not drive", d)
		}
	}
	if len(stored.Unstable) > 0 {
		inconclusive("%d device(s) changed during the read in %d attempts each (still ingesting)", len(stored.Unstable), identityReadAttempts)
	}

	for _, dev := range devices {
		if unstable[dev] {
			continue
		}
		times, ok := stored.Times[dev]
		if !ok {
			continue
		}
		rep.Persisted += int64(len(times))
		counts := map[int64]*keyCounts{}
		at := func(us int64) *keyCounts {
			c := counts[us]
			if c == nil {
				c = &keyCounts{}
				counts[us] = c
			}
			return c
		}
		ids := led.Devices[dev]
		for _, us := range ids.Accepted {
			at(us).a++
		}
		for _, us := range ids.Refused {
			at(us).r++
		}
		for _, us := range ids.Ambiguous {
			at(us).m++
		}
		for _, us := range times {
			at(us).s++
		}
		keys := make([]int64, 0, len(counts))
		for us := range counts {
			keys = append(keys, us)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, us := range keys {
			c := counts[us]
			key := IdentityKey{Device: dev, OccurredMicros: us}.String()
			switch {
			case c.a+c.r+c.m > 1:
				rep.Collisions++
			case c.a == 1:
				if c.s == 0 {
					rep.Missing++
					sample(&rep.Samples.Missing, key)
				} else if c.s > 1 {
					rep.DuplicateKeys++
					rep.ExtraCopies += int64(c.s - 1)
					sample(&rep.Samples.Duplicate, fmt.Sprintf("%s x%d", key, c.s))
				}
			case c.m == 1:
				switch c.s {
				case 0:
					rep.AmbiguousAbsent++
					sample(&rep.Samples.AmbiguousAbsent, key)
				case 1:
					rep.AmbiguousStored++
					sample(&rep.Samples.AmbiguousStored, key)
				default:
					rep.DuplicateKeys++
					rep.ExtraCopies += int64(c.s - 1)
					sample(&rep.Samples.Duplicate, fmt.Sprintf("%s x%d", key, c.s))
				}
			case c.r == 1:
				if c.s > 0 {
					rep.RefusedStored++
					rep.Unexpected += int64(c.s)
					sample(&rep.Samples.Unexpected, "refused:"+key)
				}
			default:
				rep.Unexpected += int64(c.s)
				sample(&rep.Samples.Unexpected, key)
			}
		}
	}
	if rep.Collisions > 0 {
		inconclusive("%d identities were filed more than once in the ledger (a harness defect)", rep.Collisions)
	}

	// Rows in the window that no device read saw. Only meaningful when every device was
	// read stably: an unstable device's rows are not in Persisted.
	if len(stored.Unstable) == 0 {
		rep.Unattributed = tenantTotal - rep.Persisted
		switch {
		case rep.Unattributed > 0:
			rep.Unexpected += rep.Unattributed
			sample(&rep.Samples.Unexpected, fmt.Sprintf("(%d row(s) in the window not seen by the per-device reads: an undriven device, or a row that arrived during the read)", rep.Unattributed))
		case rep.Unattributed < 0:
			inconclusive("the tenant total %d is below the %d rows the device reads returned: the stored set moved during the read", tenantTotal, rep.Persisted)
		}
	}

	rep.Reconciled = len(rep.Inconclusive) == 0 && rep.Missing == 0 && rep.DuplicateKeys == 0 && rep.Unexpected == 0
	return rep, Invariant{Name: InvIdentity, Passed: rep.Reconciled, Detail: identityDetail(rep)}
}

// identityDetail renders the verdict line.
func identityDetail(r IdentityReport) string {
	if r.Reconciled {
		return fmt.Sprintf("every accepted event stored exactly once by (device, occurredTime): %d accepted, %d missing, %d duplicated, %d unexpected, %d refused stored, ambiguous %d (stored %d, absent %d)",
			r.Accepted, r.Missing, r.DuplicateKeys, r.Unexpected, r.RefusedStored, r.Ambiguous, r.AmbiguousStored, r.AmbiguousAbsent)
	}
	var b strings.Builder
	if len(r.Inconclusive) > 0 {
		fmt.Fprintf(&b, "inconclusive: %s; ", strings.Join(r.Inconclusive, "; "))
	}
	fmt.Fprintf(&b, "IDENTITY: %d missing %v, %d duplicated %v, %d unexpected %v (%d refused stored); %d accepted, %d stored",
		r.Missing, r.Samples.Missing, r.DuplicateKeys, r.Samples.Duplicate, r.Unexpected, r.Samples.Unexpected, r.RefusedStored, r.Accepted, r.Persisted)
	return b.String()
}

// identityReader reads one device's stored Measurement occurred_times in a window.
type identityReader interface {
	DeviceIdentities(ctx context.Context, device string, w Window) (times []int64, pages int, err error)
}

// errSetMoved marks a read whose stored set changed while it was being paged: rows were
// still arriving. It is retried, and reported as unstable if it persists, rather than
// failing the run with no report.
var errSetMoved = errors.New("the stored set moved during the read")

const identityEventsQuery = `query LoadTestIdentities($c: EventSearchCriteria!) {
  events(criteria: $c) { results { deviceToken occurredTime } pagination { totalRecords } }
}`

// graphqlIdentityReader reads stored identities over the real tenant-scoped
// event-management GraphQL API, the same surface the count reads.
type graphqlIdentityReader struct {
	session  *userclient.TenantSession
	endpoint string
}

// DeviceIdentities pages one device's stored Measurement events in w. The server's order
// is total and stable (occurred_time DESC, event_id DESC), so OFFSET paging neither
// repeats nor skips a row while the set holds still; if it moves, the total changes and
// the read reports errSetMoved. A malformed response is a plain error: it is not a set
// that moved, and retrying it would only hide a broken read.
func (g *graphqlIdentityReader) DeviceIdentities(ctx context.Context, device string, w Window) ([]int64, int, error) {
	var (
		times []int64
		want  int64
		pages int
	)
	for page := 1; ; page++ {
		vars := map[string]any{
			"c": map[string]any{
				"pageNumber":  page,
				"pageSize":    identityPageSize,
				"deviceToken": device,
				"eventTypes":  []int{MeasurementEventType},
				"startTime":   w.Start.UTC().Format(time.RFC3339),
				"endTime":     w.End.UTC().Format(time.RFC3339),
			},
		}
		var out struct {
			Events struct {
				Results []struct {
					DeviceToken  string  `json:"deviceToken"`
					OccurredTime *string `json:"occurredTime"`
				} `json:"results"`
				Pagination struct {
					TotalRecords *int64 `json:"totalRecords"`
				} `json:"pagination"`
			} `json:"events"`
		}
		if err := g.session.Query(ctx, g.endpoint, identityEventsQuery, vars, &out); err != nil {
			return nil, pages, fmt.Errorf("read identities of %q: %w", device, err)
		}
		pages++
		total := out.Events.Pagination.TotalRecords
		if total == nil {
			return nil, pages, fmt.Errorf("read identities of %q: server returned a null totalRecords", device)
		}
		if page == 1 {
			want = *total
		} else if *total != want {
			return nil, pages, fmt.Errorf("%q: total %d on page 1, %d on page %d: %w", device, want, *total, page, errSetMoved)
		}
		for _, r := range out.Events.Results {
			if r.DeviceToken != device {
				return nil, pages, fmt.Errorf("read identities of %q: the server returned a row for device %q", device, r.DeviceToken)
			}
			if r.OccurredTime == nil {
				return nil, pages, fmt.Errorf("read identities of %q: the server returned a row with no occurredTime", device)
			}
			at, err := time.Parse(time.RFC3339Nano, *r.OccurredTime)
			if err != nil {
				return nil, pages, fmt.Errorf("read identities of %q: unparseable occurredTime %q: %w", device, *r.OccurredTime, err)
			}
			times = append(times, at.UnixMicro())
		}
		n := int64(len(times))
		switch {
		case n == want:
			return times, pages, nil
		case n > want:
			return nil, pages, fmt.Errorf("%q: %d rows read against a total of %d: %w", device, n, want, errSetMoved)
		case len(out.Events.Results) < identityPageSize:
			// A short (or empty) page before the total was reached: rows left the set.
			return nil, pages, fmt.Errorf("%q: %d rows read against a total of %d and the pages ran out: %w", device, n, want, errSetMoved)
		}
	}
}

// readStoredIdentities reads every device's stored identities with a bounded pool. A
// device whose set keeps moving is retried and then listed as unstable; any other error
// stops the read and is returned.
func readStoredIdentities(ctx context.Context, r identityReader, devices []string, w Window) (storedIdentities, error) {
	started := time.Now()
	out := storedIdentities{Times: make(map[string][]int64, len(devices))}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	work := make(chan string)
	for i := 0; i < identityReadWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dev := range work {
				var (
					times []int64
					pages int
					err   error
				)
				for attempt := 0; attempt < identityReadAttempts; attempt++ {
					var p int
					times, p, err = r.DeviceIdentities(ctx, dev, w)
					pages += p
					if !errors.Is(err, errSetMoved) {
						break
					}
				}
				mu.Lock()
				out.Pages += pages
				switch {
				case err == nil:
					out.Times[dev] = times
				case errors.Is(err, errSetMoved):
					out.Unstable = append(out.Unstable, dev)
				case firstErr == nil:
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, dev := range devices {
		select {
		case work <- dev:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	out.Elapsed = time.Since(started)
	sort.Strings(out.Unstable)
	if firstErr != nil {
		return out, firstErr
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// checkIdentity runs the identity reconciliation after the count has quiesced: read every
// driven device, THEN re-count the window, so a row that arrives during the read shows as
// unattributed rather than hiding, then decide. driveEnd anchors the report's observation
// horizon. A transport or protocol failure is an error; a stored set that will not hold
// still is an inconclusive verdict, so the run still produces its report.
func checkIdentity(ctx context.Context, counter eventCounter, reader identityReader, led *sim.IdentityLedger,
	devices []string, drive sim.Snapshot, w Window, driveEnd time.Time, settle time.Duration) (IdentityReport, Invariant, error) {
	readStarted := time.Since(driveEnd)
	stored, err := readStoredIdentities(ctx, reader, devices, w)
	if err != nil {
		return IdentityReport{}, Invariant{}, fmt.Errorf("identity read-back: %w", err)
	}
	total, err := counter.Count(ctx, w)
	if err != nil {
		return IdentityReport{}, Invariant{}, fmt.Errorf("identity read-back: tenant total: %w", err)
	}
	observedUntil := time.Since(driveEnd)
	rep, inv := ReconcileIdentity(led.Snapshot(), stored, total, drive, devices)
	rep.SettleSeconds = settle.Seconds()
	rep.ReadStartedAfterDriveSecs = readStarted.Seconds()
	rep.ObservedUntilAfterDriveSecs = observedUntil.Seconds()
	return rep, inv, nil
}

// deviceTokens lists the tokens of the devices a run drove.
func deviceTokens(ds []sim.DeviceInstance) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Token
	}
	return out
}
