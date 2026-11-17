// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

func TestAlarmStreamSharedFeed(t *testing.T) {
	f := newScopeFixture(t)
	nmgr := &messaging.NatsManager{}
	upstream := make(chan messaging.Message, 32)
	var opens, decodes atomic.Int64
	var upstreamCtx context.Context
	oldOpen, oldDecode := alarmLiveOpener, alarmDecode
	alarmLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
			opens.Add(1)
			upstreamCtx = ctx
			require.Equal(t, scopeTenant, tenant)
			return upstream, nil
		}
	}
	alarmDecode = func(msg messaging.Message, tenant string) []messaging.Keyed[*model.AlarmStateChangeEvent] {
		decodes.Add(1)
		return decodeAlarms(msg, tenant)
	}
	defer func() { alarmLiveOpener, alarmDecode = oldOpen, oldDecode }()
	acmeID, err := f.api.ResolveEntityToken(f.operator(), "device", "acme-pump")
	require.NoError(t, err)
	globexID, err := f.api.ResolveEntityToken(f.operator(), "device", "globex-pump")
	require.NoError(t, err)

	type subscriber struct {
		ch     <-chan *AlarmEventResolver
		filter alarmEventFilter
	}
	subs := make([]subscriber, 20)
	for i := range subs {
		ctx := f.operator()
		args := alarmStreamArgs{}
		switch i % 5 {
		case 1:
			args.State = strp("ACTIVE")
		case 2:
			args.Severity, args.AlarmKey = strp("CRITICAL"), strp("over-temp")
		case 3:
			ctx = f.member("customer:acme")
		case 4:
			args.OriginatorType, args.OriginatorToken = strp("device"), strp("acme-pump")
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
		ch, err := (&SchemaResolver{}).AlarmStream(ctx, args)
		require.NoError(t, err)
		filter := alarmEventFilter{state: args.State, severity: args.Severity, alarmKey: args.AlarmKey, originatorType: args.OriginatorType}
		if i%5 == 3 {
			var scoped bool
			filter.scope, scoped = auth.ScopeFromContext(ctx)
			require.True(t, scoped)
		}
		if args.OriginatorToken != nil {
			filter.originatorId = &acmeID
		}
		subs[i] = subscriber{ch: ch, filter: filter}
	}
	require.Equal(t, int64(1), opens.Load())
	var events []*model.AlarmStateChangeEvent
	for i := 0; i < 12; i++ {
		ev := &model.AlarmStateChangeEvent{AlarmToken: fmt.Sprint(i), OriginatorType: "device", OriginatorId: acmeID,
			State: "ACTIVE", Severity: "CRITICAL", AlarmKey: "over-temp", ScopeAnchors: []string{"customer:acme"}}
		if i%2 != 0 {
			ev.OriginatorId, ev.ScopeAnchors = globexID, []string{"customer:globex"}
		}
		if i%3 == 0 {
			ev.State = "CLEARED"
		}
		if i%4 == 0 {
			ev.Severity = "MAJOR"
		}
		if i%5 == 0 {
			ev.AlarmKey = "low-batt"
		}
		if i == 10 {
			ev.ScopeAnchors = nil
		}
		events = append(events, ev)
		data, err := dmproto.MarshalAlarmStateChangeEvent(ev)
		require.NoError(t, err)
		upstream <- messaging.Message{Value: data}
	}
	upstream <- messaging.Message{Value: []byte{0xff}}
	close(upstream)
	for _, sub := range subs {
		for _, ev := range events {
			if !sub.filter.matches(ev) {
				continue
			}
			select {
			case got, ok := <-sub.ch:
				require.True(t, ok)
				require.Equal(t, ev.AlarmToken, got.AlarmToken())
			case <-time.After(5 * time.Second):
				t.Fatal("missing alarm")
			}
		}
		select {
		case _, ok := <-sub.ch:
			require.False(t, ok, "unexpected alarm after expected matches")
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not close")
		}
	}
	require.Equal(t, int64(len(events)+1), decodes.Load())
	select {
	case <-upstreamCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shared upstream did not cancel")
	}
}
