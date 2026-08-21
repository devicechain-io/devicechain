// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// captureFactWriter records what the publisher handed the broker, or refuses everything when
// refuse is set.
type captureFactWriter struct {
	payloads [][]byte
	refuse   error
}

func (w *captureFactWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	if w.refuse != nil {
		return w.refuse
	}
	for _, m := range msgs {
		w.payloads = append(w.payloads, m.Value)
	}
	return nil
}

func (w *captureFactWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *captureFactWriter) HandleResponse(error) {}

// factOfSize builds a fence-set fact whose marshalled form is comfortably larger than n bytes,
// by padding one fence's geometry. It is a shape test, not an authoring test — the point is the
// publisher's arithmetic on a size, so the geometry need only BE that size.
func factOfSize(version int32, n int) *model.GeoFenceSetMintedEvent {
	return &model.GeoFenceSetMintedEvent{
		Version:  version,
		MintedAt: time.Unix(0, 0).UTC(),
		Fences: []model.GeoFenceSnapshotRef{{
			Token:    "yard",
			Geometry: []byte(`"` + strings.Repeat("x", n) + `"`),
		}},
	}
}

// A configured broker ceiling smaller than the fact headroom fails CLOSED — pointers — not open.
//
// 🔴 THE OPEN DIRECTION WAS REACHABLE FROM values.yaml. ApplyDefaults coerces only a
// non-positive streamMaxMsgSize and values.schema.json sets no minimum, so `streamMaxMsgSize:
// 2048` is an accepted override. Subtracting factHeadroom from it and reading the result as "no
// ceiling was stated" published every oversized fact straight at a broker that refuses it,
// silently restoring the defect this writer exists to remove. "Nothing was stated" and "what was
// stated is tiny" must not share an encoding.
//
// The table walks ACROSS the boundary rather than sampling one side of it, so the switch is
// pinned where it is rather than merely observed to exist somewhere.
func TestBrokerCeilingBelowTheHeadroomFailsClosed(t *testing.T) {
	const factBytes = 20000 // well over every tiny ceiling, well under the platform default

	for _, tc := range []struct {
		name    string
		ceiling int32
		pointer bool
	}{
		{"unstated, so unchecked", 0, false},
		{"negative, so unchecked", -1, false},
		{"absurdly small", 1024, true},
		{"one below the headroom", factHeadroom - 1, true},
		{"exactly the headroom", factHeadroom, true},
		{"one over the headroom", factHeadroom + 1, true},
		{"just under the fact", factBytes, true},
		{"the platform default", config.DefaultStreamMaxMsgSize, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &captureFactWriter{}
			NewGeoFenceSetWriter(w, tc.ceiling, nil).
				PublishGeoFenceSet(context.Background(), factOfSize(7, factBytes))

			if len(w.payloads) != 1 {
				t.Fatalf("published %d facts, want 1 — a minted version must always be announced",
					len(w.payloads))
			}
			ev, err := model.UnmarshalGeoFenceSetMintedEvent(w.payloads[0])
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if ev.FencesOmitted != tc.pointer {
				t.Fatalf("ceiling %d published fencesOmitted=%v at %d bytes, want %v",
					tc.ceiling, ev.FencesOmitted, len(w.payloads[0]), tc.pointer)
			}
			if ev.Version != 7 {
				t.Errorf("the published fact names version %d, want 7", ev.Version)
			}
			// Whichever form it took, a checked ceiling must never emit something over it.
			if tc.ceiling > 0 && tc.pointer && len(w.payloads[0]) > factBytes {
				t.Errorf("the pointer fact is %d bytes, no smaller than the set it replaced",
					len(w.payloads[0]))
			}
		})
	}
}

// An ordinary fact under the ceiling is published whole, with its fences. The counterweight to
// the table above: a publisher that answered "pointer" to everything would pass every row there
// and be useless.
func TestAFactUnderTheCeilingKeepsItsFences(t *testing.T) {
	w := &captureFactWriter{}
	omitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_under_ceiling_total"})
	NewGeoFenceSetWriter(w, config.DefaultStreamMaxMsgSize, omitted).
		PublishGeoFenceSet(context.Background(), factOfSize(7, 200))

	ev, err := model.UnmarshalGeoFenceSetMintedEvent(w.payloads[0])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.FencesOmitted {
		t.Fatal("a small fence set was published as a pointer; every fence edit would now cost " +
			"a cross-service read")
	}
	if len(ev.Fences) != 1 {
		t.Errorf("the fact carries %d fences, want 1", len(ev.Fences))
	}
	if got := testutil.ToFloat64(omitted); got != 0 {
		t.Errorf("an ordinary fact was counted as fences-omitted %v times, want 0", got)
	}
}

// A pointer fact whose publish then FAILS is not counted as published.
//
// The counter's help string says these facts were PUBLISHED, and an increment taken ahead of the
// write would make that sentence false in exactly the situation an operator is reading the number
// to understand — a broker that is not accepting.
func TestAPointerFactIsCountedOnlyOnceItIsActuallyPublished(t *testing.T) {
	omitted := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_pointer_publish_total"})
	w := &captureFactWriter{refuse: errors.New("broker refused")}
	NewGeoFenceSetWriter(w, 1024, omitted).
		PublishGeoFenceSet(context.Background(), factOfSize(7, 20000))

	if got := testutil.ToFloat64(omitted); got != 0 {
		t.Errorf("a pointer fact whose publish failed was counted %v times, want 0", got)
	}

	// The control: the same call against a broker that accepts DOES count. Without it, a
	// publisher that never counted anything would pass the assertion above.
	ok := &captureFactWriter{}
	NewGeoFenceSetWriter(ok, 1024, omitted).
		PublishGeoFenceSet(context.Background(), factOfSize(7, 20000))
	if got := testutil.ToFloat64(omitted); got != 1 {
		t.Errorf("a pointer fact that WAS published was counted %v times, want 1", got)
	}
}
