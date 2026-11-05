// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/loadtest/pgperturb"
	"github.com/devicechain-io/dc-simulator/sim"
)

// The rig proves the aggregate oracle against a real Postgres at scale, the way the self-test
// proves the per-device one at small scale: seed a known store, show the oracle GREEN on it,
// then perturb it ONE ROW AT A TIME beneath the API and show it RED, naming the row, in the
// cases a count cannot see. A gate that has not been shown to fail is a check that cannot fail.
//
// The stages, in order (the red ones run FIRST, before the store is ever declared clean):
//
//	delete-one   one row deleted                            oracle must be RED, naming the row
//	swap         ...and one row on another device
//	             duplicated, so every total is equal again  oracle RED naming both; the grand-total
//	                                                        count comparison is GREEN (blind)
//	restore-1    both put back                              oracle must be GREEN
//	substitute   one row's instant moved by 1 µs: every
//	             device count unchanged                     oracle RED; a per-device COUNT comparison
//	                                                        is GREEN (blind)
//	restore-2    put back                                   oracle must be GREEN
//
// The blind comparators are the mutants of the oracle: if one of them were the oracle, the
// stage it is blind in would pass. The rig requires them green exactly where the oracle is red,
// which is what shows the perturbation is one a weaker check misses and the control bites.

// The rig's table is the event store's, for the columns the oracle and the perturber touch.
const rigTable = `"event-management"."events"`

const rigBaseUs = int64(1_800_000_000_000_000) // a whole second, far from any real clock

func rigUs(d, k int) int64  { return rigBaseUs + int64(k)*1_000_000 + int64(d) }
func rigToken(d int) string { return fmt.Sprintf("rig-%06d", d) }
func rigTokenIdx(s string) int {
	var d int
	_, _ = fmt.Sscanf(strings.TrimPrefix(s, "rig-"), "%d", &d)
	return d
}

type stageResult struct {
	Name        string   `json:"name"`
	ExpectRed   bool     `json:"expectRed"`
	Red         bool     `json:"red"`
	AsExpected  bool     `json:"asExpected"`
	Seconds     float64  `json:"seconds"`
	Detail      string   `json:"detail"`
	Missing     int64    `json:"missing"`
	Duplicated  int64    `json:"duplicateKeys"`
	Unexpected  int64    `json:"unexpected"`
	Substituted int      `json:"substitutedDevices"`
	Named       []string `json:"named,omitempty"`
	// Mutant is the verdict of the weaker comparator the stage is meant to defeat ("" if none):
	// "green" means it was blind to the perturbation.
	Mutant        string `json:"mutant,omitempty"`
	MutantBlind   bool   `json:"mutantBlind,omitempty"`
	MutantExpects string `json:"mutantExpects,omitempty"`
}

type rigReport struct {
	Rows            int64         `json:"rows"`
	Devices         int           `json:"devices"`
	SeedSeconds     float64       `json:"seedSeconds"`
	LedgerSeconds   float64       `json:"ledgerSeconds"`
	AggregateSecond float64       `json:"aggregateQuerySeconds"`
	ParityOK        bool          `json:"sqlGoHashParity"`
	Stages          []stageResult `json:"stages"`
	Sound           bool          `json:"oracleSound"`
}

func rig(args []string) int {
	fs := flag.NewFlagSet("rig", flag.ExitOnError)
	dsn := fs.String("dsn", "", "scratch Postgres DSN (prefer DC_EVENTSTORE_DSN)")
	scratch := fs.Bool("scratch", false, "required: confirm the database may be DROPPED and refilled (the rig recreates the event-management events table)")
	events := fs.Int("events", 10_000_000, "rows to seed")
	devices := fs.Int("devices", 10_000, "devices to spread them over (must divide --events)")
	tenant := fs.String("tenant", "aggrig", "tenant the rows belong to")
	report := fs.String("report", "", "write the JSON evidence to this path")
	ledgerOut := fs.String("write-ledger", "", "also write the seeded run's aggregate ledger (aggregates only) to this path, so `verify` can be pointed at the same database")
	_ = fs.Parse(args)
	conn := *dsn
	if conn == "" {
		conn = envOr("DC_EVENTSTORE_DSN", "")
	}
	if !*scratch || conn == "" {
		fmt.Fprintln(os.Stderr, "rig: --scratch and a DSN (--dsn or DC_EVENTSTORE_DSN) are required; the rig DROPS the events table")
		return exitError
	}
	if *devices <= 0 || *events <= 0 || *events%*devices != 0 || *devices >= 1_000_000 {
		fmt.Fprintln(os.Stderr, "rig: --devices must divide --events and stay below 1,000,000")
		return exitError
	}
	ctx := context.Background()
	rep, err := runRig(ctx, conn, *tenant, *events, *devices, *ledgerOut)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rig:", err)
		return exitError
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if *report != "" {
		if err := os.WriteFile(*report, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "rig: write report:", err)
			return exitError
		}
	}
	if !rep.Sound {
		fmt.Fprintln(os.Stderr, "rig: the oracle is NOT sound (a control did not behave as required)")
		return exitFail
	}
	fmt.Fprintln(os.Stderr, "rig: the oracle is sound: green on truth, red on every perturbation, and the weaker comparators were blind where it saw")
	return exitOK
}

func runRig(ctx context.Context, dsn, tenant string, events, devices int, ledgerOut string) (*rigReport, error) {
	k := events / devices
	rep := &rigReport{Devices: devices}
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer db.Close(ctx)

	// --- SQL and Go must hash alike, on instants either side of zero.
	rep.ParityOK = true
	for _, us := range []int64{0, 1, -1, rigBaseUs, rigBaseUs + 999_999, -rigBaseUs} {
		var got int64
		if err := db.QueryRow(ctx, `SELECT ('x' || encode(substr(sha256(int8send($1::bigint)), 1, 8), 'hex'))::bit(64)::bigint`, us).Scan(&got); err != nil {
			return nil, fmt.Errorf("hash parity: %w", err)
		}
		if uint64(got) != loadtest.MicrosHash(us) {
			rep.ParityOK = false
			fmt.Fprintf(os.Stderr, "rig: hash parity broken at %d: sql %x go %x\n", us, uint64(got), loadtest.MicrosHash(us))
		}
	}
	if !rep.ParityOK {
		return rep, nil
	}

	// --- seed
	t0 := time.Now()
	if err := seed(ctx, db, tenant, events, devices, k); err != nil {
		return nil, err
	}
	rep.SeedSeconds = time.Since(t0).Seconds()
	if err := db.QueryRow(ctx, `SELECT count(*) FROM `+rigTable+` WHERE tenant_id = $1`, tenant).Scan(&rep.Rows); err != nil {
		return nil, err
	}
	if rep.Rows != int64(events) {
		return nil, fmt.Errorf("seeded %d rows, wanted %d", rep.Rows, events)
	}

	// --- the ledger the harness would have written: every device's accepted instants.
	t0 = time.Now()
	snap := sim.IdentitySnapshot{Devices: make(map[string]sim.DeviceIdentities, devices)}
	for d := 0; d < devices; d++ {
		acc := make([]int64, k)
		for j := range acc {
			acc[j] = rigUs(d, j)
		}
		snap.Devices[rigToken(d)] = sim.DeviceIdentities{Accepted: acc}
	}
	snap.All = sim.OutcomeTotals{Accepted: int64(events)}
	w := loadtest.Window{
		Start: time.UnixMicro(rigBaseUs - 5_000_000).UTC(),
		End:   time.UnixMicro(rigUs(devices, k) + 5_000_000).UTC(),
	}
	led := loadtest.NewAggregateLedger(tenant, "rig", 0, w, snap, sim.Snapshot{Emitted: int64(events)}, true)
	rep.LedgerSeconds = time.Since(t0).Seconds()
	if ledgerOut != "" {
		slim := loadtest.NewAggregateLedger(tenant, "rig", 0, w, snap, sim.Snapshot{Emitted: int64(events)}, false)
		if err := slim.WriteFile(ledgerOut); err != nil {
			return nil, fmt.Errorf("write the ledger: %w", err)
		}
	}

	oracle, err := pgperturb.NewOracle(ctx, dsn, tenant)
	if err != nil {
		return nil, err
	}
	defer oracle.Close(ctx)
	pert, err := pgperturb.New(ctx, dsn, tenant)
	if err != nil {
		return nil, err
	}
	defer pert.Close(ctx)

	run := func(name string, expectRed bool, mutant string, blindFn func(map[string]loadtest.DeviceAggregate) bool, named ...string) error {
		t0 := time.Now()
		r, inv, err := loadtest.CheckAggregate(ctx, oracle, led, loadtest.AggregateOptions{Exact: oracle})
		if err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		secs := time.Since(t0).Seconds()
		st := stageResult{Name: name, ExpectRed: expectRed, Red: !inv.Passed, Seconds: secs, Detail: inv.Detail,
			Missing: r.Missing, Duplicated: r.DuplicateKeys, Unexpected: r.Unexpected, Substituted: r.Substituted}
		st.AsExpected = st.Red == expectRed
		// Exact naming: every identity the stage must name has to be in the report's samples.
		found := append(append(append([]string{}, r.Samples.Missing...), r.Samples.Duplicate...), r.Samples.Unexpected...)
		for _, want := range named {
			ok := false
			for _, f := range found {
				if strings.HasPrefix(f, want) {
					ok = true
				}
			}
			st.Named = append(st.Named, fmt.Sprintf("%s named=%v", want, ok))
			st.AsExpected = st.AsExpected && ok
		}
		if blindFn != nil {
			stored, err := oracle.DeviceAggregates(ctx, led.Window())
			if err != nil {
				return err
			}
			blind := blindFn(stored)
			st.Mutant, st.MutantBlind, st.MutantExpects = mutant, blind, "green (blind)"
			if blind {
				st.Mutant += ": GREEN"
			} else {
				st.Mutant += ": red"
			}
			st.AsExpected = st.AsExpected && blind
		}
		// The cost of one whole-tenant check on an intact store (the fastest of the green stages, so a
		// busy machine does not inflate the figure a published claim would cite).
		if (name == "restore-1" || name == "restore-2") && (rep.AggregateSecond == 0 || secs < rep.AggregateSecond) {
			rep.AggregateSecond = secs
		}
		rep.Stages = append(rep.Stages, st)
		fmt.Fprintf(os.Stderr, "rig: stage %-10s red=%-5v expected-red=%-5v as-expected=%v %.1fs %s\n", name, st.Red, expectRed, st.AsExpected, secs, st.Mutant)
		return nil
	}

	// Grand-total count comparison: the blind spot of every total.
	totalOnly := func(stored map[string]loadtest.DeviceAggregate) bool {
		var n int64
		for _, a := range stored {
			n += a.Count
		}
		return n == int64(events)
	}
	// Per-device count comparison: blind to a substitution.
	countPerDevice := func(stored map[string]loadtest.DeviceAggregate) bool {
		for dev, e := range led.Devices {
			if stored[dev].Count != e.Accepted.Count {
				return false
			}
		}
		return len(stored) == len(led.Devices)
	}

	// 1. RED FIRST: one deleted row.
	delKey, n, err := pert.DeleteOneMeasurement(ctx, w)
	if err != nil || n != 1 {
		return nil, fmt.Errorf("delete-one: removed %d rows: %v", n, err)
	}
	if err := run("delete-one", true, "", nil, delKey.String()); err != nil {
		return nil, err
	}
	// 2. ...and one duplicated, on another device, so every total is equal again.
	dupKey, n, err := pert.DuplicateOneMeasurement(ctx, w)
	if err != nil || n != 1 {
		return nil, fmt.Errorf("swap: duplicated %d rows: %v", n, err)
	}
	if delKey.Device == dupKey.Device {
		return nil, fmt.Errorf("swap: the delete and the duplicate landed on one device (%s)", delKey.Device)
	}
	if err := run("swap", true, "grand-total count", totalOnly, delKey.String(), dupKey.String()); err != nil {
		return nil, err
	}
	// 3. put both back; now truth is intact and the oracle must say so.
	dd, dk := rigTokenIdx(delKey.Device), int((delKey.OccurredMicros-rigBaseUs-int64(rigTokenIdx(delKey.Device)))/1_000_000)
	if _, err := db.Exec(ctx, `INSERT INTO `+rigTable+` (tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
		VALUES ($1, sha256(($2::int || ':' || $3::int)::bytea), $4, $5, $6, '', now())`,
		tenant, dd, dk, delKey.Device, loadtest.MeasurementEventType, time.UnixMicro(delKey.OccurredMicros).UTC()); err != nil {
		return nil, fmt.Errorf("restore the deleted row: %w", err)
	}
	ed, ek := rigTokenIdx(dupKey.Device), int((dupKey.OccurredMicros-rigBaseUs-int64(rigTokenIdx(dupKey.Device)))/1_000_000)
	if tag, err := db.Exec(ctx, `DELETE FROM `+rigTable+` WHERE tenant_id = $1 AND occurred_time = $2 AND event_id = sha256(sha256(($3::int || ':' || $4::int)::bytea) || '\x01'::bytea)`,
		tenant, time.UnixMicro(dupKey.OccurredMicros).UTC(), ed, ek); err != nil || tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("remove the duplicate: %v (%v rows)", err, tag.RowsAffected())
	}
	if err := run("restore-1", false, "", nil); err != nil {
		return nil, err
	}
	// 4. a substitution: one row's instant moved by a microsecond; no device's count changes.
	subD, subK := devices/2, k/2
	subAt := time.UnixMicro(rigUs(subD, subK)).UTC()
	if tag, err := db.Exec(ctx, `UPDATE `+rigTable+` SET occurred_time = occurred_time + interval '1 microsecond' WHERE tenant_id = $1 AND device_token = $2 AND occurred_time = $3`,
		tenant, rigToken(subD), subAt); err != nil || tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("substitute: %v (%v rows)", err, tag.RowsAffected())
	}
	// It must name the instant that vanished and the one that appeared, not only the device.
	subKey := loadtest.IdentityKey{Device: rigToken(subD), OccurredMicros: rigUs(subD, subK)}
	subNow := loadtest.IdentityKey{Device: rigToken(subD), OccurredMicros: rigUs(subD, subK) + 1}
	if err := run("substitute", true, "per-device count", countPerDevice, subKey.String(), subNow.String()); err != nil {
		return nil, err
	}
	if tag, err := db.Exec(ctx, `UPDATE `+rigTable+` SET occurred_time = occurred_time - interval '1 microsecond' WHERE tenant_id = $1 AND device_token = $2 AND occurred_time = $3`,
		tenant, rigToken(subD), subAt.Add(time.Microsecond)); err != nil || tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("restore the substitution: %v (%v rows)", err, tag.RowsAffected())
	}
	if err := run("restore-2", false, "", nil); err != nil {
		return nil, err
	}

	rep.Sound = rep.ParityOK && len(rep.Stages) == 5
	for _, s := range rep.Stages {
		rep.Sound = rep.Sound && s.AsExpected
	}
	return rep, nil
}

// seed (re)creates the events table and fills it with devices x k rows, one second apart per
// device, device d offset by d microseconds so every (device, instant) pair is unique and no
// two devices share an instant.
func seed(ctx context.Context, db *pgx.Conn, tenant string, events, devices, k int) error {
	ddl := []string{
		`CREATE SCHEMA IF NOT EXISTS "event-management"`,
		`DROP TABLE IF EXISTS ` + rigTable,
		`CREATE TABLE ` + rigTable + ` (
			tenant_id text NOT NULL, event_id bytea NOT NULL, device_token varchar(128) NOT NULL,
			event_type int NOT NULL, occurred_time timestamptz NOT NULL, source text NOT NULL DEFAULT '',
			alt_id text, processed_time timestamptz NOT NULL,
			PRIMARY KEY (tenant_id, occurred_time, event_id))`,
		`CREATE INDEX idx_events_tenant_device_time ON ` + rigTable + ` (tenant_id, device_token, occurred_time DESC)`,
	}
	for _, s := range ddl {
		if _, err := db.Exec(ctx, s); err != nil {
			return fmt.Errorf("seed ddl: %w", err)
		}
	}
	if _, err := db.Exec(ctx, `SET synchronous_commit = off`); err != nil {
		return err
	}
	const chunk = 500
	for lo := 0; lo < devices; lo += chunk {
		hi := min(lo+chunk, devices) - 1
		if _, err := db.Exec(ctx, `
INSERT INTO `+rigTable+` (tenant_id, event_id, device_token, event_type, occurred_time, source, processed_time)
SELECT $1, sha256((d::text || ':' || j::text)::bytea), 'rig-' || lpad(d::text, 6, '0'), $2,
       timestamptz 'epoch' + ($3::bigint / 1000000) * interval '1 second' + j * interval '1 second' + d * interval '1 microsecond',
       '', now()
FROM generate_series($4::int, $5::int) d, generate_series(0, $6::int - 1) j`,
			tenant, loadtest.MeasurementEventType, rigBaseUs, lo, hi, k); err != nil {
			return fmt.Errorf("seed devices %d-%d: %w", lo, hi, err)
		}
	}
	if _, err := db.Exec(ctx, `ANALYZE `+rigTable); err != nil {
		return err
	}
	return nil
}
