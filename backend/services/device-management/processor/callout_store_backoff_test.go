// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// While the broker's MQTT message store is above its high-water mark, every device connect is
// refused at once with the generic denial, before any credential work: the reconnect storm a
// full store causes must not reach the credential store. Once the back-off lifts, connects are
// authorized again.
//
// GUARD: kills a back-off that is not consulted, one consulted after the credential check (the
// storm would still load the database), and one that refuses once it has lifted.
func TestCalloutRefusesEveryConnectWhileTheMessageStoreIsNearlyFull(t *testing.T) {
	var checked atomic.Int32
	g := newCalloutBoundsRig(t, func(ctx context.Context, _ *model.PresentedCredential) (*model.Device, error) {
		checked.Add(1)
		return sensorDevice(), nil
	}, 8, 5*time.Second, 5*time.Second)
	var full atomic.Bool
	full.Store(true)
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "refused"})
	g.r.WithStoreBackoff(full.Load, refused)

	for i := 0; i < 3; i++ {
		isDenial, got := denied(t, g.send(), 2*time.Second)
		if !got || !isDenial {
			t.Fatalf("a connect during the back-off got denial=%v reply=%v, want the generic denial", isDenial, got)
		}
	}
	if n := checked.Load(); n != 0 {
		t.Fatalf("%d connects reached the credential check during the back-off", n)
	}
	if n := testutil.ToFloat64(refused); n != 3 {
		t.Fatalf("refused counter = %v, want 3", n)
	}
	if g.r.RefusedBusy() != 0 {
		t.Fatalf("a back-off refusal was counted as the in-flight bound")
	}

	full.Store(false)
	isDenial, got := denied(t, g.send(), 2*time.Second)
	if !got || isDenial {
		t.Fatalf("a connect after the back-off lifted got denial=%v reply=%v, want a grant", isDenial, got)
	}
	if checked.Load() != 1 {
		t.Fatalf("the connect after the back-off lifted was not authorized")
	}
}
