// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// lockRead is one FOR UPDATE read of detect_snapshots as the query callback saw it.
type lockRead struct {
	sql        string   // the statement as built (the sqlite dialector omits FOR UPDATE; Postgres keeps it)
	projection []string // normalized SELECT list, e.g. [stream_seq watermark] or [*]
	destBytes  int      // sum of len() over the []byte and string fields of the scanned Dest
}

// recordLockReads registers a Query-After callback on db that records every
// detect_snapshots query carrying a Locking clause. A lock read is identified by
// Statement.Clauses["FOR"], NOT by searching the SQL text, because the sqlite dialector
// drops FOR UPDATE from the SQL it builds (SQLite has no row locks) while leaving the
// clause on the statement.
func recordLockReads(tb testing.TB, db *gorm.DB) func() []lockRead {
	tb.Helper()
	var (
		mu    sync.Mutex
		reads []lockRead
	)
	const name = "test:record_snapshot_lock_reads"
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		// A suffix, not an equality: on Postgres the naming strategy qualifies the table
		// with the functional-area schema ("event-processing.detect_snapshots").
		if !strings.HasSuffix(tx.Statement.Table, "detect_snapshots") {
			return
		}
		if _, locked := tx.Statement.Clauses["FOR"]; !locked {
			return
		}
		r := lockRead{
			sql:        tx.Statement.SQL.String(),
			projection: selectList(tx.Statement.SQL.String()),
			destBytes:  stringAndByteLen(reflect.ValueOf(tx.Statement.Dest)),
		}
		mu.Lock()
		reads = append(reads, r)
		mu.Unlock()
	})
	if err != nil {
		tb.Fatalf("register lock-read recorder: %v", err)
	}
	tb.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return func() []lockRead {
		mu.Lock()
		defer mu.Unlock()
		return append([]lockRead(nil), reads...)
	}
}

// selectList returns the sorted, unquoted, unqualified column list between SELECT and the
// first FROM of sql.
func selectList(sql string) []string {
	s := strings.TrimSpace(sql)
	s = strings.TrimPrefix(s, "SELECT ")
	if i := strings.Index(s, " FROM "); i >= 0 {
		s = s[:i]
	}
	var cols []string
	for _, c := range strings.Split(s, ",") {
		c = strings.Trim(strings.TrimSpace(c), "`\"")
		if i := strings.LastIndex(c, "."); i >= 0 {
			c = strings.Trim(c[i+1:], "`\"")
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}

// stringAndByteLen sums len() over every string and []byte field of the (dereferenced)
// struct v, descending into embedded or nested structs other than time.Time. It is generic
// on purpose — it does not special-case Payload — so it measures what the read actually
// brought into the process.
func stringAndByteLen(v reflect.Value) int {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	n := 0
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch {
		case f.Kind() == reflect.String:
			n += f.Len()
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Uint8:
			n += f.Len()
		case f.Kind() == reflect.Struct && f.Type() != reflect.TypeOf(time.Time{}):
			n += stringAndByteLen(f)
		}
	}
	return n
}

// Save and Reset lock the checkpoint row FOR UPDATE and then compare only its sequence and
// watermark. The lock read must project exactly those two columns: on Postgres a wider read
// detoasts and ships the PREVIOUS engine-state payload on every checkpoint, inside the lock
// window, only to throw it away.
//
// The assertion is the exact column list, not "payload is absent": the previous code
// selected *, which names no column at all, so an absence check would have passed on it.
// The count pins that the read still takes the lock — a narrowed read that dropped it would
// record zero reads here.
func TestSnapshotLockReadProjectsOnlyTheComparedColumns(t *testing.T) {
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		op   func(ctx context.Context, s *SnapshotStore) error
	}{
		{"Save", func(ctx context.Context, s *SnapshotStore) error {
			return s.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 101, Watermark: wm, Payload: []byte("next")})
		}},
		{"Reset", func(ctx context.Context, s *SnapshotStore) error {
			return s.Reset(ctx, "singleton", 100)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			if err := store.Save(ctx, &DetectSnapshot{
				PartitionId: "singleton", StreamSeq: 100, Watermark: wm,
				Payload: bytes.Repeat([]byte{0xAB}, 1<<20),
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			reads := recordLockReads(t, store.rdb.Database)

			if err := tc.op(ctx, store); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			got := reads()
			if len(got) != 1 {
				t.Fatalf("%s took %d FOR UPDATE reads of detect_snapshots, want exactly 1", tc.name, len(got))
			}
			if want := []string{"stream_seq", "watermark"}; !reflect.DeepEqual(got[0].projection, want) {
				t.Fatalf("lock read projected %v (%d bytes scanned), want %v: the checkpoint lock must "+
					"not transfer the previous payload", got[0].projection, got[0].destBytes, want)
			}
			if got[0].destBytes != 0 {
				t.Fatalf("lock read scanned %d bytes of string/[]byte data, want 0", got[0].destBytes)
			}
		})
	}
}

// BenchmarkSnapshotSave reports the bytes Save's lock read brings into the process per
// checkpoint (lockread-B/op), at three previous-payload sizes. lockreads/op must read 1.0;
// any other value means the counter is not seeing the read and its byte figure is not a
// reading. On SQLite the read is a memcpy, so ns/op here is NOT the gain; on Postgres the
// same bytes are detoasted and sent over the wire inside the row lock. The write of the new
// payload is unchanged by design and is not counted.
func BenchmarkSnapshotSave(b *testing.B) {
	wm := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, sz := range []struct {
		name string
		n    int
	}{{"64KiB", 64 << 10}, {"1MiB", 1 << 20}, {"8MiB", 8 << 20}} {
		b.Run(sz.name, func(b *testing.B) {
			store := newTestStore(b)
			ctx := context.Background()
			payload := bytes.Repeat([]byte{0xAB}, sz.n)
			if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: 1, Watermark: wm, Payload: payload}); err != nil {
				b.Fatalf("seed: %v", err)
			}
			reads := recordLockReads(b, store.rdb.Database)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.Save(ctx, &DetectSnapshot{PartitionId: "singleton", StreamSeq: int64(i + 2), Watermark: wm, Payload: payload}); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			got := reads()
			total := 0
			for _, r := range got {
				total += r.destBytes
			}
			b.ReportMetric(float64(total)/float64(b.N), "lockread-B/op")
			b.ReportMetric(float64(len(got))/float64(b.N), "lockreads/op")
		})
	}
}
