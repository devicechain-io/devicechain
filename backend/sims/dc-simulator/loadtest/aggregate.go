// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// This is the AGGREGATE identity oracle: the same question the per-device read-back answers
// ("was every accepted event stored exactly once, and nothing else?") at a cost that does not
// grow with the number of events.
//
// The per-device read-back (identity.go) pages every stored row back through the tenant
// GraphQL API: 90M events at 100k/s for 15 minutes is hours of reads that themselves load the
// shard under test. Here the store computes, per device and IN SQL on the shard, the pair
//
//	(count, sum over rows of MicrosHash(occurred_time in µs) mod 2^64)
//
// and the ledger computes the same pair from what the simulator sent. One grouped query per
// tenant replaces millions of paged reads.
//
// WHAT THE PAIR CATCHES, by construction:
//   - loss: the count and the hash sum both fall;
//   - duplication: both rise (the same row is added twice);
//   - substitution (A,A,C,D stored for A,B,C,D): the count agrees and the hash sum does not,
//     because MicrosHash(A)+MicrosHash(A) != MicrosHash(A)+MicrosHash(B) unless the 64-bit
//     hashes collide (2^-64 per pair of distinct instants).
//
// The count alone is the blind spot the swap control in selftest.go demonstrates: a lost row
// offset by a duplicate leaves every total unchanged.
//
// THE HASH is the first 8 bytes of SHA-256 over the 8-byte big-endian two's-complement
// encoding of the microsecond instant, read as a uint64. A cryptographic hash, not a cheap
// mixer, because the instants are regular (a device at 1 Hz steps by exactly 1,000,000 µs) and
// a weak mixer sums regular inputs to colliding totals. Postgres computes the same value with
// sha256(int8send(µs)) (built in since PG 11), so Go and SQL agree bit for bit; the SQL lives
// in pgperturb/aggregate.go and the two are pinned together by a test against a real server.
//
// WHEN A DEVICE DISAGREES, the oracle does not stop at "mismatch". A device has two admissible
// stored states: exactly its accepted events, or its accepted events plus ALL of its ambiguous
// ones (the sends whose outcome was unknown). Anything else is drilled into: the device's
// rows are read exactly (the SAME per-identity rules as the full read-back, classifyDevice),
// which names the missing and duplicated identities. Ambiguity is the only reason a
// mismatching device can still be sound (a SUBSET of its ambiguous events was stored), and the
// ledger file carries the full identities of exactly those devices (and of any device with a
// refused send), so the drill-down is possible from the file alone.
//
// BOUNDED OBSERVATION, the same as the full read-back's: the oracle sees what was stored when
// it read. Settle > 0 reads twice that far apart and refuses to decide if the answer moved.

// InvAggregate is the aggregate oracle's invariant key.
const InvAggregate = "ingest-aggregate-identity"

// AggregateSchema versions the ledger file.
const AggregateSchema = 1

// aggregateMethod is the report's statement of what was compared and how it was read.
const aggregateMethod = "per device, the event store computes (count, sum of sha256-derived 64-bit hashes of " +
	"occurred_time in microseconds, mod 2^64) in SQL over the tenant's Measurement rows in the run's window, " +
	"and the harness's ledger computes the same pair from what it sent; a device is sound when the pair equals " +
	"its accepted events, or its accepted plus all ambiguous events; any other device is read exactly and " +
	"judged by identity, with multiplicity. One grouped query per tenant, no per-row transfer. A 64-bit hash " +
	"sum: it misses a loss only if a different row set happens to hash to the same 64-bit total (about 2^-64)."

const (
	// aggregateMismatchSamples bounds the per-device mismatches a report lists.
	aggregateMismatchSamples = 50
	// aggregateDrillMax bounds how many mismatching devices are read exactly; beyond it the
	// verdict stands (it is already a failure) but the report names only the first of them.
	aggregateDrillMax = 200
)

// MicrosHash is the order-independent row hash: the first 8 bytes of SHA-256 over the 8-byte
// big-endian two's-complement encoding of the Unix-microsecond instant.
func MicrosHash(us int64) uint64 {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(us))
	h := sha256.Sum256(b[:])
	return binary.BigEndian.Uint64(h[:8])
}

// DeviceAggregate is the (count, hash sum) pair of one device's rows. The sum wraps modulo
// 2^64, which is exactly what summing in SQL numeric and reducing modulo 2^64 gives.
type DeviceAggregate struct {
	Count int64
	Hash  uint64
}

// Add folds one instant in.
func (a *DeviceAggregate) Add(us int64) {
	a.Count++
	a.Hash += MicrosHash(us)
}

// Plus is the aggregate of the union (multiset sum) of the two row sets.
func (a DeviceAggregate) Plus(b DeviceAggregate) DeviceAggregate {
	return DeviceAggregate{Count: a.Count + b.Count, Hash: a.Hash + b.Hash}
}

// AggregateOf is the aggregate of a set of instants.
func AggregateOf(times []int64) DeviceAggregate {
	var a DeviceAggregate
	for _, us := range times {
		a.Add(us)
	}
	return a
}

type deviceAggregateJSON struct {
	Count int64  `json:"count"`
	Hash  string `json:"hash"`
}

// MarshalJSON writes the hash as 16 hex digits: a uint64 does not survive a JSON number.
func (a DeviceAggregate) MarshalJSON() ([]byte, error) {
	return json.Marshal(deviceAggregateJSON{Count: a.Count, Hash: fmt.Sprintf("%016x", a.Hash)})
}

// UnmarshalJSON is MarshalJSON's inverse.
func (a *DeviceAggregate) UnmarshalJSON(b []byte) error {
	var j deviceAggregateJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	h, err := strconv.ParseUint(j.Hash, 16, 64)
	if err != nil {
		return fmt.Errorf("aggregate hash %q: %w", j.Hash, err)
	}
	a.Count, a.Hash = j.Count, h
	return nil
}

// IdentityDetail is one device's full ledger, kept in the file only where the aggregate cannot
// decide on its own (ambiguous or refused sends) or when asked for.
type IdentityDetail struct {
	Accepted  []int64 `json:"accepted,omitempty"`
	Refused   []int64 `json:"refused,omitempty"`
	Ambiguous []int64 `json:"ambiguous,omitempty"`
}

// AggregateDevice is what one device sent, summarised.
type AggregateDevice struct {
	Accepted  DeviceAggregate `json:"accepted"`
	Refused   DeviceAggregate `json:"refused"`
	Ambiguous DeviceAggregate `json:"ambiguous"`
	Detail    *IdentityDetail `json:"detail,omitempty"`
}

// DriveCounters are the driver's own counters the ledger is cross-checked against.
type DriveCounters struct {
	Emitted        int64 `json:"emitted"`
	Failed         int64 `json:"failed"`
	Shed           int64 `json:"shed"`
	Backpressured  int64 `json:"backpressured"`
	ConnectRefused int64 `json:"connectRefused"`
}

// AggregateLedger is a harness's expected aggregates: the compact artifact a run leaves
// behind (--aggregate-ledger) for the oracle to check against the store, wherever the store is
// reachable. Its size follows the number of devices, not the number of events.
type AggregateLedger struct {
	Schema      int       `json:"schema"`
	Tenant      string    `json:"tenant"`
	Manifest    string    `json:"manifest,omitempty"`
	Seed        int64     `json:"seed,omitempty"`
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`

	Drive DriveCounters `json:"drive"`
	// All counts emits of EVERY type by outcome, as the ledger saw them.
	AllAccepted  int64 `json:"allAccepted"`
	AllRefused   int64 `json:"allRefused"`
	AllAmbiguous int64 `json:"allAmbiguous"`
	Unparsed     int64 `json:"unparsed,omitempty"`
	SubMicro     int64 `json:"subMicrosecond,omitempty"`
	// Collisions counts identities the ledger filed more than once ((device, instant) in two sends).
	// Summing hashes cannot tell one stored copy of each from two stored copies of one, so a ledger
	// with a collision cannot be decided by the aggregate and is inconclusive, exactly as the
	// per-identity read-back calls it.
	Collisions int64 `json:"collisions,omitempty"`

	Devices map[string]AggregateDevice `json:"devices"`
}

// Window is the run's occurred-time window.
func (l *AggregateLedger) Window() Window { return Window{Start: l.WindowStart, End: l.WindowEnd} }

// NewAggregateLedger summarises a finished run's identity ledger. detailAll keeps every
// device's identities (large: for tests and small runs); otherwise only the devices the
// aggregate alone cannot judge (those with an ambiguous or refused send) keep theirs.
func NewAggregateLedger(tenant, manifest string, seed int64, w Window, led sim.IdentitySnapshot, drive sim.Snapshot, detailAll bool) *AggregateLedger {
	l := &AggregateLedger{
		Schema: AggregateSchema, Tenant: tenant, Manifest: manifest, Seed: seed,
		WindowStart: w.Start.UTC(), WindowEnd: w.End.UTC(),
		Drive: DriveCounters{Emitted: drive.Emitted, Failed: drive.Failed, Shed: drive.Shed,
			Backpressured: drive.Backpressured, ConnectRefused: drive.ConnectRefused},
		AllAccepted: led.All.Accepted, AllRefused: led.All.Refused, AllAmbiguous: led.All.Ambiguous,
		Unparsed: led.Unparsed, SubMicro: led.SubMicro,
		Devices: make(map[string]AggregateDevice, len(led.Devices)),
	}
	for dev, ids := range led.Devices {
		l.Collisions += identityCollisions(ids)
		d := AggregateDevice{
			Accepted:  AggregateOf(ids.Accepted),
			Refused:   AggregateOf(ids.Refused),
			Ambiguous: AggregateOf(ids.Ambiguous),
		}
		if detailAll || len(ids.Ambiguous) > 0 || len(ids.Refused) > 0 {
			d.Detail = &IdentityDetail{Accepted: ids.Accepted, Refused: ids.Refused, Ambiguous: ids.Ambiguous}
		}
		l.Devices[dev] = d
	}
	return l
}

// identityCollisions counts the instants one device filed more than once across all its sends.
func identityCollisions(ids sim.DeviceIdentities) int64 {
	seen := make(map[int64]struct{}, len(ids.Accepted)+len(ids.Refused)+len(ids.Ambiguous))
	var n int64
	for _, list := range [][]int64{ids.Accepted, ids.Refused, ids.Ambiguous} {
		for _, us := range list {
			if _, dup := seen[us]; dup {
				n++
				continue
			}
			seen[us] = struct{}{}
		}
	}
	return n
}

// snapshots rebuilds what ledgerInconsistencies reads.
func (l *AggregateLedger) snapshots() (sim.IdentitySnapshot, sim.Snapshot) {
	return sim.IdentitySnapshot{
		All:      sim.OutcomeTotals{Accepted: l.AllAccepted, Refused: l.AllRefused, Ambiguous: l.AllAmbiguous},
		Unparsed: l.Unparsed, SubMicro: l.SubMicro,
	}, sim.Snapshot{Emitted: l.Drive.Emitted, Failed: l.Drive.Failed, Shed: l.Drive.Shed,
		Backpressured: l.Drive.Backpressured, ConnectRefused: l.Drive.ConnectRefused}
}

// WriteFile writes the ledger as JSON.
func (l *AggregateLedger) WriteFile(path string) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// ReadAggregateLedger reads a ledger file, refusing a schema it does not know.
func ReadAggregateLedger(path string) (*AggregateLedger, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l AggregateLedger
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l.Schema != AggregateSchema {
		return nil, fmt.Errorf("%s: aggregate ledger schema %d, this build reads %d", path, l.Schema, AggregateSchema)
	}
	if l.Tenant == "" || l.Devices == nil {
		return nil, fmt.Errorf("%s: not an aggregate ledger (no tenant or devices)", path)
	}
	return &l, nil
}

// ErrReadTransport marks an exact read whose request failed (so it is retried), for readers
// that live outside this package.
var ErrReadTransport = errReadTransport

// AggregateReader returns the store's (count, hash) per device for the tenant's Measurement
// rows in a window. A device with no rows is simply absent from the map.
type AggregateReader interface {
	DeviceAggregates(ctx context.Context, w Window) (map[string]DeviceAggregate, error)
}

// DeviceMismatch is one device whose store and ledger disagree.
type DeviceMismatch struct {
	Device string `json:"device"`
	// Kind is missing, extra, substituted, unledgered, partial-ambiguous or unresolved.
	Kind           string `json:"kind"`
	AcceptedCount  int64  `json:"acceptedCount"`
	AmbiguousCount int64  `json:"ambiguousCount"`
	StoredCount    int64  `json:"storedCount"`
}

// AggregateReport is the aggregate oracle's evidence.
type AggregateReport struct {
	Method string `json:"method"`
	Tenant string `json:"tenant"`

	LedgerDevices int   `json:"ledgerDevices"`
	StoredDevices int   `json:"storedDevices"`
	Accepted      int64 `json:"accepted"`
	Ambiguous     int64 `json:"ambiguous"`
	Refused       int64 `json:"refused"`
	StoredRows    int64 `json:"storedRows"`

	DevicesSound      int `json:"devicesSound"`
	DevicesMismatched int `json:"devicesMismatched"`
	// DevicesDrilled is how many mismatching devices were read exactly.
	DevicesDrilled int `json:"devicesDrilled"`

	// Missing counts accepted identities stored 0 times; ExtraCopies the copies of an identity
	// beyond the one allowed; Unexpected rows that should not be there at all. For a device
	// that was drilled the numbers are exact. For one that could not be they are LOWER BOUNDS
	// derived from the counts (the report says which).
	Missing       int64 `json:"missing"`
	DuplicateKeys int64 `json:"duplicateKeys"`
	ExtraCopies   int64 `json:"extraCopies"`
	Unexpected    int64 `json:"unexpected"`
	Substituted   int   `json:"substitutedDevices"`
	Unresolved    int   `json:"unresolvedDevices"`

	Mismatches []DeviceMismatch `json:"mismatches,omitempty"`
	Samples    IdentitySamples  `json:"samples"`

	ReadSeconds float64 `json:"readSeconds"`
	// Inconclusive lists why the evidence could not decide the run, if it could not.
	Inconclusive []string `json:"inconclusive,omitempty"`
	Reconciled   bool     `json:"reconciled"`
}

// AggregateOptions tune CheckAggregate.
type AggregateOptions struct {
	// Exact reads one device's stored instants exactly (the drill-down). Nil leaves a
	// mismatching device judged by its counts alone, with a lower bound.
	Exact identityReader
	// Settle, when positive, reads the aggregates twice that far apart and refuses to decide
	// if they differ: rows still arriving would otherwise read as loss.
	Settle time.Duration
}

// CheckAggregate runs the aggregate oracle: read the store's per-device aggregates, compare
// them with the ledger's, drill into the devices that disagree, and decide.
func CheckAggregate(ctx context.Context, reader AggregateReader, exp *AggregateLedger, opt AggregateOptions) (AggregateReport, Invariant, error) {
	started := time.Now()
	w := exp.Window()
	stored, err := reader.DeviceAggregates(ctx, w)
	if err != nil {
		return AggregateReport{}, Invariant{}, fmt.Errorf("aggregate read: %w", err)
	}
	moved := false
	if opt.Settle > 0 {
		select {
		case <-ctx.Done():
			return AggregateReport{}, Invariant{}, ctx.Err()
		case <-time.After(opt.Settle):
		}
		again, err := reader.DeviceAggregates(ctx, w)
		if err != nil {
			return AggregateReport{}, Invariant{}, fmt.Errorf("aggregate re-read: %w", err)
		}
		moved = !sameAggregates(stored, again)
		stored = again
	}
	rep := reconcileAggregate(ctx, exp, stored, opt)
	rep.ReadSeconds = time.Since(started).Seconds()
	if moved {
		rep.Inconclusive = append(rep.Inconclusive, fmt.Sprintf("the stored aggregates changed during the %s settle: rows were still arriving", opt.Settle))
		rep.Reconciled = false
	}
	return rep, Invariant{Name: InvAggregate, Passed: rep.Reconciled, Detail: aggregateDetail(rep)}, nil
}

func sameAggregates(a, b map[string]DeviceAggregate) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// reconcileAggregate is the pure verdict (apart from the drill-down reads).
func reconcileAggregate(ctx context.Context, exp *AggregateLedger, stored map[string]DeviceAggregate, opt AggregateOptions) AggregateReport {
	rep := AggregateReport{Method: aggregateMethod, Tenant: exp.Tenant, LedgerDevices: len(exp.Devices), StoredDevices: len(stored)}
	inconclusive := func(format string, args ...any) {
		rep.Inconclusive = append(rep.Inconclusive, fmt.Sprintf(format, args...))
	}
	ledSnap, driveSnap := exp.snapshots()
	for _, why := range ledgerInconsistencies(ledSnap, driveSnap) {
		inconclusive("%s", why)
	}
	if exp.Collisions > 0 {
		inconclusive("%d identities were filed more than once in the ledger (a harness defect): a sum of hashes cannot tell one stored copy of each from two of one", exp.Collisions)
	}
	for _, d := range exp.Devices {
		rep.Accepted += d.Accepted.Count
		rep.Ambiguous += d.Ambiguous.Count
		rep.Refused += d.Refused.Count
	}
	if rep.Accepted+rep.Ambiguous+rep.Refused == 0 {
		inconclusive("the ledger holds no Measurement emit, so there is nothing to reconcile")
	}
	for _, s := range stored {
		rep.StoredRows += s.Count
	}

	// Sorted, so a report is reproducible and the drill-down cap is deterministic.
	names := make([]string, 0, len(exp.Devices)+len(stored))
	for d := range exp.Devices {
		names = append(names, d)
	}
	for d := range stored {
		if _, ok := exp.Devices[d]; !ok {
			names = append(names, d)
		}
	}
	sort.Strings(names)

	var drill []string
	record := func(m DeviceMismatch) {
		rep.DevicesMismatched++
		if len(rep.Mismatches) < aggregateMismatchSamples {
			rep.Mismatches = append(rep.Mismatches, m)
		}
	}
	for _, dev := range names {
		e, inLedger := exp.Devices[dev]
		s := stored[dev]
		if !inLedger {
			// Rows for a device the run never sent for.
			rep.Unexpected += s.Count
			sample(&rep.Samples.Unexpected, fmt.Sprintf("%s (%d row(s), a device the ledger does not name)", dev, s.Count))
			record(DeviceMismatch{Device: dev, Kind: "unledgered", StoredCount: s.Count})
			continue
		}
		if s == e.Accepted || s == e.Accepted.Plus(e.Ambiguous) {
			rep.DevicesSound++
			continue
		}
		if opt.Exact != nil && len(drill) < aggregateDrillMax && (e.Detail != nil) {
			drill = append(drill, dev)
			continue
		}
		rep.arithmetic(dev, e, s, record)
	}

	// The drill-down: read the disagreeing devices exactly and judge them by identity.
	if len(drill) > 0 {
		out, err := readStoredIdentities(ctx, opt.Exact, drill, exp.Window())
		if err != nil {
			inconclusive("the exact drill-down read failed: %v", err)
		}
		unstable := map[string]bool{}
		for _, d := range out.Unstable {
			unstable[d] = true
		}
		for _, dev := range drill {
			e, s := exp.Devices[dev], stored[dev]
			times, ok := out.Times[dev]
			if !ok {
				if unstable[dev] {
					inconclusive("device %q kept changing under the exact read", dev)
				}
				rep.arithmetic(dev, e, s, record)
				continue
			}
			var ex IdentityReport
			classifyDevice(&ex, dev, sim.DeviceIdentities{Accepted: e.Detail.Accepted, Refused: e.Detail.Refused, Ambiguous: e.Detail.Ambiguous}, times)
			rep.DevicesDrilled++
			if ex.Collisions > 0 {
				inconclusive("device %q filed an identity more than once in the ledger (a harness defect)", dev)
			}
			if ex.Missing == 0 && ex.DuplicateKeys == 0 && ex.Unexpected == 0 {
				// A subset of the ambiguous events was stored: allowed.
				rep.DevicesSound++
				continue
			}
			rep.Missing += ex.Missing
			rep.DuplicateKeys += ex.DuplicateKeys
			rep.ExtraCopies += ex.ExtraCopies
			rep.Unexpected += ex.Unexpected
			for _, x := range ex.Samples.Missing {
				sample(&rep.Samples.Missing, x)
			}
			for _, x := range ex.Samples.Duplicate {
				sample(&rep.Samples.Duplicate, x)
			}
			for _, x := range ex.Samples.Unexpected {
				sample(&rep.Samples.Unexpected, x)
			}
			kind := "extra"
			switch {
			case ex.Missing > 0 && ex.DuplicateKeys == 0 && ex.Unexpected == 0:
				kind = "missing"
			case ex.Missing > 0:
				kind = "substituted"
			}
			record(DeviceMismatch{Device: dev, Kind: kind, AcceptedCount: e.Accepted.Count, AmbiguousCount: e.Ambiguous.Count, StoredCount: s.Count})
		}
	}
	if rep.DevicesMismatched > 0 && opt.Exact == nil {
		inconclusive("%d mismatching device(s) were judged from their counts alone; the missing/extra figures are lower bounds, run with the exact read for the identities", rep.DevicesMismatched)
	}
	rep.Reconciled = len(rep.Inconclusive) == 0 && rep.Missing == 0 && rep.DuplicateKeys == 0 &&
		rep.ExtraCopies == 0 && rep.Unexpected == 0 && rep.Substituted == 0 && rep.Unresolved == 0 && rep.DevicesMismatched == 0
	return rep
}

// arithmetic judges a mismatching device from its counts alone: its stored count against the
// admissible range [accepted, accepted+ambiguous]. The figures it adds are lower bounds.
func (r *AggregateReport) arithmetic(dev string, e AggregateDevice, s DeviceAggregate, record func(DeviceMismatch)) {
	lo, hi := e.Accepted.Count, e.Accepted.Count+e.Ambiguous.Count
	m := DeviceMismatch{Device: dev, AcceptedCount: lo, AmbiguousCount: e.Ambiguous.Count, StoredCount: s.Count}
	switch {
	case s.Count < lo:
		m.Kind = "missing"
		r.Missing += lo - s.Count
		sample(&r.Samples.Missing, fmt.Sprintf("%s (at least %d, stored %d of %d accepted)", dev, lo-s.Count, s.Count, lo))
	case s.Count > hi:
		m.Kind = "extra"
		r.ExtraCopies += s.Count - hi
		sample(&r.Samples.Duplicate, fmt.Sprintf("%s (at least %d extra, stored %d of at most %d)", dev, s.Count-hi, s.Count, hi))
	case e.Ambiguous.Count == 0:
		// Same count as accepted and a different hash: a different set of instants.
		m.Kind = "substituted"
		r.Substituted++
		sample(&r.Samples.Unexpected, fmt.Sprintf("%s (count %d as expected, instants differ: a row was replaced)", dev, s.Count))
	default:
		m.Kind = "unresolved"
		r.Unresolved++
		sample(&r.Samples.Unexpected, fmt.Sprintf("%s (count within the ambiguous range, hash matches no allowed state; needs the exact read)", dev))
	}
	record(m)
}

// aggregateDetail renders the verdict line.
func aggregateDetail(r AggregateReport) string {
	if r.Reconciled {
		return fmt.Sprintf("every accepted event stored exactly once, by (count, hash) per device: %d accepted, %d ambiguous, %d stored in %d devices, 0 missing, 0 duplicated, 0 unexpected",
			r.Accepted, r.Ambiguous, r.StoredRows, r.StoredDevices)
	}
	s := ""
	if len(r.Inconclusive) > 0 {
		s = fmt.Sprintf("inconclusive: %v; ", r.Inconclusive)
	}
	return s + fmt.Sprintf("AGGREGATE: %d of %d device(s) disagree (%d read exactly): %d missing %v, %d duplicated (%d extra copies) %v, %d unexpected %v, %d substituted; %d accepted, %d stored",
		r.DevicesMismatched, r.LedgerDevices, r.DevicesDrilled, r.Missing, r.Samples.Missing, r.DuplicateKeys, r.ExtraCopies,
		r.Samples.Duplicate, r.Unexpected, r.Samples.Unexpected, r.Substituted, r.Accepted, r.StoredRows)
}
