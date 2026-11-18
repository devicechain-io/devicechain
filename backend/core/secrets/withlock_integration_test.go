// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package secrets_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/test/secretstest"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWithLockIncrements(t *testing.T) {
	_, stores := secretstest.Stores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ref := secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: "t1", Name: "counter"}
	for _, locked := range []bool{true, false} {
		require.NoError(t, stores[0].Put(ctx, ref, []byte("0")))
		for round := 0; round < 20; round++ {
			start := make(chan struct{})
			read := make(chan struct{}, 2)
			write := make(chan struct{})
			done := make(chan error, 2)
			for _, st := range stores {
				go func(st secrets.SecretStore) {
					<-start
					fn := func(v secrets.SecretStore) error {
						b, err := v.Resolve(ctx, ref)
						if err != nil {
							return err
						}
						n, err := strconv.Atoi(string(b))
						if err != nil {
							return err
						}
						if !locked {
							read <- struct{}{}
							select {
							case <-write:
							case <-ctx.Done():
								return ctx.Err()
							}
						}
						time.Sleep(5 * time.Millisecond)
						return v.Put(ctx, ref, []byte(strconv.Itoa(n+1)))
					}
					if locked {
						_, err := secrets.WithLock(ctx, st, "t1", "counter", fn)
						done <- err
					} else {
						done <- fn(st)
					}
				}(st)
			}
			close(start)
			if !locked {
				for i := 0; i < 2; i++ {
					select {
					case <-read:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				close(write)
			}
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		}
		value, err := stores[0].Resolve(ctx, ref)
		require.NoError(t, err)
		if locked {
			require.Equal(t, "40", string(value))
		} else {
			require.Equal(t, "20", string(value), "unlocked negative control must lose updates")
		}
	}
}

func TestIntegrationWithLockIsolationAndErrorRelease(t *testing.T) {
	admin, stores := secretstest.Stores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, pair := range [][2]string{{"other", "b:c"}, {"a:b", "c"}, {"a", "b:c"}} {
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		boom := errors.New("callback failed")
		go func() {
			_, err := secrets.WithLock(ctx, stores[0], "a", "b:c", func(secrets.SecretStore) error {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return boom
			})
			done <- err
		}()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		probeCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
		ran := false
		_, err := secrets.WithLock(probeCtx, stores[1], pair[0], pair[1], func(secrets.SecretStore) error { ran = true; return nil })
		stop()
		close(release)
		require.ErrorIs(t, <-done, boom)
		if pair[0] == "a" {
			require.Error(t, err)
			require.False(t, ran)
		} else {
			require.NoError(t, err)
			require.True(t, ran)
		}
		_, err = secrets.WithLock(ctx, stores[1], "a", "b:c", func(secrets.SecretStore) error { return nil })
		require.NoError(t, err, "error must release transaction lock")
		var n int64
		require.NoError(t, admin.WithContext(ctx).Raw(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND classid::bigint = ((hashtextextended('secrets:1:a:b:c', 0) >> 32) & 4294967295) AND objid::bigint = (hashtextextended('secrets:1:a:b:c', 0) & 4294967295)`).Scan(&n).Error)
		require.Zero(t, n)
	}
}
