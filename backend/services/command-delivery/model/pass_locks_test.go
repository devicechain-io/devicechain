// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// passLockServer models only the server's session lock responses. This unit
// test checks the three API methods' wiring; real sessions are tested separately.
type passLockServer struct {
	mu   sync.Mutex
	held map[int64]bool
}
type passLockConn struct{ server *passLockServer }
type passLockRows struct {
	acquired bool
	done     bool
}

func (s *passLockServer) Connect(context.Context) (driver.Conn, error) { return &passLockConn{s}, nil }
func (s *passLockServer) Driver() driver.Driver                        { return passLockDriver{} }

type passLockDriver struct{}

func (passLockDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }
func (*passLockConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*passLockConn) Close() error              { return nil }
func (*passLockConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c *passLockConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query != "SELECT pg_try_advisory_lock($1)" || len(args) != 1 {
		return nil, errors.New("unexpected lock query")
	}
	key := args[0].Value.(int64)
	c.server.mu.Lock()
	defer c.server.mu.Unlock()
	acquired := !c.server.held[key]
	if acquired {
		c.server.held[key] = true
	}
	return &passLockRows{acquired: acquired}, nil
}
func (c *passLockConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query != "SELECT pg_advisory_unlock($1)" || len(args) != 1 {
		return nil, errors.New("unexpected unlock query")
	}
	c.server.mu.Lock()
	defer c.server.mu.Unlock()
	delete(c.server.held, args[0].Value.(int64))
	return driver.RowsAffected(1), nil
}
func (*passLockRows) Columns() []string { return []string{"acquired"} }
func (*passLockRows) Close() error      { return nil }
func (r *passLockRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	values[0] = r.acquired
	return nil
}

type passLocker func(context.Context, func() error) (bool, error)

func passLockers(api *Api) []passLocker {
	return []passLocker{api.TrySweepLock, api.TryReconcileLock, api.TryStrandedLock}
}

func TestPassLocksExcludePeersButNotOtherPasses(t *testing.T) {
	server := &passLockServer{held: make(map[int64]bool)}
	replica := func() *Api {
		pool := sql.OpenDB(server)
		t.Cleanup(func() { require.NoError(t, pool.Close()) })
		return NewApi(&rdb.RdbManager{Database: &gorm.DB{Config: &gorm.Config{ConnPool: pool}}})
	}
	a, b := passLockers(replica()), passLockers(replica())
	ctx := context.Background()
	for i, lock := range a {
		ran, err := lock(ctx, func() error {
			for j, peer := range b {
				called := false
				ran, err := peer(ctx, func() error { called = true; return nil })
				require.NoError(t, err)
				require.Equal(t, i != j, ran, "holder pass %d, contender pass %d", i, j)
				require.Equal(t, ran, called)
			}
			return nil
		})
		require.NoError(t, err)
		require.True(t, ran)
		called := false
		ran, err = b[i](ctx, func() error { called = true; return nil })
		require.NoError(t, err)
		require.True(t, ran, "peer must acquire after the holder returns")
		require.True(t, called)
	}
}
