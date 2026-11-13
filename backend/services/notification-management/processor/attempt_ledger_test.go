// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// memoryLedgerStore records every row it is given, or fails/blocks as planted. entered,
// when set, receives once per call that reaches the block.
type memoryLedgerStore struct {
	mu      sync.Mutex
	rows    []model.NotificationAttempt
	err     error
	block   chan struct{}
	entered chan struct{}
}

func (m *memoryLedgerStore) RecordNotificationAttempt(ctx context.Context, a *model.NotificationAttempt) error {
	if m.block != nil {
		if m.entered != nil {
			select {
			case m.entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-m.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, *a)
	return nil
}

// snapshot is the recorded rows ordered by channel, then attempt: the ledger has more
// than one writer, so arrival order is not the order the attempts were made in.
func (m *memoryLedgerStore) snapshot() []model.NotificationAttempt {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]model.NotificationAttempt(nil), m.rows...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ChannelToken != out[j].ChannelToken {
			return out[i].ChannelToken < out[j].ChannelToken
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out
}

func ledgerCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ledger_writes_total"}, []string{"outcome"})
}

func statuses(rows []model.NotificationAttempt) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s#%d", r.Status, r.Attempt))
	}
	return strings.Join(out, ",")
}

// Every adapter call is one ledger row, in order, numbered, timed and classified: two
// failures then a success read FAILED#1, FAILED#2, SENT#3.
func TestEveryAttemptIsALedgerRow(t *testing.T) {
	store := &memoryLedgerStore{}
	fa := &fakeAdapter{failTimes: 2}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.store = &fakeSecretStore{}
	n.Ledger = NewAttemptLedger(store, ledgerCounter())

	deliveries := []delivery{{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP),
		recipients: []string{"ops@x.com"}, policy: "p1"}}
	tally := n.deliverAll(tenantScoped(), deliveries, &RenderedNotification{Subject: "pump down"}, "a1",
		model.AttemptTriggerEscalation)
	n.Ledger.Wait(context.Background())

	if tally.delivered != 1 {
		t.Fatalf("tally = %+v, want delivered", tally)
	}
	rows := store.snapshot()
	if got := statuses(rows); got != "FAILED#1,FAILED#2,SENT#3" {
		t.Fatalf("ledger = %s, want FAILED#1,FAILED#2,SENT#3", got)
	}
	for _, r := range rows {
		if r.AlarmToken != "a1" || r.Trigger != model.AttemptTriggerEscalation ||
			r.ChannelToken != "smtp-1" || r.PolicyToken.String != "p1" || r.MaxAttempts != 3 {
			t.Fatalf("row carries the wrong delivery: %+v", r)
		}
		if string(r.Recipients) != `["ops@x.com"]` {
			t.Fatalf("recipients = %s", r.Recipients)
		}
		if !r.LatencyMillis.Valid {
			t.Fatalf("an attempt that called the adapter has no latency: %+v", r)
		}
		if len(r.SubjectHash) != 64 {
			t.Fatalf("subject hash = %q", r.SubjectHash)
		}
	}
	if rows[0].ErrorCode.String != model.AttemptErrorDelivery || rows[0].ErrorMessage.String != "boom" {
		t.Fatalf("failed row = %+v, want DELIVERY_ERROR / boom", rows[0])
	}
	if rows[2].ErrorCode.Valid || rows[2].ErrorMessage.Valid {
		t.Fatalf("a SENT row carries an error: %+v", rows[2])
	}
}

// A refused destination is one REFUSED row with its code, and nothing after it.
func TestARefusedAttemptIsRecordedAsRefused(t *testing.T) {
	store := &memoryLedgerStore{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &blockingAdapter{}})
	n.Ledger = NewAttemptLedger(store, nil)
	d := delivery{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP), recipients: []string{"x@x.com"}}

	if got := n.deliverWithRetry(tenantScoped(), d, &RenderedNotification{}); got != deliveryRefused {
		t.Fatalf("deliverWithRetry = %v, want refused", got)
	}
	n.Ledger.Wait(context.Background())
	rows := store.snapshot()
	if len(rows) != 1 || rows[0].Status != model.AttemptStatusRefused ||
		rows[0].ErrorCode.String != model.AttemptErrorEgressBlocked {
		t.Fatalf("ledger = %+v, want one REFUSED / EGRESS_BLOCKED row", rows)
	}
}

// Channels the budget never reached, and a channel whose secret could not be read, are
// recorded as NOT_ATTEMPTED with the reason — so "was this channel tried?" has an answer
// even when the answer is no.
func TestChannelsNeverTriedAreRecorded(t *testing.T) {
	store := &memoryLedgerStore{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &stallingAdapter{hardStop: 10 * time.Second}})
	n.attempts = 1
	n.timeout = time.Minute
	n.store = &fakeSecretStore{}
	n.Ledger = NewAttemptLedger(store, nil)
	deliveries := []delivery{
		{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP)},
		{channel: enabledChannel("smtp-2", model.ChannelTypeSMTP)},
	}
	ctx, cancel := context.WithTimeout(tenantScoped(), 100*time.Millisecond)
	defer cancel()
	n.deliverAll(ctx, deliveries, &RenderedNotification{}, "a1", model.AttemptTriggerAlarm)
	n.Ledger.Wait(context.Background())
	rows := store.snapshot()
	if len(rows) != 2 {
		t.Fatalf("ledger = %+v, want 2 rows", rows)
	}
	// The attempt in flight when the DISPATCH budget ran out was cut by the platform, not
	// failed by the channel: it is recorded as BUDGET_SPENT, never as the channel's TIMEOUT,
	// so it does not count against the channel's health.
	if rows[0].Status != model.AttemptStatusFailed || rows[0].ErrorCode.String != model.AttemptErrorBudgetSpent {
		t.Fatalf("first row = %+v, want FAILED / BUDGET_SPENT", rows[0])
	}
	if rows[1].ChannelToken != "smtp-2" || rows[1].Status != model.AttemptStatusNotAttempted ||
		rows[1].ErrorCode.String != model.AttemptErrorBudgetSpent || rows[1].LatencyMillis.Valid {
		t.Fatalf("second row = %+v, want smtp-2 NOT_ATTEMPTED / BUDGET_SPENT with no latency", rows[1])
	}

	store2 := &memoryLedgerStore{}
	n2 := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
	n2.store = &fakeSecretStore{err: context.DeadlineExceeded}
	n2.Ledger = NewAttemptLedger(store2, nil)
	n2.deliverAll(tenantScoped(), deliveries[:1], &RenderedNotification{}, "a1", model.AttemptTriggerAlarm)
	n2.Ledger.Wait(context.Background())
	if rows := store2.snapshot(); len(rows) != 1 || rows[0].ErrorCode.String != model.AttemptErrorSecretUnavailable {
		t.Fatalf("ledger = %+v, want one NOT_ATTEMPTED / SECRET_UNAVAILABLE row", rows)
	}
}

// 🔴 THE LEDGER NEVER DELAYS A PAGE. With the store planted to hang, delivery still
// completes at once and every channel is still sent; with it planted to fail, the page
// still goes out and the failure is COUNTED, which is the only way an operator can tell
// a missing row from a missing page.
func TestAFailingLedgerNeverBlocksDelivery(t *testing.T) {
	hung := &memoryLedgerStore{block: make(chan struct{})}
	defer close(hung.block)
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.store = &fakeSecretStore{}
	n.Ledger = NewAttemptLedger(hung, ledgerCounter())
	deliveries := []delivery{
		{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP)},
		{channel: enabledChannel("smtp-2", model.ChannelTypeSMTP)},
	}
	start := time.Now()
	tally := n.deliverAll(tenantScoped(), deliveries, &RenderedNotification{}, "a1", model.AttemptTriggerAlarm)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("delivery took %v behind a hung ledger; a ledger write must never be on the delivery path", elapsed)
	}
	if tally.delivered != 2 || fa.calls != 2 {
		t.Fatalf("tally = %+v calls = %d, want both channels delivered", tally, fa.calls)
	}

	writes := ledgerCounter()
	failing := &memoryLedgerStore{err: errors.New("database is down")}
	fa2 := &fakeAdapter{}
	n2 := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa2})
	n2.store = &fakeSecretStore{}
	n2.Ledger = NewAttemptLedger(failing, writes)
	tally = n2.deliverAll(tenantScoped(), deliveries[:1], &RenderedNotification{}, "a1", model.AttemptTriggerAlarm)
	n2.Ledger.Wait(context.Background())
	if tally.delivered != 1 || fa2.calls != 1 {
		t.Fatalf("tally = %+v, want delivered despite the ledger failing", tally)
	}
	if got := testutil.ToFloat64(writes.WithLabelValues(ledgerFailed)); got != 1 {
		t.Fatalf("ledger failed counter = %v, want 1", got)
	}
}

// The ledger holds at most ledgerWriters store calls at once (the connection pool is
// shared with delivery), and past its queue a row is dropped and counted, never blocked on.
func TestTheLedgerIsBoundedAndDropsRatherThanBlocks(t *testing.T) {
	hung := &memoryLedgerStore{block: make(chan struct{}), entered: make(chan struct{}, 64)}
	writes := ledgerCounter()
	l := NewAttemptLedger(hung, writes)
	for i := 0; i < ledgerWriters; i++ {
		l.record(tenantScoped(), &model.NotificationAttempt{AlarmToken: "a1"})
	}
	for i := 0; i < ledgerWriters; i++ {
		select {
		case <-hung.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d writers reached the store", i, ledgerWriters)
		}
	}
	start := time.Now()
	for i := 0; i < ledgerQueueDepth+3; i++ {
		l.record(tenantScoped(), &model.NotificationAttempt{AlarmToken: "a1"})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("recording behind a hung store took %v; record must never block", elapsed)
	}
	if got := testutil.ToFloat64(writes.WithLabelValues(ledgerDropped)); got != 3 {
		t.Fatalf("dropped = %v, want 3", got)
	}
	// No further call reaches the store while the first ones are stuck.
	select {
	case <-hung.entered:
		t.Fatalf("more than %d ledger writes were in flight at once", ledgerWriters)
	case <-time.After(200 * time.Millisecond):
	}
	close(hung.block)
	l.Wait(context.Background())
	if got := testutil.ToFloat64(writes.WithLabelValues(ledgerWritten)); got != ledgerWriters+ledgerQueueDepth {
		t.Fatalf("written = %v, want %d", got, ledgerWriters+ledgerQueueDepth)
	}
}

// End to end over the real persistence layer: a dispatched alarm leaves its attempts in
// the ledger, readable through the API, with the policy that routed it.
func TestADispatchedAlarmIsInTheLedger(t *testing.T) {
	clock := time.Now()
	n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
	fa.failTimes = 1
	// One connection: each connection to an in-memory sqlite is a separate, empty
	// database, and the ledger writes from its own goroutines.
	sqlDB, err := stateDB(api).DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	n.Ledger = NewAttemptLedger(api, ledgerCounter())
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatal(err)
	}
	n.Ledger.Wait(context.Background())
	found, err := api.NotificationAttempts(ctx, model.NotificationAttemptSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}})
	if err != nil {
		t.Fatal(err)
	}
	// Newest first.
	if got := statuses(found.Results); got != "SENT#2,FAILED#1" {
		t.Fatalf("ledger = %s, want SENT#2,FAILED#1", got)
	}
	r := found.Results[0]
	if r.PolicyToken.String != "p1" || r.Trigger != model.AttemptTriggerAlarm || r.TenantId != "acme" {
		t.Fatalf("row = %+v", r)
	}
	health, err := api.ChannelHealthByToken(ctx, "smtp-1")
	if err != nil {
		t.Fatal(err)
	}
	if !health.LastSuccessAt.Valid || !health.LastFailureAt.Valid || health.ConsecutiveFailures != 0 {
		t.Fatalf("health = %+v, want a success, a failure, and 0 failures since the success", health)
	}
}

// A deleted tenant's late row is refused by the erasure fence, and that is counted as
// purged — the right answer — not as a ledger failure.
func TestAPurgedTenantsLedgerRowIsRefusedNotFailed(t *testing.T) {
	api := fencedApi(t, "acme")
	sqlDB, err := stateDB(api).DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1) // see TestADispatchedAlarmIsInTheLedger
	writes := ledgerCounter()
	l := NewAttemptLedger(api, writes)
	l.record(tenantScoped(), &model.NotificationAttempt{CreatedAt: time.Now(), AlarmToken: "a1",
		Trigger: model.AttemptTriggerAlarm, ChannelToken: "c", ChannelType: "smtp", Status: model.AttemptStatusSent})
	l.Wait(context.Background())
	if got := testutil.ToFloat64(writes.WithLabelValues(ledgerPurged)); got != 1 {
		t.Fatalf("purged = %v failed = %v, want the fence's refusal counted as purged", got,
			testutil.ToFloat64(writes.WithLabelValues(ledgerFailed)))
	}
}

// 🔴 A webhook URL can BE the credential (Slack and Teams put the secret in the path), and
// Go's HTTP client puts the URL in every transport error. The ledger is readable with
// notification:read, so the URL must not reach it.
func TestTheLedgerNeverStoresADeliveryURL(t *testing.T) {
	secretURL := "https://hooks.slack.com/services/T000/B000/SECRETSECRET"
	transport := &url.Error{Op: "Post", URL: secretURL, Err: errors.New("dial tcp 10.0.0.1:443: connect: connection refused")}
	cases := []error{
		transport,
		fmt.Errorf("slack delivery: %w", transport),
		fmt.Errorf("unexpected status 404 from %s", secretURL),
	}
	for _, err := range cases {
		got := sanitizeProviderMessage(err)
		if strings.Contains(got, "SECRETSECRET") || strings.Contains(got, "hooks.slack.com") {
			t.Fatalf("sanitized %q -> %q still carries the URL", err, got)
		}
		if got == "" {
			t.Fatalf("sanitized %q to nothing; the cause must survive", err)
		}
	}
	long := errors.New(strings.Repeat("é", 400))
	if got := sanitizeProviderMessage(long); len(got) > maxLedgerErrorMessage || !strings.HasPrefix(long.Error(), got) {
		t.Fatalf("truncation broke the text: %d bytes", len(got))
	}
}

// The error code comes from the error's IDENTITY, never its text.
func TestAttemptErrorCodes(t *testing.T) {
	blocked := fmt.Errorf("dial: %w", &egress.BlockedError{Reason: "private"})
	cases := []struct {
		status string
		err    error
		want   string
	}{
		{model.AttemptStatusSent, nil, ""},
		{model.AttemptStatusRefused, blocked, model.AttemptErrorEgressBlocked},
		{model.AttemptStatusFailed, fmt.Errorf("x: %w", context.DeadlineExceeded), model.AttemptErrorTimeout},
		{model.AttemptStatusFailed, errors.New("egress blocked, says the text"), model.AttemptErrorDelivery},
		// SMTP's socket deadline: a net.OpError around os.ErrDeadlineExceeded, not the context's.
		{model.AttemptStatusFailed, fmt.Errorf("smtp: %w", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}),
			model.AttemptErrorTimeout},
		{model.AttemptStatusNotAttempted, nil, model.AttemptErrorBudgetSpent},
		{model.AttemptStatusNotAttempted, errors.New("store"), model.AttemptErrorSecretUnavailable},
	}
	for _, c := range cases {
		if got := attemptErrorCode(c.status, c.err); got != c.want {
			t.Fatalf("attemptErrorCode(%s, %v) = %q, want %q", c.status, c.err, got, c.want)
		}
	}
}

// The channel's OWN per-attempt timeout is still the channel's failure: only a cut by the
// whole-dispatch budget is the platform's.
func TestAChannelsOwnTimeoutIsStillATimeout(t *testing.T) {
	store := &memoryLedgerStore{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &stallingAdapter{hardStop: 10 * time.Second}})
	n.attempts = 1
	n.timeout = 50 * time.Millisecond
	n.store = &fakeSecretStore{}
	n.Ledger = NewAttemptLedger(store, nil)
	n.deliverAll(tenantScoped(), []delivery{{channel: enabledChannel("smtp-1", model.ChannelTypeSMTP)}},
		&RenderedNotification{}, "a1", model.AttemptTriggerAlarm)
	n.Ledger.Wait(context.Background())
	if rows := store.snapshot(); len(rows) != 1 || rows[0].ErrorCode.String != model.AttemptErrorTimeout {
		t.Fatalf("ledger = %+v, want one TIMEOUT row", rows)
	}
}

// 🔴 SHUTDOWN WAITS, BUT NOT FOREVER. Behind a hung database every queued row would hold
// Wait for its own write timeout in turn, so a stop could take minutes. Wait is bounded by
// its context (and a ceiling of its own); what it gives up on is counted as dropped.
func TestWaitIsBoundedAndCountsWhatItAbandons(t *testing.T) {
	hung := &memoryLedgerStore{block: make(chan struct{})}
	defer close(hung.block)
	writes := ledgerCounter()
	l := NewAttemptLedger(hung, writes)
	for i := 0; i < 10; i++ {
		l.record(tenantScoped(), &model.NotificationAttempt{AlarmToken: "a1"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	abandoned := l.Wait(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Wait took %v behind a hung store; it must stop at its context", elapsed)
	}
	if abandoned != 10 {
		t.Fatalf("Wait reported %d abandoned rows, want 10", abandoned)
	}
	// The queued rows are discarded rather than written later, so they are counted once.
	if got := testutil.ToFloat64(writes.WithLabelValues(ledgerDropped)); got < 10-ledgerWriters {
		t.Fatalf("dropped = %v, want at least the %d rows still queued", got, 10-ledgerWriters)
	}
}
