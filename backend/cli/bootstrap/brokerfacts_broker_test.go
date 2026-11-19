// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// `dcctl broker facts` against a broker that ENFORCES the generated permissions, reading as the
// broker administrator. A denied read is a silent drop that surfaces as a timeout, so only a real
// broker shows the grant is there; and only a real broker shows the login still cannot remove what
// it reads.

// seedFacts publishes deletion facts as device-management's outbox does: on the tenant's subject,
// under the outbox's message id.
func seedFacts(t *testing.T, b *permtest.Broker, instance string, facts ...entityevent.EntityDeletedEvent) {
	t.Helper()
	js, err := b.Connect(t, "device-management").JetStream()
	require.NoError(t, err)
	for i, f := range facts {
		tenant := "acme"
		if i%2 == 1 {
			tenant = "globex"
		}
		body, err := entityevent.MarshalEntityDeletedEvent(&f)
		require.NoError(t, err)
		m := nats.NewMsg(messaging.ScopedSubject(instance, tenant, streams.EntityDeleted))
		m.Data = body
		m.Header.Set(nats.MsgIdHdr, fmt.Sprintf("entdel/%s/%s/%d", tenant, f.EntityType, f.EntityId))
		m.Header.Set("X-Other", "not printed")
		_, err = js.PublishMsg(m)
		require.NoError(t, err)
	}
}

func adminConn(t *testing.T, b *permtest.Broker) *nats.Conn {
	t.Helper()
	nc, err := dialBrokerCtx(context.Background(), fmt.Sprintf("nats://%s:%d", b.Host, b.Port), administratorView(b))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func threeFacts() []entityevent.EntityDeletedEvent {
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	return []entityevent.EntityDeletedEvent{
		{EntityType: entity.TypeDevice, EntityId: 7, EntityToken: "sensor-7", DeletedTime: at},
		{EntityType: entity.TypeDevice, EntityId: 8, EntityToken: "sensor-8", DeletedTime: at.Add(time.Second)},
		{EntityType: entity.TypeAsset, EntityId: 9, EntityToken: "pump-9", DeletedTime: at.Add(2 * time.Second)},
	}
}

func TestBrokerFactsReadsTheDeletionFactsAsTheBrokerAdministrator(t *testing.T) {
	const instance = "factsread"
	b := permtest.Start(t, instance)
	seedFacts(t, b, instance, threeFacts()...)
	nc := adminConn(t, b)
	ctx := context.Background()

	facts, info, err := readBrokerFacts(ctx, nc, instance, factFilter{})
	require.NoError(t, err, "the administrator cannot read the deletion facts")
	require.EqualValues(t, 3, info.State.Msgs)
	require.Len(t, facts, 3)
	require.Equal(t, DeletionFact{Seq: 1, Stored: facts[0].Stored, Tenant: "acme", MsgID: "entdel/acme/device/7",
		EntityType: "device", EntityID: 7, EntityToken: "sensor-7", Deleted: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}, facts[0])
	require.Equal(t, "globex", facts[1].Tenant)
	require.False(t, facts[0].Stored.IsZero(), "the stored time was not read")

	// --tenant reads that tenant's subject only.
	facts, _, err = readBrokerFacts(ctx, nc, instance, factFilter{tenant: "acme"})
	require.NoError(t, err)
	require.Len(t, facts, 2)
	for _, f := range facts {
		require.Equal(t, "acme", f.Tenant)
	}
	// --token.
	facts, _, err = readBrokerFacts(ctx, nc, instance, factFilter{token: "pump-9"})
	require.NoError(t, err)
	require.Len(t, facts, 1)
	require.Equal(t, "asset", facts[0].EntityType)
	// --since: a cutoff after every stored time selects nothing; one before selects all.
	facts, _, err = readBrokerFacts(ctx, nc, instance, factFilter{since: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	require.Empty(t, facts)
	facts, _, err = readBrokerFacts(ctx, nc, instance, factFilter{since: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	require.Len(t, facts, 3)

	var out bytes.Buffer
	printBrokerFacts(&out, info, facts)
	require.Contains(t, out.String(), "entdel/globex/device/8")
	require.Contains(t, out.String(), "3 deletion fact(s)")
	require.NotContains(t, out.String(), "not printed", "a header other than the message id was printed")
	b.RequireNoViolations(t)
}

// 🔴 THE GRANT IS A READ, AND A READ ONLY. The same login that reads a fact must not be able to
// remove it, put one there, or open a consumer on the stream: each is refused by the broker (a
// logged permission violation) and the stream still holds every fact afterwards.
func TestTheBrokerAdministratorCannotRemoveOrForgeADeletionFact(t *testing.T) {
	const instance = "factsro"
	b := permtest.Start(t, instance)
	seedFacts(t, b, instance, threeFacts()...)
	nc := adminConn(t, b)
	name := messaging.StreamName(instance, streams.EntityDeleted)
	js, err := nc.JetStream(nats.MaxWait(time.Second))
	require.NoError(t, err)

	require.Error(t, js.DeleteMsg(name, 1), "the administrator deleted a deletion fact")
	require.Error(t, js.SecureDeleteMsg(name, 2), "the administrator erased a deletion fact")
	require.Error(t, js.PurgeStream(name), "the administrator purged the deletion facts")
	_, err = js.AddConsumer(name, &nats.ConsumerConfig{Durable: "operator", AckPolicy: nats.AckExplicitPolicy})
	require.Error(t, err, "the administrator opened a consumer on the deletion facts")
	_, err = js.Publish(messaging.ScopedSubject(instance, "acme", streams.EntityDeleted), []byte("forged"))
	require.Error(t, err, "the administrator published a deletion fact")
	require.NotEmpty(t, b.Violations(), "the broker refused nothing, so the refusals above were not the grant's doing")

	facts, info, err := readBrokerFacts(context.Background(), nc, instance, factFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 3, info.State.Msgs, "the stream lost or gained a message")
	require.Len(t, facts, 3)
}

// The negative control: without the read grant the same call is a denied read, named as such, and
// the broker logs the violation, so the passing test above is the grant's doing.
func TestBrokerFactsNamesADeniedReadNotATimeout(t *testing.T) {
	const instance = "factsno"
	name := messaging.StreamName(instance, streams.EntityDeleted)
	b := permtest.Start(t, instance, permtest.Without("$JS.API.STREAM.MSG.GET."+name))
	seedFacts(t, b, instance, threeFacts()...)

	prev := factReadTimeout
	factReadTimeout = time.Second
	t.Cleanup(func() { factReadTimeout = prev })

	_, _, err := readBrokerFacts(context.Background(), adminConn(t, b), instance, factFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "denied")
	require.Contains(t, err.Error(), "STREAM.MSG.GET."+name)
	require.NotEmpty(t, b.Violations(), "the broker did not refuse the read, so it was not what failed")
}

func TestTenantOfFactSubject(t *testing.T) {
	require.Equal(t, "acme", tenantOfFactSubject("inst", "inst.acme.entity-deleted"))
	for _, s := range []string{"other.acme.entity-deleted", "inst.acme.other", "inst..entity-deleted", "inst.acme.entity-deleted.x"} {
		require.Equal(t, "?", tenantOfFactSubject("inst", s), s)
	}
	require.True(t, strings.HasPrefix(messaging.ScopedSubject("inst", "acme", streams.EntityDeleted), "inst.acme."))
}

// fakeFactStream answers every read with the next stored message after seq, from a list that may
// have grown since its bounds were read.
type fakeFactStream struct {
	info *jetstream.StreamInfo
	msgs []*jetstream.RawStreamMsg
}

func (f *fakeFactStream) CachedInfo() *jetstream.StreamInfo { return f.info }

func (f *fakeFactStream) GetMsg(_ context.Context, seq uint64, _ ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error) {
	for _, m := range f.msgs {
		if m.Sequence >= seq {
			return m, nil
		}
	}
	return nil, jetstream.ErrMsgNotFound
}

// A fact appended while the walk runs is past the bounds the summary line reports, so it is not
// listed under them.
func TestBrokerFactsStopsAtTheBoundsItReports(t *testing.T) {
	body, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{
		EntityType: entity.TypeDevice, EntityId: 1, EntityToken: "d", DeletedTime: time.Now()})
	require.NoError(t, err)
	msg := func(seq uint64) *jetstream.RawStreamMsg {
		return &jetstream.RawStreamMsg{Subject: "inst.acme.entity-deleted", Sequence: seq, Data: body, Time: time.Now()}
	}
	s := &fakeFactStream{
		info: &jetstream.StreamInfo{State: jetstream.StreamState{Msgs: 2, FirstSeq: 1, LastSeq: 2}},
		msgs: []*jetstream.RawStreamMsg{msg(1), msg(2), msg(3)},
	}
	facts, err := collectFacts(context.Background(), s, "inst", factFilter{})
	require.NoError(t, err)
	require.Len(t, facts, 2)
	require.EqualValues(t, 2, facts[1].Seq)
}
