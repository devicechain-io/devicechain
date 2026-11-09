// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lpwan

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// ---- an in-memory PendingStore ----

type memPending struct {
	mu      sync.Mutex
	next    uint
	rows    map[uint]Pending
	addErr  error
	findErr error
	markErr error
	delErr  error
	listErr error
	// claimErr fails Claim; lease is each record's settle lease.
	claimErr error
	lease    map[uint]time.Time
	// heldDeleteRefused counts deletes that did not hold the lease.
	heldDeleteRefused int
}

func newMemPending() *memPending {
	return &memPending{rows: map[uint]Pending{}, lease: map[uint]time.Time{}}
}

func (m *memPending) Add(_ context.Context, p Pending) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.addErr != nil {
		return m.addErr
	}
	for _, r := range m.rows {
		if r.Tenant == p.Tenant && r.QueueItemID == p.QueueItemID {
			return errors.New("duplicate queue item")
		}
	}
	m.next++
	p.ID = m.next
	m.rows[p.ID] = p
	return nil
}

func (m *memPending) Lookup(_ context.Context, tenant, id string) (*Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	for _, r := range m.rows {
		if r.Tenant == tenant && r.QueueItemID == id {
			c := r
			return &c, nil
		}
	}
	return nil, ErrNoPending
}

func (m *memPending) MarkTransmitted(_ context.Context, _ string, id uint, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markErr != nil {
		return m.markErr
	}
	r := m.rows[id]
	if r.TransmittedAt == nil {
		r.TransmittedAt = &at
		m.rows[id] = r
	}
	return nil
}

func (m *memPending) Claim(_ context.Context, _ string, id uint, now time.Time, lease time.Duration) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return time.Time{}, false, m.claimErr
	}
	if _, ok := m.rows[id]; !ok || m.lease[id].After(now) {
		return time.Time{}, false, nil
	}
	m.lease[id] = now.Add(lease)
	return m.lease[id], true, nil
}

func (m *memPending) Delete(_ context.Context, _ string, id uint, token time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErr != nil {
		return m.delErr
	}
	if !m.lease[id].Equal(token) {
		m.heldDeleteRefused++
		return nil
	}
	delete(m.rows, id)
	return nil
}

func (m *memPending) DeleteGone(_ context.Context, _ string, id uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.rows, id)
	return nil
}

func (m *memPending) Expired(_ context.Context, now time.Time, limit int) ([]Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []Pending
	for _, r := range m.rows {
		if !r.DeadlineAt.After(now) && !m.lease[r.ID].After(now) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memPending) size() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.rows) }

type ackCounts struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *ackCounts) CountAck(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	c.m[r]++
}
func (c *ackCounts) get(r string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.m[r] }

// ---- the rig ----

const (
	reportSubject = "inst.acme.lpwan-downlink-events"
	itemID        = "5e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f11"
)

type ackRig struct {
	f    *AckFeed
	resp *respWriter
	pend *memPending
	m    *ackCounts
	now  time.Time
	gone map[string]bool
}

func newAckRig(t *testing.T) *ackRig {
	t.Helper()
	r := &ackRig{resp: &respWriter{}, pend: newMemPending(), m: &ackCounts{}, now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
		gone: map[string]bool{}}
	r.f = NewAckFeed(&nopReader{}, r.resp, r.pend, r.m, AckFeedOptions{
		Workers: 1, ReadPacer: core.NewReadPacer(nil, "test"), Now: func() time.Time { return r.now },
		TenantDeleted:    func(t string) bool { return r.gone[t] },
		ResponseAttempts: 1, ResponseBackoff: time.Millisecond,
	})
	return r
}

// seed stores the pending downlink the happy-path reports match.
func (r *ackRig) seed(t *testing.T, mut func(*Pending)) Pending {
	t.Helper()
	p := Pending{Tenant: "acme", Integration: "cs1", DeviceToken: "dev-1", DevEUI: "0102030405060708",
		CommandToken: "cmd-1", DispatchNonce: "claimed-nonce", QueueItemID: itemID,
		DeadlineAt: r.now.Add(time.Hour)}
	if mut != nil {
		mut(&p)
	}
	require.NoError(t, r.pend.Add(context.Background(), p))
	return p
}

func report(kind string, acked bool) cmddownlink.DownlinkReport {
	return cmddownlink.DownlinkReport{Integration: "cs1", Kind: kind, DevEUI: "0102030405060708",
		QueueItemID: itemID, Acknowledged: acked, FCntDown: 7}
}

func (r *ackRig) deliver(t *testing.T, rep any, numDelivered int) *fakeAck {
	t.Helper()
	var b []byte
	switch v := rep.(type) {
	case []byte:
		b = v
	default:
		var err error
		b, err = json.Marshal(v)
		require.NoError(t, err)
	}
	a := &fakeAck{}
	r.f.handle(context.Background(), messaging.NewConsumedMessage(reportSubject, b, numDelivered, nil, a))
	return a
}

// ---- the mapping onto delivery states ----

func TestAnAcknowledgementAnswersTheCommandSuccessWithTheClaimNonce(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	a := r.deliver(t, report("ack", true), 1)

	require.True(t, a.acked)
	require.Len(t, r.resp.envs, 1)
	e := r.resp.envs[0]
	require.Equal(t, []string{"dev-1"}, r.resp.devices)
	require.True(t, e.Success)
	require.Equal(t, "cmd-1", e.CommandToken)
	require.Equal(t, "claimed-nonce", e.DispatchNonce, "the answer quotes the claim's nonce stored at enqueue")
	require.Empty(t, e.Reach)
	require.JSONEq(t, `{"stage":"acknowledged","queueItemId":"`+itemID+`","fCntDown":7}`, *e.Payload)
	require.Zero(t, r.pend.size(), "a settled downlink's record is removed")
	require.Equal(t, 1, r.m.get(AckAcknowledged))
}

func TestANetworkServerTimeoutOfTheConfirmationIsTimeoutNotNotDelivered(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	a := r.deliver(t, report("ack", false), 1)

	require.True(t, a.acked)
	require.Len(t, r.resp.envs, 1)
	e := r.resp.envs[0]
	require.False(t, e.Success)
	// UNANSWERED is recorded by command-delivery as TIMEOUT / UNKNOWN. UNSENT would offer a retry as
	// proven safe, and the device may have heard the frame.
	require.Equal(t, cmddownlink.ReachUnanswered, e.Reach)
	require.Equal(t, "claimed-nonce", e.DispatchNonce)
	require.JSONEq(t, `{"reason":"NOT_ACKNOWLEDGED"}`, *e.Payload)
	require.Zero(t, r.pend.size())
	require.Equal(t, 1, r.m.get(AckTimedOut))
}

func TestATxAckIsRecordedAndDoesNotSettleTheCommand(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	a := r.deliver(t, report("txack", false), 1)

	require.True(t, a.acked)
	require.Empty(t, r.resp.envs, "a gateway taking the frame is not the device hearing it")
	require.Equal(t, 1, r.pend.size())
	for _, p := range r.pend.rows {
		require.NotNil(t, p.TransmittedAt)
	}
	require.Equal(t, 1, r.m.get(AckTransmitted))

	// The acknowledgement that follows still settles it.
	r.deliver(t, report("ack", true), 1)
	require.Len(t, r.resp.envs, 1)
	require.True(t, r.resp.envs[0].Success)
}

func TestNoReportByTheDeadlineAnswersTheCommandUnanswered(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(10 * time.Minute) })
	r.seed(t, func(p *Pending) { p.QueueItemID = "6e0a6c1e-6a1c-4f38-9d2b-0c7a2b7d9f22"; p.CommandToken = "cmd-2" })

	r.f.Sweep(context.Background())
	require.Empty(t, r.resp.envs, "nothing is overdue yet")
	require.Equal(t, 2, r.pend.size())

	r.now = r.now.Add(11 * time.Minute)
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 1, "only the overdue downlink is answered")
	e := r.resp.envs[0]
	require.Equal(t, "cmd-1", e.CommandToken)
	require.Equal(t, cmddownlink.ReachUnanswered, e.Reach)
	require.Equal(t, "claimed-nonce", e.DispatchNonce)
	require.JSONEq(t, `{"reason":"NO_ACKNOWLEDGEMENT"}`, *e.Payload)
	require.Equal(t, 1, r.pend.size(), "the other downlink keeps waiting")
	require.Equal(t, 1, r.m.get(AckDeadline))

	// Swept once means answered once: the record is gone.
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 1)
}

func TestTheDeadlineAnswerSaysWhetherAGatewayTransmittedIt(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.deliver(t, report("txack", false), 1)
	r.now = r.now.Add(2 * time.Hour)
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 1)
	require.Contains(t, *r.resp.envs[0].Error, "transmitted")
}

func TestAnOverdueDownlinkOfADeletedTenantIsDroppedWithoutAnAnswer(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(-time.Minute) })
	r.gone["acme"] = true
	r.f.Sweep(context.Background())
	require.Empty(t, r.resp.envs)
	require.Zero(t, r.pend.size())
}

// A purge in progress fences the tenant's writes, so a claim of its row fails. The sweep must
// not try: it would count store_failed for every row, every pass, and leave them in the batch.
func TestASweepDoesNotClaimADeletedTenantsRows(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(-time.Minute) })
	r.gone["acme"] = true
	r.pend.claimErr = errors.New("tenant purged: writes fenced")
	r.f.Sweep(context.Background())
	require.Zero(t, r.m.get(AckStoreFailed), "a deleted tenant's row was claimed")
	require.Equal(t, 1, r.m.get(AckTenantGone))
	require.Zero(t, r.pend.size())
	require.Empty(t, r.resp.envs)
}

// A delete that fails is not a settled row: counting it as tenant_gone hides a store that cannot
// delete, while the row stays and fills the batch every pass. It is store_failed.
func TestASweepCountsAFailedGoneDeleteAsStoreFailed(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(-time.Minute) })
	r.gone["acme"] = true
	r.pend.delErr = errors.New("store unavailable")
	r.f.Sweep(context.Background())
	require.Equal(t, 1, r.m.get(AckStoreFailed))
	require.Zero(t, r.m.get(AckTenantGone), "a delete that failed was counted as settled")
	require.Equal(t, 1, r.pend.size(), "the row is still there")
}

// ---- never matched by guess ----

func TestAnAckForNoPendingDownlinkIsRetriedOnceThenDroppedLoudly(t *testing.T) {
	r := newAckRig(t)
	a := r.deliver(t, report("ack", true), 1)
	require.False(t, a.acked, "the first sight is left to redeliver: the record may still be being stored")
	require.Equal(t, 1, r.m.get(AckRetried))

	a = r.deliver(t, report("ack", true), 2)
	require.True(t, a.acked)
	require.Equal(t, 1, r.m.get(AckUnmatched))
	require.Empty(t, r.resp.envs, "an unmatched ack settles nothing")
}

func TestATxAckForNoPendingDownlinkIsTheOrdinaryUnconfirmedCase(t *testing.T) {
	r := newAckRig(t)
	a := r.deliver(t, report("txack", false), 1)
	require.True(t, a.acked)
	require.Equal(t, 1, r.m.get(AckTxUntracked))
	require.Zero(t, r.m.get(AckUnmatched), "every unconfirmed downlink produces a txack; it is not an anomaly")
	require.Empty(t, r.resp.envs)
}

func TestAReportFromAnotherIntegrationOrForAnotherDeviceSettlesNothing(t *testing.T) {
	for name, mut := range map[string]func(*cmddownlink.DownlinkReport){
		"another integration": func(rep *cmddownlink.DownlinkReport) { rep.Integration = "cs2" },
		"another device":      func(rep *cmddownlink.DownlinkReport) { rep.DevEUI = "AABBCCDDEEFF0011" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newAckRig(t)
			r.seed(t, nil)
			rep := report("ack", true)
			mut(&rep)
			a := r.deliver(t, rep, 1)
			require.True(t, a.acked, "dropped, not retried")
			require.Empty(t, r.resp.envs, "a queue item id alone is not enough to settle a command")
			require.Equal(t, 1, r.pend.size(), "the real downlink keeps waiting for its own report")
			require.Equal(t, 1, r.m.get(AckMismatch))

			// The paired positive: the matching report settles it.
			r.deliver(t, report("ack", true), 1)
			require.Len(t, r.resp.envs, 1)
		})
	}
}

func TestAReportIsMatchedWithinItsOwnTenantOnly(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.Tenant = "other" })
	a := &fakeAck{}
	b, _ := json.Marshal(report("ack", true))
	r.f.handle(context.Background(), messaging.NewConsumedMessage(reportSubject, b, 2, nil, a))
	require.True(t, a.acked)
	require.Empty(t, r.resp.envs, "acme's report must not settle another tenant's downlink")
	require.Equal(t, 1, r.pend.size())
	require.Equal(t, 1, r.m.get(AckUnmatched))
}

func TestMalformedReportsAreDroppedAndCounted(t *testing.T) {
	for name, raw := range map[string][]byte{
		"not json":       []byte("nope"),
		"no queue item":  []byte(`{"integration":"cs1","kind":"ack","devEui":"01"}`),
		"no integration": []byte(`{"kind":"ack","devEui":"01","queueItemId":"x"}`),
		"unknown kind":   []byte(`{"integration":"cs1","kind":"nack","devEui":"01","queueItemId":"x"}`),
		"no device":      []byte(`{"integration":"cs1","kind":"ack","queueItemId":"x"}`),
		"empty":          []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			r := newAckRig(t)
			r.seed(t, nil)
			a := r.deliver(t, raw, 1)
			require.True(t, a.acked)
			require.Equal(t, 1, r.m.get(AckPoison))
			require.Empty(t, r.resp.envs)
		})
	}
	r := newAckRig(t)
	a := &fakeAck{}
	r.f.handle(context.Background(), messaging.NewConsumedMessage("no-tenant-here", []byte(`{}`), 1, nil, a))
	require.True(t, a.acked)
	require.Equal(t, 1, r.m.get(AckPoison))
}

func TestADeletedTenantsReportIsDropped(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.gone["acme"] = true
	a := r.deliver(t, report("ack", true), 1)
	require.True(t, a.acked)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(AckTenantGone))
}

// ---- faults leave the report to redeliver ----

func TestAnAnswerThatCannotBePublishedLeavesTheReportAndTheRecord(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.resp.fail = errors.New("broker down")
	a := r.deliver(t, report("ack", true), 1)
	require.False(t, a.acked)
	require.Equal(t, 1, r.pend.size(), "the record must survive to answer on redelivery")
	require.Equal(t, 1, r.m.get(AckResponseFail))

	r.resp.fail = nil
	r.now = r.now.Add(SettleLease + time.Second) // the redelivery arrives after the lease
	a = r.deliver(t, report("ack", true), 2)
	require.True(t, a.acked)
	require.Len(t, r.resp.envs, 1)
	require.Zero(t, r.pend.size())
}

func TestAStoreFaultLeavesTheReportUnacked(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, nil)
	r.pend.findErr = errors.New("db down")
	a := r.deliver(t, report("ack", true), 1)
	require.False(t, a.acked)
	require.Equal(t, 1, r.m.get(AckStoreFailed))
	require.Empty(t, r.resp.envs)

	r.pend.findErr = nil
	r.pend.markErr = errors.New("db down")
	a = r.deliver(t, report("txack", false), 1)
	require.False(t, a.acked)
}

func TestAnAnswerThatLeftItsRecordIsRepeatedBySweepNotLost(t *testing.T) {
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(-time.Minute) })
	r.pend.delErr = errors.New("db down")
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, 1, r.pend.size())
	r.pend.delErr = nil
	r.now = r.now.Add(SettleLease + time.Second)
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 2, "the repeat is ignored by command-delivery for a terminal command")
	require.Zero(t, r.pend.size())
}

func TestASweepThatCannotListCountsTheFault(t *testing.T) {
	r := newAckRig(t)
	r.pend.listErr = errors.New("db down")
	r.f.Sweep(context.Background())
	require.Equal(t, 1, r.m.get(AckDeadlineFault))
}

func TestNewAckFeedPanicsWithoutWhatItNeeds(t *testing.T) {
	require.Panics(t, func() { NewAckFeed(nil, nil, nil, nil, AckFeedOptions{}) })
}

// ---- exactly one settler ----

func TestAReportAndTheSweepNeverAnswerTheSameDownlinkBothWays(t *testing.T) {
	// The sweep holds the record: a late acknowledgement must not also answer SUCCESSFUL.
	r := newAckRig(t)
	r.seed(t, func(p *Pending) { p.DeadlineAt = r.now.Add(-time.Minute) })
	r.f.Sweep(context.Background())
	require.Len(t, r.resp.envs, 1)
	require.Equal(t, cmddownlink.ReachUnanswered, r.resp.envs[0].Reach)

	// The converse: a report holds the record (its answer could not be published), so the sweep that
	// follows must not answer TIMEOUT beside it.
	r2 := newAckRig(t)
	r2.seed(t, func(p *Pending) { p.DeadlineAt = r2.now.Add(-time.Minute) })
	r2.resp.fail = errors.New("broker down")
	a := r2.deliver(t, report("ack", true), 1)
	require.False(t, a.acked)
	r2.resp.fail = nil
	r2.f.Sweep(context.Background())
	require.Empty(t, r2.resp.envs, "the report's lease keeps the sweep away")

	// The lease expires, so a settler that died mid-answer does not strand the command.
	r2.now = r2.now.Add(SettleLease + time.Second)
	a = r2.deliver(t, report("ack", true), 2)
	require.True(t, a.acked)
	require.Len(t, r2.resp.envs, 1)
	require.True(t, r2.resp.envs[0].Success)
}

func TestAReportThatLosesTheClaimIsLeftToRedeliver(t *testing.T) {
	r := newAckRig(t)
	p := r.seed(t, nil)
	r.pend.lease[1] = r.now.Add(time.Minute)
	_ = p
	a := r.deliver(t, report("ack", true), 1)
	require.False(t, a.acked)
	require.Empty(t, r.resp.envs)
	require.Equal(t, 1, r.m.get(AckContended))

	r.pend.claimErr = errors.New("db down")
	a = r.deliver(t, report("ack", true), 1)
	require.False(t, a.acked)
	require.Equal(t, 1, r.m.get(AckStoreFailed))
}
