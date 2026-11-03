// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A batch delivered again after it committed (a broker roll, an ack lost before the commit)
// is written with plain INSERTs first, meets rows that are already stored, and is written
// again with ON CONFLICT: nothing is stored twice, nothing errors, every message is
// acknowledged, and the redelivery costs a single transaction, not a retry loop. A second
// batch that overlaps the first in only some messages stores just the new ones.
func TestARedeliveredBatchIsAbsorbedOnPostgres(t *testing.T) {
	r := newPgBatchRig(t, "embatchtest", 8)
	tenant := fmt.Sprintf("rd%d", time.Now().UnixNano())
	at := time.Now().UTC().Truncate(time.Hour)
	send := func(from, to int) {
		var msgs []messaging.Message
		for i := from; i < to; i++ {
			msgs = append(msgs, consumed(t, r.acks, i, tenant, 1, batchEvent(i, false, at)))
		}
		r.run(msgs)
	}
	counts := func() (e, m int64) {
		db := r.mgr.DB(core.WithTenant(context.Background(), tenant))
		if err := db.Model(&model.Event{}).Count(&e).Error; err != nil {
			t.Fatalf("count events: %v", err)
		}
		if err := db.Model(&model.MeasurementEvent{}).Count(&m).Error; err != nil {
			t.Fatalf("count measurements: %v", err)
		}
		return
	}
	send(0, 8)
	if e, m := counts(); e != 8 || m != 24 {
		t.Fatalf("first delivery stored %d events / %d measurements; want 8 / 24", e, m)
	}
	before := r.api.txs.Load()
	send(0, 8) // the same batch again
	if got := r.api.txs.Load() - before; got != 1 {
		t.Errorf("the redelivery took %d transactions; want 1", got)
	}
	if e, m := counts(); e != 8 || m != 24 {
		t.Errorf("after the redelivery %d events / %d measurements are stored; want 8 / 24", e, m)
	}
	send(4, 12) // half old, half new
	if e, m := counts(); e != 12 || m != 36 {
		t.Errorf("after the overlapping batch %d events / %d measurements are stored; want 12 / 36", e, m)
	}
	if len(r.failures) != 0 {
		t.Errorf("reported failures: %+v", r.failures)
	}
}
