// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// GeoFenceSetWriter is the concrete, NATS-backed implementation of
// model.GeoFenceSetPublisher (ADR-078): it marshals a newly-minted fence set's MANIFEST and
// publishes it to the geofence-set-manifest subject. Like the roster, detection-rules,
// device-attribute and entity-deleted writers it lives in the processor layer (which owns the
// messaging writer) and is injected into the shared *Api at wiring time, keeping the model
// free of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the tenant in
// the caller's context (the fence write runs under the request's tenant), so no tenant
// plumbing is needed here — and so a fence set can never be published onto another tenant's
// subject by a bug in this file. A marshal or publish failure is logged, counted and
// swallowed; it must never fail or roll back the authoring action, which is the source of
// truth.
//
// 🔴 THIS FILE USED TO BE MOSTLY SIZE MACHINERY, AND UNDERSTANDING WHAT DELETED IT MATTERS
// MORE THAN THE CODE THAT REMAINS. The fact it published carried the whole frozen fence set,
// so a tenant at the documented authoring ceiling — model.MaxGeoFencesPerTenant fences of
// model.MaxGeoFenceVertices positions — marshalled to more than the broker's per-message
// limit, and every one of that tenant's fence edits produced a publish the broker refused, a
// log line nobody was reading, and a mutation that returned 200. Downstream, containment
// reported a counted eval error for that tenant on every location event, indefinitely. The
// answer at the time was to publish a POINTER fact instead when the payload was too big,
// which needed a headroom subtraction, a floor under it, and a live read of the broker's own
// max_payload because the chart can only set half the wall.
//
// None of that is reachable any more, because the fact no longer carries geometry. A manifest
// is a version, a timestamp, and a token plus a 64-character content address per fence: at
// the fence ceiling it measures model.MaxGeoFenceSetManifestBytes(), a forty-eighth of a
// 1 MiB message, and it stays inside that ceiling at roughly 4,876 fences. There is no fence
// set whose SIZE can prevent its announcement, so there is no second form for the writer to
// choose between and nothing for it to measure.
//
// 🔴 WHAT SURVIVES IS THE COUNTER, AND IT IS NOT VESTIGIAL. "No fence set can be too large"
// is a statement about fence sets, not about deployments: infrastructure.nats.streamMaxMsgSize
// is chart configuration, values.schema.json states no minimum, and an operator who sets it
// to 2048 gets a broker that refuses every manifest. Under the old design that configuration
// degraded correctly (everything became a pointer fact). Under this one it stops fence
// announcements entirely, and the only thing standing between that and silence is a refused
// publish being COUNTED rather than merely logged. A floor on the config would refuse the
// deployment instead, which is a harsher answer to a degradation than a degradation deserves.
type GeoFenceSetWriter struct {
	writer messaging.MessageWriter
	// failures counts manifests this writer could not put on the wire, for any reason —
	// a marshal error, a broker refusal, a transport fault. Nil-safe.
	//
	// It is deliberately NOT a size metric. The interesting question an operator has is
	// "are fence edits reaching the engine?", and every way the answer can be no belongs
	// on one counter; splitting it by cause would leave the causes nobody enumerated
	// uncounted, which is how the original defect stayed invisible.
	failures prometheus.Counter
}

// NewGeoFenceSetWriter builds a fence-set manifest publisher over the given writer.
//
// maxMsgSize is the broker's configured per-message ceiling
// (infrastructure.nats.streamMaxMsgSize), where a non-positive value means no ceiling was
// stated. It is used for ONE thing — warning, at startup, that the deployment has configured
// a ceiling too small for a full manifest — and is deliberately not retained: there is no
// runtime decision left for it to inform.
//
// 🔴 THE WARNING CONSULTS THE CONFIGURED CEILING ALONE, AND CANNOT CONSULT THE BROKER'S. The
// server's account-wide max_payload is a second, independent wall that this chart cannot set,
// but it is CONNECTION-DERIVED state: nats.Connect returns a usable conn before it has
// connected (RetryOnFailedConnect is on so a service starting ahead of the broker does not
// crashloop), and nats.go answers the zero value for every connection-derived field until the
// status is CONNECTED. This constructor runs inside the NATS manager's own initialize, so
// reading max_payload here would read 0 on exactly the startup ordering the platform is built
// for. That trap has been documented three times in this codebase; the honest response is to
// warn on what is knowable now and let the counter above report what is not.
func NewGeoFenceSetWriter(writer messaging.MessageWriter, maxMsgSize int32,
	failures prometheus.Counter) *GeoFenceSetWriter {
	if worst := model.MaxGeoFenceSetManifestBytes(); maxMsgSize > 0 && int(maxMsgSize) < worst {
		log.Warn().Int32("streamMaxMsgSize", maxMsgSize).Int("worstCaseManifestBytes", worst).
			Msg("The configured per-message ceiling is smaller than a full geofence-set manifest. A tenant near the fence limit will have its fence edits refused by the broker, and its containment will lag until a reconcile sweep. Raise infrastructure.nats.streamMaxMsgSize.")
	}
	return &GeoFenceSetWriter{writer: writer, failures: failures}
}

// PublishGeoFenceSetManifest marshals and publishes one fence-set manifest. It never returns
// an error (the interface is fire-and-forget); failures are logged and counted.
func (w *GeoFenceSetWriter) PublishGeoFenceSetManifest(ctx context.Context, manifest *model.GeoFenceSetManifest) {
	encoded, err := model.MarshalGeoFenceSetManifest(manifest)
	if err != nil {
		log.Error().Err(err).Int32("version", manifest.Version).
			Msg("Unable to marshal geofence set manifest; publishing nothing. Containment for this tenant holds its previous fence set until a reconcile sweep.")
		w.countFailure()
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: encoded}); err != nil {
		log.Error().Err(err).Int32("version", manifest.Version).Int("bytes", len(encoded)).
			Int("fences", len(manifest.Fences)).
			Msg("Unable to publish geofence set manifest. Containment for this tenant holds its previous fence set until a reconcile sweep repairs it.")
		w.countFailure()
	}
}

// countFailure increments the failure counter when one is wired. Counted only on the paths
// that actually failed, never optimistically ahead of a write, so the number means what its
// help string says it means.
func (w *GeoFenceSetWriter) countFailure() {
	if w.failures != nil {
		w.failures.Inc()
	}
}
