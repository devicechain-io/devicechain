// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package rdbtest is support for harnesses that run a service's real database startup
// against a throwaway PostgreSQL: integration tests, and the schema tools under
// backend/tools. It also holds statement-counting support (StatementCounter) for tests
// and benchmarks that assert what a write path costs.
//
// 🔴 NOTHING A SERVICE RUNS MAY IMPORT IT. No service creates its database — the
// instance database exists before any service starts, created by dcctl and owned by
// the instance's own login, and that login cannot create databases. A harness has no
// dcctl in front of it, so it stands in for that one step here, and only here.
package rdbtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"github.com/devicechain-io/dc-microservice/core"
	pgx "github.com/jackc/pgx/v5"
)

// EnsureDatabase creates database name if it does not exist, owned by owner — or by the
// connecting user when owner is empty — the way dcctl does before any service starts.
//
// It connects to the `postgres` maintenance database as user, which must be able to
// create databases (and, for a different owner, to act as that role). Retried like a
// service's own startup, because it is typically the first connection to a container
// that is still settling.
func EnsureDatabase(ctx context.Context, host string, port int, user, password, name, owner string) error {
	if name == "" {
		return errors.New("rdbtest: a database name is required")
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/postgres",
		RawQuery: url.Values{"sslmode": []string{"disable"}, "connect_timeout": []string{"5"}}.Encode(),
	}
	return core.RetryInfraConnect(ctx, "postgres", func(ctx context.Context) error {
		conn, err := pgx.Connect(ctx, u.String())
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())

		var exists bool
		if err := conn.QueryRow(ctx,
			`select exists(select 1 from pg_database where datname = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
		if owner != "" {
			stmt += " OWNER " + pgx.Identifier{owner}.Sanitize()
		}
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("rdbtest: creating database %q: %w", name, err)
		}
		return nil
	})
}
