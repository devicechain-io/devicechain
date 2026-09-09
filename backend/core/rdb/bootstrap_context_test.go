// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// silentPostgres is a listener that ACCEPTS the connection and then says nothing.
//
// That is the case this file is about, and it is deliberately not "a port nothing is
// listening on": a refused connection fails instantly, so it cannot tell a bounded
// dial apart from an unbounded one. A host that completes the TCP handshake and then
// never speaks the startup protocol is what a blackholing database looks like, and it
// is the only shape in which a connect attempt actually blocks.
func silentPostgres(t *testing.T) (string, int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Held, not closed: closing would give the client an EOF to fail on, which
			// is the fast path this fixture exists to avoid.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("splitting the listener address: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parsing the listener port: %v", err)
	}
	return host, int32(port)
}

func silentPgConfig(t *testing.T) *PostgresConfig {
	host, port := silentPostgres(t)
	return &PostgresConfig{
		Hostname: host, Port: port,
		Username: "devicechain", Password: "devicechain", SslMode: "disable",
	}
}

// A CANCELLED STARTUP MUST NOT WAIT OUT THE DIAL IT IS INSIDE.
//
// bootstrapPostgres runs inside RetryInfraConnect, whose single advertised property is
// that it returns as soon as its context is cancelled. That check happens BETWEEN
// attempts, so it was worth nothing while an attempt itself ran on
// context.Background(): a cancellation arriving mid-dial was not observed until the
// dial ended on its own, and against a host that accepts and then goes quiet it does
// not end on its own at all.
//
// The elapsed bound is the assertion. Returning an error is not evidence — the
// unbounded version returns an error too, five seconds later.
func TestBootstrapStopsDiallingWhenItsContextIsCancelled(t *testing.T) {
	rdb := rdbFixture(t, nil)
	pg := silentPgConfig(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	type result struct {
		err     error
		elapsed time.Duration
	}
	got := make(chan result, 1)
	go func() {
		start := time.Now()
		err := rdb.bootstrapPostgres(ctx, pg)
		got <- result{err, time.Since(start)}
	}()

	// 🔴 Bounded, with a message naming the absence: with neither the context nor the
	// connect bound this call never returns, and a hung test reports nothing.
	select {
	case r := <-got:
		if r.err == nil {
			t.Fatal("a bootstrap against a database that never answered must not report success")
		}
		if r.elapsed > 2*time.Second {
			t.Errorf("bootstrapPostgres took %s after a cancellation at 100ms. The context is not "+
				"reaching pgx.Connect, so the retry loop's promise to give up on cancellation "+
				"cannot be kept while an attempt is in flight", r.elapsed)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("bootstrapPostgres never returned after its context was cancelled; a service asked " +
			"to stop while it is bringing up its database has nothing that can interrupt it")
	}
}

// 🔴 THE COUNTERWEIGHT, and it carries two separate loads.
//
// First, every assertion above is satisfied by a bootstrap that gives up instantly and
// never dials at all. This one is not cancelled, so it must actually attempt the work.
//
// Second, it is the gate on the connect bound itself. A context is what a caller uses
// to say "stop"; it is not what makes an attempt end when nobody says anything. Against
// this fixture, with no connect_timeout on the connection string, the dial waits
// forever — so the UPPER bound below is what proves the string carries one.
func TestAnUncancelledBootstrapStillDialsAndStillGivesUpOnItsOwn(t *testing.T) {
	rdb := rdbFixture(t, nil)
	pg := silentPgConfig(t)

	got := make(chan time.Duration, 1)
	errs := make(chan error, 1)
	go func() {
		start := time.Now()
		err := rdb.bootstrapPostgres(context.Background(), pg)
		errs <- err
		got <- time.Since(start)
	}()

	select {
	case elapsed := <-got:
		if err := <-errs; err == nil {
			t.Fatal("a bootstrap against a database that never answered must not report success")
		}
		if elapsed < time.Second {
			t.Errorf("bootstrapPostgres gave up after %s without a cancellation. It is not "+
				"attempting the connection at all, which would make the cancellation test above "+
				"vacuous", elapsed)
		}
		if elapsed > 30*time.Second {
			t.Errorf("bootstrapPostgres took %s with nothing cancelling it: the connection string "+
				"carries no connect_timeout, so one attempt of a bounded retry loop is unbounded",
				elapsed)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("bootstrapPostgres never returned against a host that accepts and then says " +
			"nothing: nothing bounds the dial, so the ~1 minute retry budget is unbounded in practice")
	}
}

// Every connection this package builds must carry a bound on the dial, and pgconn has
// to agree that it does — asserting the substring would pass for a string the parser
// rejects or reads differently.
//
// It runs over the SAME builder table the sslMode contract uses, so a builder added
// later is held to this too rather than to whichever properties its author remembered.
func TestEveryBuilderBoundsTheConnectAttempt(t *testing.T) {
	for _, b := range builders() {
		t.Run(b.fn, func(t *testing.T) {
			got, err := b.build(t, basePg("require"))
			if err != nil {
				t.Fatalf("%s: %v", b.fn, err)
			}
			cfg, err := pgconn.ParseConfig(got)
			if err != nil {
				t.Fatalf("pgconn could not parse the connection string %s produced: %v", b.fn, err)
			}
			if cfg.ConnectTimeout <= 0 {
				t.Errorf("%s produced a connection string with no connect_timeout. A dial to a host "+
					"that accepts and then goes silent never ends on its own, so the retry budget "+
					"wrapped around it bounds nothing", b.fn)
			}
		})
	}
}
