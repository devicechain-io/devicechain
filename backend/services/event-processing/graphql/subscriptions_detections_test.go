// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

func TestDetectionStreamSharedFeed(t *testing.T) {
	nmgr := &messaging.NatsManager{}
	upstream := make(chan messaging.Message, 32)
	var opens, decodes atomic.Int64
	var upstreamCtx context.Context
	oldOpen, oldDecode := detectionLiveOpener, detectionDecode
	detectionLiveOpener = func(*messaging.NatsManager) messaging.LiveOpener {
		return func(ctx context.Context, tenant string) (<-chan messaging.Message, error) {
			opens.Add(1)
			upstreamCtx = ctx
			require.Equal(t, "acme", tenant)
			return upstream, nil
		}
	}
	detectionDecode = func(msg messaging.Message, tenant string) []messaging.Keyed[liveDetection] {
		decodes.Add(1)
		return decodeDetections(msg, tenant)
	}
	defer func() { detectionLiveOpener, detectionDecode = oldOpen, oldDecode }()
	ctx, cancel := context.WithCancel(core.WithTenant(authedContext(auth.DeviceRead), "acme"))
	defer cancel()
	ctx = context.WithValue(ctx, gqlcore.ContextNatsKey, nmgr)
	chans := make([]<-chan *DetectionEventResolver, 20)
	for i := range chans {
		ch, err := (&SchemaResolver{}).DetectionStream(ctx, struct{ ProfileToken string }{fmt.Sprintf("profile-%d", i%4)})
		require.NoError(t, err)
		chans[i] = ch
	}
	require.Equal(t, int64(1), opens.Load())
	for i := 0; i < 24; i++ {
		data, err := json.Marshal(runtime.DerivedEvent{RuleID: fmt.Sprintf("acme/profile-%d@v1/rule-%d", i%4, i), Series: fmt.Sprint(i)})
		require.NoError(t, err)
		upstream <- messaging.Message{Value: data}
	}
	// Neither malformed JSON nor an unparseable rule id reaches any subscriber.
	upstream <- messaging.Message{Value: []byte("{")}
	upstream <- messaging.Message{Value: []byte(`{"ruleId":"invalid"}`)}
	close(upstream)
	for i, ch := range chans {
		for j := i % 4; j < 24; j += 4 {
			select {
			case got, ok := <-ch:
				require.True(t, ok)
				require.Equal(t, fmt.Sprintf("acme/profile-%d@v1/rule-%d", i%4, j), got.RuleId())
				require.Equal(t, fmt.Sprintf("rule-%d", j), got.RuleToken())
				require.Equal(t, fmt.Sprint(j), got.Series())
			case <-time.After(5 * time.Second):
				t.Fatal("missing detection")
			}
		}
		select {
		case _, ok := <-ch:
			require.False(t, ok, "unexpected detection after expected profile matches")
		case <-time.After(5 * time.Second):
			t.Fatal("resolver did not close")
		}
	}
	require.Equal(t, int64(26), decodes.Load())
	select {
	case <-upstreamCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shared upstream did not cancel")
	}
}
