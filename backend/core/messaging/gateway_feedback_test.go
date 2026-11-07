// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func feedbackRig(t *testing.T) (*GatewayFeedback, *nats.Conn) {
	t.Helper()
	url := evictRig(t)
	nc := evictConn(t, url)
	f := NewGatewayFeedback(NewGatewayNoticePublisher(func() *nats.Conn { return nc }, "inst"))
	return f, nc
}

func collectAccepts(t *testing.T, nc *nats.Conn, subject string) (func() []string, func() []GatewayNotice) {
	t.Helper()
	var mu sync.Mutex
	var ids []string
	var notices []GatewayNotice
	_, err := nc.Subscribe(subject, func(m *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		var a GatewayAccept
		if json.Unmarshal(m.Data, &a) == nil && a.AltIds != nil {
			ids = append(ids, a.AltIds...)
			return
		}
		var n GatewayNotice
		if json.Unmarshal(m.Data, &n) == nil {
			notices = append(notices, n)
		}
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), ids...) },
		func() []GatewayNotice { mu.Lock(); defer mu.Unlock(); return append([]GatewayNotice(nil), notices...) }
}

func TestAcceptancesAreBatchedPerGatewayAndReachOnlyThatGateway(t *testing.T) {
	f, nc := feedbackRig(t)
	gw1, _ := collectAccepts(t, nc, GatewayAcceptsSubject("inst", "acme", "gw-1"))
	gw2, _ := collectAccepts(t, nc, GatewayAcceptsSubject("inst", "acme", "gw-2"))

	for i := 0; i < 10; i++ {
		f.Accepted("acme", "gw-1", fmt.Sprintf("k%d", i))
	}
	f.Accepted("acme", "gw-2", "other")
	f.Accepted("acme", "gw-1", "") // no key: nothing to say
	f.Flush()
	require.NoError(t, nc.Flush())

	require.Eventually(t, func() bool { return len(gw1()) == 10 && len(gw2()) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{"other"}, gw2())
	require.Equal(t, int64(11), f.Sent())
}

func TestAFullBatchIsSentWithoutWaitingForTheClock(t *testing.T) {
	f, nc := feedbackRig(t)
	got, _ := collectAccepts(t, nc, GatewayAcceptsSubject("inst", "acme", "gw-1"))
	for i := 0; i < gatewayAcceptBatchMax; i++ {
		f.Accepted("acme", "gw-1", fmt.Sprintf("k%d", i))
	}
	require.Eventually(t, func() bool { return len(got()) == gatewayAcceptBatchMax }, 2*time.Second, 5*time.Millisecond)
}

func TestHeldAcceptancesAreBoundedNotGrownWithoutLimit(t *testing.T) {
	f, _ := feedbackRig(t)
	f.pendingMax = 5
	f.batchMax = 1000
	for i := 0; i < 20; i++ {
		f.Accepted("acme", "gw-1", fmt.Sprintf("k%d", i))
	}
	require.Equal(t, int64(15), f.Dropped(), "ids over the bound are counted, not held")
}

func TestRefusalsGoOnTheNoticesSubjectAndAcceptancesNeverDo(t *testing.T) {
	f, nc := feedbackRig(t)
	ids, _ := collectAccepts(t, nc, GatewayAcceptsSubject("inst", "acme", "gw-1"))
	_, notices := collectAccepts(t, nc, GatewayNoticesSubject("inst", "acme", "gw-1"))
	f.Refused("acme", "gw-1", GatewayNotice{AltId: "k1", Device: "d", Reason: GatewayNoticeReasonOverCeiling})
	f.Accepted("acme", "gw-1", "k2")
	f.Flush()
	require.NoError(t, nc.Flush())
	require.Eventually(t, func() bool { return len(notices()) == 1 && len(ids()) == 1 }, 2*time.Second, 5*time.Millisecond)
	// A gateway agent that predates acceptances reads the notices subject as refusals only: an
	// acceptance there would be counted as one.
	require.Equal(t, "k1", notices()[0].AltId)
	require.Equal(t, []string{"k2"}, ids())
}

func TestNilFeedbackIsInert(t *testing.T) {
	var f *GatewayFeedback
	f.Accepted("acme", "gw-1", "k")
	f.Refused("acme", "gw-1", GatewayNotice{})
	f.Flush()
}
