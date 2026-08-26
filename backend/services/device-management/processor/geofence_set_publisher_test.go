// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	dcconfig "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
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

func failureCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Name: "geofence_set_publish_failures_total", Help: "test"})
}

// manifestOf builds a manifest naming n fences with distinct tokens and addresses.
func manifestOf(version int32, n int) *model.GeoFenceSetManifest {
	fences := make([]model.GeoFenceManifestEntry, 0, n)
	for i := 0; i < n; i++ {
		fences = append(fences, model.GeoFenceManifestEntry{
			Token: "fence-" + strings.Repeat("x", i%8) + string(rune('a'+i%26)),
			Hash:  model.GeoFenceGeometryHash([]byte(strings.Repeat("g", i+1))),
		})
	}
	return &model.GeoFenceSetManifest{Version: version, Fences: fences, MintedAt: time.Now().UTC()}
}

// 🔴 THE ARITHMETIC THAT DELETED THE BROKER-CEILING MACHINERY, CHECKED RATHER THAN QUOTED.
//
// The pointer fact, the headroom subtraction and the live max_payload clamp all existed
// because a fence-set fact carrying geometry could exceed one broker message. A manifest
// cannot, and this is the only thing that says so. It is deliberately expressed against the
// DEFAULT per-message ceiling and against the fence cap as it stands, so that raising either
// re-derives the claim: if a future cap made a manifest approach the wall, this fails here
// rather than in production as a swallowed publish.
//
// The margin is asserted as a BAND, not a ceiling. "Smaller than 1 MiB" would still pass if a
// manifest quietly grew a hundredfold, and a bound that only fails in one direction reads as
// safe while saying almost nothing — so the lower edge asserts the worst case is genuinely
// worst, i.e. that this is measuring a full manifest rather than an accidentally empty one.
//
// 🔴 THE UPPER EDGE MOVED WHEN THE FENCE CAP BECAME A TIER SETTING, AND THE OLD ONE WOULD HAVE
// BEEN THE WRONG GATE TO KEEP. It demanded a worst case under an EIGHTH of the ceiling, which
// was the ~48x margin a hard-coded 100 fences bought. The number a manifest must now be built
// at is governance.MaxGeoFenceCeiling — what the instance must survive, not what an
// unconfigured tenant gets — and that constant was chosen deliberately as 82% of the default
// ceiling rather than at break-even, precisely so the publisher's startup warning stays silent
// on a default deployment. Re-asserting an eighth would have failed a correct rewiring and
// invited someone to "fix" it by looping the default again.
func TestManifestFitsOneBrokerMessage(t *testing.T) {
	// The broker's default per-message ceiling. The DEPLOYED value is configuration
	// (infrastructure.nats.streamMaxMsgSize) and a deployment that lowers it is what the
	// publisher's startup warning and its failure counter exist for — but the DEFAULT is a Go
	// constant, and this now reads it rather than restating it.
	//
	// 🔴 IT WAS A LITERAL 1<<20 UNDER A COMMENT SAYING "deployment configuration rather than a
	// Go constant", which is the shape that lets a number drift: a false sentence justifying a
	// copy. dcconfig.DefaultStreamMaxMsgSize is where it lives.
	defaultCeiling := int(dcconfig.DefaultStreamMaxMsgSize)

	// The floor is DERIVED, not chosen. One entry cannot encode to less than its token and its
	// hash, so a full manifest cannot encode to less than this — which is what makes the floor
	// an assertion that the worst case is genuinely being BUILT rather than a number that
	// happened to be below the answer. An earlier version of this test used a literal 20000 and
	// a comment claiming it was computed from the constants; it was not.
	minimumEntry := core.MaxTokenLen + 64
	floor := governance.MaxGeoFenceCeiling * minimumEntry

	// The headroom MaxGeoFenceCeiling was chosen to leave: room for one more per-entry field
	// of ~46 bytes before a default deployment starts warning. Asserted as a share rather than
	// as a byte count, so it survives a token-length change.
	const wantHeadroom = 0.15

	worst := model.MaxGeoFenceSetManifestBytes()
	if worst >= defaultCeiling {
		t.Fatalf("a worst-case manifest is %d bytes, OVER the %d-byte per-message ceiling; every "+
			"fence edit by a tenant at the platform maximum fence count would be refused by the "+
			"broker, which is the failure the pointer fact used to absorb", worst, defaultCeiling)
	}
	if headroom := 1 - float64(worst)/float64(defaultCeiling); headroom < wantHeadroom {
		t.Fatalf("a worst-case manifest is %d bytes, leaving only %.1f%% of the %d-byte ceiling; "+
			"governance.MaxGeoFenceCeiling was set below break-even to keep at least %.0f%% so the "+
			"publisher's startup warning stays silent on a default deployment",
			worst, headroom*100, defaultCeiling, wantHeadroom*100)
	}
	if worst < floor {
		t.Fatalf("a worst-case manifest measured only %d bytes, which is below the %d that "+
			"%d entries of a %d-character token and a 64-character hash must encode to at minimum; "+
			"the worst case is not being built (looping the DEFAULT fence count instead of the "+
			"platform maximum is how that happens, and it understates by 40x)",
			worst, floor, governance.MaxGeoFenceCeiling, core.MaxTokenLen)
	}
	t.Logf("worst-case manifest at the platform maximum of %d fences: %d bytes, %.1f%% of the "+
		"%d-byte default ceiling", governance.MaxGeoFenceCeiling, worst,
		100*float64(worst)/float64(defaultCeiling), defaultCeiling)

	// The value is stable across calls. It is rebuilt and re-measured on every call rather
	// than cached, so a non-deterministic worst case — a map iteration leaking into the
	// encoding, say — would make the bound above mean nothing on the run that mattered.
	if again := model.MaxGeoFenceSetManifestBytes(); again != worst {
		t.Fatalf("the worst-case manifest measured %d then %d; the bound is not deterministic",
			worst, again)
	}
}

// A manifest goes on the wire whole, and what arrives decodes back to what was published.
func TestManifestIsPublishedVerbatim(t *testing.T) {
	writer := &captureFactWriter{}
	failures := failureCounter()
	pub := NewGeoFenceSetWriter(writer, 1<<20, failures)

	manifest := manifestOf(7, 3)
	pub.PublishGeoFenceSetManifest(context.Background(), manifest)

	if len(writer.payloads) != 1 {
		t.Fatalf("expected one published message, got %d", len(writer.payloads))
	}
	decoded, err := model.UnmarshalGeoFenceSetManifest(writer.payloads[0])
	if err != nil {
		t.Fatalf("decode published manifest: %v", err)
	}
	if decoded.Version != 7 || len(decoded.Fences) != 3 {
		t.Fatalf("published manifest decoded as version %d with %d fences; want 7 and 3",
			decoded.Version, len(decoded.Fences))
	}
	for i := range manifest.Fences {
		if decoded.Fences[i] != manifest.Fences[i] {
			t.Fatalf("fence %d changed on the wire: published %+v, decoded %+v",
				i, manifest.Fences[i], decoded.Fences[i])
		}
	}
	if got := testutil.ToFloat64(failures); got != 0 {
		t.Fatalf("a successful publish counted %v failures", got)
	}
}

// 🔴 A PUBLISH THE BROKER REFUSES IS COUNTED, WHICH IS THE ONLY THING LEFT GUARDING A
// CONFIGURATION THE OLD DESIGN HANDLED BY DEGRADING.
//
// infrastructure.nats.streamMaxMsgSize is chart configuration and values.schema.json states
// no minimum, so a ceiling below a manifest is a legal deployment. Under the pointer-fact
// design that configuration still worked — everything became a pointer. Under this one the
// broker refuses every announcement, and the difference between a diagnosable outage and a
// silent one is this counter moving.
func TestARefusedPublishIsCounted(t *testing.T) {
	writer := &captureFactWriter{refuse: errors.New("maximum payload exceeded")}
	failures := failureCounter()
	pub := NewGeoFenceSetWriter(writer, 1<<20, failures)

	pub.PublishGeoFenceSetManifest(context.Background(), manifestOf(9, 2))

	if got := testutil.ToFloat64(failures); got != 1 {
		t.Fatalf("a refused publish counted %v failures; want 1", got)
	}
	if len(writer.payloads) != 0 {
		t.Fatalf("a refusing writer recorded %d payloads", len(writer.payloads))
	}
}

// The counter is nil-safe, and the publish still happens. Wiring in tests and in a service
// built before the metric existed both hand it nil, and a panic there would take down an
// authoring request that had already committed.
func TestAMissingCounterDoesNotBreakPublishing(t *testing.T) {
	writer := &captureFactWriter{}
	pub := NewGeoFenceSetWriter(writer, 1<<20, nil)
	pub.PublishGeoFenceSetManifest(context.Background(), manifestOf(1, 1))
	if len(writer.payloads) != 1 {
		t.Fatalf("expected one published message with no counter wired, got %d", len(writer.payloads))
	}

	refusing := &captureFactWriter{refuse: errors.New("nope")}
	NewGeoFenceSetWriter(refusing, 1<<20, nil).
		PublishGeoFenceSetManifest(context.Background(), manifestOf(2, 1))
}

// The published bytes are a manifest and carry NO geometry. This is the property the whole
// slice exists to produce, and it is asserted on the wire form rather than on the struct
// because the struct could not express geometry even by mistake — which would make a
// struct-level assertion vacuous. Here a producer that reverted to sending fence bodies would
// show up as a "geometry" key in the payload.
func TestThePublishedFactCarriesNoGeometry(t *testing.T) {
	writer := &captureFactWriter{}
	pub := NewGeoFenceSetWriter(writer, 1<<20, failureCounter())
	pub.PublishGeoFenceSetManifest(context.Background(), manifestOf(4, 5))

	payload := string(writer.payloads[0])
	if strings.Contains(payload, "geometry") {
		t.Fatalf("a published fence-set fact named geometry: %s", payload)
	}
	// The control: the payload really is the manifest and really does describe five fences,
	// so "no geometry" is not passing because nothing was published or nothing was named.
	var shape struct {
		Fences []map[string]json.RawMessage `json:"fences"`
	}
	if err := json.Unmarshal(writer.payloads[0], &shape); err != nil {
		t.Fatalf("published payload is not a manifest: %v", err)
	}
	if len(shape.Fences) != 5 {
		t.Fatalf("published manifest described %d fences; want 5", len(shape.Fences))
	}
	for i, fence := range shape.Fences {
		if _, ok := fence["hash"]; !ok {
			t.Fatalf("manifest entry %d carries no hash: %v", i, fence)
		}
		if len(fence) != 2 {
			t.Fatalf("manifest entry %d carries %d keys; a manifest entry is exactly a token "+
				"and a hash, and an extra key is how geometry would come back: %v", i, len(fence), fence)
		}
	}
}
