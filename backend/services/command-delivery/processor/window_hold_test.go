// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
)

func seedHeldRow(t *testing.T, api *model.Api, tenant, token, device string, status model.CommandStatus,
	after time.Time) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	cmd := &model.Command{
		DeviceToken:   device,
		Name:          "reboot",
		Status:        status.String(),
		DispatchAfter: sql.NullTime{Time: after, Valid: true},
		WindowToken:   sql.NullString{String: "plant-a", Valid: true},
	}
	cmd.Token = token
	if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
		t.Fatalf("seed %s: %v", token, err)
	}
}

func rowOf(t *testing.T, api *model.Api, tenant, token string) *model.Command {
	t.Helper()
	got, err := api.CommandsByToken(core.WithTenant(context.Background(), tenant), []string{token})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s: %v (%d rows)", token, err, len(got))
	}
	return got[0]
}

// The real sweep, against real rows: a held command is not published, and the same command
// is published once its hold has lapsed. The fake API the other sweep tests use would
// accept any claim predicate, including none; this one answers from the database.
func TestTheSweepPublishesNothingForAHeldCommandUntilItsWindowOpens(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	seedHeldRow(t, api, "acme", "held", "dev-1", model.CommandQueued, time.Now().Add(time.Hour))

	if err := proc.deliverPendingCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writer.count() != 0 {
		t.Fatalf("the sweep published %d messages for a command waiting on its window", writer.count())
	}
	if got := rowOf(t, api, "acme", "held"); got.Status != model.CommandQueued.String() {
		t.Fatalf("status = %s, want QUEUED while waiting", got.Status)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	if err := api.RDB.DB(ctx).Model(&model.Command{}).Where("token = ?", "held").
		Update("dispatch_after", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := proc.deliverPendingCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writer.count() != 1 {
		t.Fatalf("the sweep published %d messages once the window opened, want 1", writer.count())
	}
	if got := rowOf(t, api, "acme", "held"); got.Status != model.CommandSent.String() {
		t.Fatalf("status = %s, want SENT after the window opened", got.Status)
	}
}

type fakeOpenings struct {
	calls   []string
	answers map[string]model.WindowOpening
	err     error
}

func (f *fakeOpenings) WindowOpening(_ context.Context, token string, _ time.Time) (model.WindowOpening, error) {
	f.calls = append(f.calls, token)
	if f.err != nil {
		return model.WindowOpening{}, f.err
	}
	return f.answers[token], nil
}

func TestTheRestampPassMovesWaitingRowsOfEveryTenantUnderItsOwnTenant(t *testing.T) {
	api := realApi(t)
	next := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	for _, tenant := range []string{"acme", "globex"} {
		seedHeldRow(t, api, tenant, "w-"+tenant, "dev-1", model.CommandHeld, time.Now().Add(time.Hour))
	}
	opens := &fakeOpenings{answers: map[string]model.WindowOpening{
		"plant-a": {Found: true, DispatchAfter: next}}}
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: opens}

	if err := proc.restampWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme", "globex"} {
		if got := rowOf(t, api, tenant, "w-"+tenant); !got.DispatchAfter.Time.Equal(next) {
			t.Fatalf("%s: stamp = %v, want %v", tenant, got.DispatchAfter.Time, next)
		}
	}
	if len(opens.calls) != 2 {
		t.Fatalf("the window was read %d times, want once per (tenant, window)", len(opens.calls))
	}
}

func TestTheRestampPassIsThrottled(t *testing.T) {
	api := realApi(t)
	seedHeldRow(t, api, "acme", "w", "dev-1", model.CommandHeld, time.Now().Add(time.Hour))
	opens := &fakeOpenings{answers: map[string]model.WindowOpening{
		"plant-a": {Found: true, DispatchAfter: time.Now().Add(48 * time.Hour)}}}
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: opens}
	for i := 0; i < 3; i++ {
		if err := proc.restampWindows(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(opens.calls) != 1 {
		t.Fatalf("the owner was asked %d times in three back-to-back passes, want 1", len(opens.calls))
	}
	proc.lastWindowRestamp = time.Now().Add(-2 * proc.windowRestampInterval())
	if err := proc.restampWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opens.calls) != 2 {
		t.Fatalf("the pass did not run again after its interval (%d reads)", len(opens.calls))
	}
}

// When the owner cannot be reached the rows stay exactly as stamped. Reading an outage as
// "the window is gone" would release every waiting disruptive command at once.
func TestTheRestampPassLeavesRowsHeldWhenTheOwnerIsUnreachable(t *testing.T) {
	api := realApi(t)
	hold := time.Now().Add(time.Hour).Truncate(time.Second)
	seedHeldRow(t, api, "acme", "w", "dev-1", model.CommandQueued, hold)
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: &fakeOpenings{err: errors.New("down")}}

	err := proc.restampWindows(context.Background())
	if !errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("err = %v, want a partial pass", err)
	}
	got := rowOf(t, api, "acme", "w")
	if !got.DispatchAfter.Valid || !got.DispatchAfter.Time.Equal(hold) || !got.WindowToken.Valid {
		t.Fatalf("an outage changed the hold: %v / %v", got.DispatchAfter, got.WindowToken)
	}
}

func TestTheRestampPassSkipsADeletedTenant(t *testing.T) {
	api := realApi(t)
	seedHeldRow(t, api, "gone", "w", "dev-1", model.CommandQueued, time.Now().Add(time.Hour))
	opens := &fakeOpenings{}
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: opens,
		TenantDeleted: func(tenant string) bool { return tenant == "gone" }}
	if err := proc.restampWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opens.calls) != 0 {
		t.Fatal("the pass reached into a deleted tenant")
	}
}

func TestTheRestampPassIsOffWithoutAResolver(t *testing.T) {
	api := realApi(t)
	seedHeldRow(t, api, "acme", "w", "dev-1", model.CommandQueued, time.Now().Add(time.Hour))
	proc := &CommandDeliveryProcessor{Api: api}
	if err := proc.restampWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// restampingApi is the fake API plus the two methods the pass uses, counting its calls.
type restampingApi struct {
	*fakeApi
	listed int
}

func (r *restampingApi) WaitingWindows(context.Context, int) ([]model.WaitingWindow, error) {
	r.listed++
	return nil, nil
}

func (r *restampingApi) RestampWindow(context.Context, string, model.WindowOpening, time.Time) (int64, error) {
	return 0, nil
}

// The pass is only worth having if the sweep actually runs it, and nothing else in this
// package would notice it being unplugged.
func TestTheSweepRunsTheRestampPassUnderItsLock(t *testing.T) {
	api := &restampingApi{fakeApi: &fakeApi{lockAvailable: true}}
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: &recordingWriter{},
		WindowOpenings: &fakeOpenings{}}
	if err := proc.sweepLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.listed != 1 {
		t.Fatalf("the sweep listed waiting windows %d times, want 1", api.listed)
	}

	api2 := &restampingApi{fakeApi: &fakeApi{lockAvailable: false}}
	proc2 := &CommandDeliveryProcessor{Api: api2, DeviceCommandsWriter: &recordingWriter{},
		WindowOpenings: &fakeOpenings{}}
	_ = proc2.sweepLocked(context.Background())
	if api2.listed != 0 {
		t.Fatal("a replica without the sweep lock ran the re-stamp pass")
	}
}
