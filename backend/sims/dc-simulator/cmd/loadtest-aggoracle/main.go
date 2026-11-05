// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// dc-loadtest-aggoracle is the aggregate identity oracle's command: it checks what a load run
// sent against what the event store holds, per device, with one grouped query per tenant
// instead of a paged read of every event.
//
//	dc-loadtest-aggoracle verify --ledger run-a.json [--ledger run-b.json ...]   # check real runs
//	dc-loadtest-aggoracle rig --scratch --events 10000000 --devices 10000        # prove the oracle itself
//
// verify reads the aggregate ledgers the harnesses wrote (--aggregate-ledger on dc-loadtest and
// dc-loadtest-mqtt), asks the event store for each tenant's per-device (count, hash sum) and
// reports every device that disagrees, reading those devices (and only those) exactly. rig
// seeds a scratch database, perturbs it one row at a time and shows the oracle red where it
// must be red and green where it must be green; it never touches a database it did not just
// fill, and refuses to run without --scratch.
//
// Unlike dc-loadtest this command needs DIRECT, read-only access to the event store's Postgres
// (the harness proper is an untrusted client with none); reach it with a port-forward and pass
// the DSN in DC_EVENTSTORE_DSN so the password stays out of the process list.
//
// Exit codes: 0 every ledger reconciled; 1 a ledger did not (loss, duplicates, unexpected rows,
// or inconclusive); 2 the check could not be carried out.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-simulator/loadtest"
	"github.com/devicechain-io/dc-simulator/loadtest/pgperturb"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitError = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitError)
	}
	var code int
	switch os.Args[1] {
	case "verify":
		code = verify(os.Args[2:])
	case "rig":
		code = rig(os.Args[2:])
	default:
		usage()
		code = exitError
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: dc-loadtest-aggoracle verify --ledger FILE [--ledger FILE...] | rig --scratch [flags]")
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// verifyResult is one ledger's outcome, in the command's JSON report.
type verifyResult struct {
	Ledger string                   `json:"ledger"`
	Report loadtest.AggregateReport `json:"report"`
	Passed bool                     `json:"passed"`
}

func verify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	var ledgers listFlag
	fs.Var(&ledgers, "ledger", "an aggregate ledger file a harness wrote (repeatable: one per harness)")
	dsn := fs.String("dsn", "", "event-store Postgres DSN (prefer DC_EVENTSTORE_DSN: a flag shows in the process list)")
	settle := fs.Duration("settle", 10*time.Second, "read the aggregates twice this far apart and refuse to decide if they moved (0 reads once)")
	noExact := fs.Bool("no-exact", false, "do not read mismatching devices exactly; judge them by counts alone (lower bounds)")
	report := fs.String("report", "", "write the JSON report to this path")
	_ = fs.Parse(args)
	ledgers = append(ledgers, fs.Args()...)
	if len(ledgers) == 0 {
		fmt.Fprintln(os.Stderr, "verify: at least one --ledger is required")
		return exitError
	}
	conn := *dsn
	if conn == "" {
		conn = os.Getenv("DC_EVENTSTORE_DSN")
	}
	if conn == "" {
		fmt.Fprintln(os.Stderr, "verify: --dsn or DC_EVENTSTORE_DSN is required")
		return exitError
	}
	ctx := context.Background()

	var (
		results []verifyResult
		allOK   = true
	)
	for _, path := range ledgers {
		led, err := loadtest.ReadAggregateLedger(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify:", err)
			return exitError
		}
		o, err := pgperturb.NewOracle(ctx, conn, led.Tenant)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify:", err)
			return exitError
		}
		opt := loadtest.AggregateOptions{Settle: *settle}
		if !*noExact {
			opt.Exact = o
		}
		rep, inv, err := loadtest.CheckAggregate(ctx, o, led, opt)
		_ = o.Close(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "verify:", path, err)
			return exitError
		}
		results = append(results, verifyResult{Ledger: path, Report: rep, Passed: inv.Passed})
		mark := "ok"
		if !inv.Passed {
			mark = "FAIL"
			allOK = false
		}
		fmt.Fprintf(os.Stderr, "[%-4s] tenant %s (%s): %s\n", mark, led.Tenant, path, inv.Detail)
	}
	if *report != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*report, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "verify: write report:", err)
			return exitError
		}
	}
	if !allOK {
		return exitFail
	}
	return exitOK
}
