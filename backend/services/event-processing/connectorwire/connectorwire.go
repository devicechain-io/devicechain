// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package connectorwire is the JSON wire contract for REACT's outbound-connector dispatch (ADR-060):
// the message event-processing produces and the dedicated outbound-connectors service (slice C3)
// consumes. It is a DELIBERATELY leaf, stdlib-only package (imports just encoding/json + time) so the
// C3 connector binary can bind the contract WITHOUT dragging in event-processing's model package (its
// gorm stores, migrations, and the two SQL drivers) — the "lean connector binary" posture of ADR-060
// §4. It also can't reach event-processing's internal/rules (the CEL layer) across the module boundary,
// which is exactly the determinism/supply-chain firewall the ADR wants: CEL stays out of the connector
// binary. Keeping the contract here (not in model/) also freezes a clean import path before C3 binds it.
package connectorwire

import (
	"encoding/json"
	"fmt"
	"time"
)

// ConnectorKindHTTPCall / ConnectorKindPublish are the canonical ConnectorDispatchRequest.Kind tokens
// (ADR-060) — the SHARED wire vocabulary both the producer (event-processing's connector sink) and the
// consumer (the outbound-connectors service, slice C3) bind to. They live here, in the wire struct's
// own leaf package, because it is the one package both modules import; neither can import
// event-processing's internal/rules across the module boundary. A hardcoded literal on either side
// would drift, so both sides reference these constants, and the producer maps its internal action type
// onto them EXPLICITLY (react_connector_client.wireConnectorKind) — a spelling change is a
// compile-visible mapping edit, not a silent misroute. Their string values equal rules.ActionHTTPCall
// / rules.ActionPublish by construction; the mapping asserts that so the two can never diverge unseen.
const (
	ConnectorKindHTTPCall = "httpCall"
	ConnectorKindPublish  = "publish"
)

// MaxTimeoutMs is the shared ceiling on a connector action's per-dispatch timeout (ADR-060). It lives
// here, in the leaf package both the authoring side (event-processing's rules gate) and the execution
// side (the outbound-connectors executor) import, so ONE constant bounds both — the authoring gate
// rejects a larger value at publish and the executor re-clamps it at dispatch (defense-in-depth
// against a forged/corrupt stored config that bypassed the gate), and the two can never disagree.
//
// It is kept safely BELOW the messaging layer's consumer AckWait (60s): a fetched batch of messages
// all start their AckWait timer at fetch, so a per-send timeout at or near AckWait would let a
// slow-but-succeeding send be redelivered underneath the worker — a duplicate outbound call plus a
// NumDelivered climb that would spuriously dead-letter a healthy endpoint. At 20s a full fetch batch
// clears the bounded worker pool well within AckWait, leaving margin.
const MaxTimeoutMs = 20_000

// ConnectorDispatchRequest is the message event-processing's REACT dispatcher publishes to the
// outbound-connectors service (ADR-060 §4) when a detection rule's httpCall or publish action fires on
// its rising edge. REACT does NOT execute the action — it renders the CEL payload template to bytes and
// hands the resolved request to the dedicated service, so the heavy connector dep-tree, the CEL
// runtime, and any secret resolution stay OUT of the replay-correct DETECT/REACT binary. The
// outbound-connectors service consumes this, resolves the SecretRef against the secret store (ADR-059),
// and performs the HTTP call / broker publish.
//
// It is a JSON message — like event-processing's derived events and its raise-alarm request (ADR-037),
// and unlike device-management's protobuf FACT streams — because it is an event-processing-produced
// request another service consumes, not one of this service's own emitted facts. The producer marshals
// it via MarshalConnectorDispatchRequest; the consumer decodes it via UnmarshalConnectorDispatchRequest.
type ConnectorDispatchRequest struct {
	// Kind is ConnectorKindHTTPCall or ConnectorKindPublish — the discriminator selecting which of
	// HTTPCall / Publish below is populated. The consumer routes on it (comparing the shared constant).
	Kind string `json:"kind"`
	// Tenant is the owning tenant (redundant with the subject, carried for convenience/attribution).
	Tenant string `json:"tenant"`
	// DeviceToken is the detection's series (the source device), carried for the connectors service's
	// logging / rate-attribution and any device context a template rendered into the payload.
	DeviceToken string `json:"deviceToken"`
	// RuleID is the composed runtime id of the rule that fired, for observability/tracing on the
	// connectors side (which rule drove this outbound call). Not part of the idempotency identity.
	RuleID string `json:"ruleId"`
	// Edge is always ConnectorKindHTTPCall/publish's rising edge ("raised") today — a connector action
	// is one-shot and has no falling-edge twin (react.dispatchAction) — but it is carried explicitly so
	// the wire contract stays true if a future edge model changes, and so a consumer can log it.
	Edge string `json:"edge,omitempty"`
	// OccurredTime is the detection's event time (the logical time the action is about).
	OccurredTime time.Time `json:"occurredTime"`
	// IdempotencyKey is the deterministic, content-addressed token the connectors service dedups on so
	// an at-least-once redelivery / DETECT replay collapses to ONE execution. It is opaque to the
	// consumer (a hex SHA-256, ADR-042 grammar-safe) — the consumer only compares it, never recomputes.
	IdempotencyKey string `json:"idempotencyKey"`
	// Payload is the CEL payload template ALREADY RENDERED to a string by REACT (empty ⇒ no body). The
	// connectors service never re-renders — it never imports cel (the determinism/supply-chain firewall,
	// ADR-060 §4) — it sends these bytes verbatim.
	Payload string `json:"payload,omitempty"`

	// Exactly one of HTTPCall / Publish is set, per Kind.
	HTTPCall *HTTPCallDispatch `json:"httpCall,omitempty"`
	Publish  *PublishDispatch  `json:"publish,omitempty"`
}

// HTTPCallDispatch is the resolved inline webhook config for a Kind==httpCall dispatch (ADR-060 Tier 1).
// The connectors service issues an HTTP request to URL with Method (POST) and Headers, resolving
// SecretRef (if set) to an Authorization credential via the secret store — REACT passes only the
// secret HANDLE, never a resolved credential (ADR-059: cleartext never on the wire).
type HTTPCallDispatch struct {
	URL       string            `json:"url"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	SecretRef string            `json:"secretRef,omitempty"`
	// TimeoutMs bounds the outbound call (0 ⇒ the connectors service's default). The publish gate
	// already bounded it to rules.MaxActionTimeoutMs.
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// PublishDispatch is the resolved config for a Kind==publish dispatch (ADR-060 Tier 2): a reference to
// a registered, tenant-scoped Connector the outbound-connectors service resolves to a transport
// (MQTT/Kafka/SNS/SQS/Pub-Sub) with its own credentials. The Connector entity is owned by that service
// (slice C4), so only the reference travels here.
type PublishDispatch struct {
	ConnectorRef string `json:"connectorRef"`
	TimeoutMs    int    `json:"timeoutMs,omitempty"`
}

// Validate is the CONSUMER-SIDE fail-closed structural check (ADR-060 §4 / slice C3): the
// outbound-connectors service is a SEPARATE binary that must self-enforce the contract, because a
// message on the connector-dispatch stream did not necessarily originate from this producer — a
// forged or corrupt message (broker write access), or a decode of a hand-edited row, could carry an
// unknown Kind or a Kind whose matching variant is absent. The producer's own populated-variant
// guarantee is not something the consumer may assume; it re-checks here before it does any work.
//
// It validates STRUCTURE only, from this stdlib-only leaf package: a known Kind, exactly the matching
// variant populated (and the other nil), a tenant, and the minimal per-variant field an executor
// cannot proceed without (an httpCall URL, a publish connectorRef). It deliberately does NOT
// re-validate the URL/header grammar or the secret handle — those belong to the executor, which owns
// core/httpsink and the secret store; keeping this package free of those imports is the CEL /
// supply-chain firewall (see the package doc). A failing request is terminal poison for the
// consumer (a redelivery cannot fix a malformed message), never a retry.
func (r *ConnectorDispatchRequest) Validate() error {
	if r.Tenant == "" {
		return fmt.Errorf("connectorwire: dispatch request has no tenant")
	}
	switch r.Kind {
	case ConnectorKindHTTPCall:
		if r.HTTPCall == nil {
			return fmt.Errorf("connectorwire: kind %q with no httpCall config", r.Kind)
		}
		if r.Publish != nil {
			return fmt.Errorf("connectorwire: kind %q must not carry a publish config", r.Kind)
		}
		if r.HTTPCall.URL == "" {
			return fmt.Errorf("connectorwire: httpCall dispatch has no url")
		}
	case ConnectorKindPublish:
		if r.Publish == nil {
			return fmt.Errorf("connectorwire: kind %q with no publish config", r.Kind)
		}
		if r.HTTPCall != nil {
			return fmt.Errorf("connectorwire: kind %q must not carry an httpCall config", r.Kind)
		}
		if r.Publish.ConnectorRef == "" {
			return fmt.Errorf("connectorwire: publish dispatch has no connectorRef")
		}
	default:
		return fmt.Errorf("connectorwire: unknown dispatch kind %q", r.Kind)
	}
	return nil
}

// MarshalConnectorDispatchRequest encodes a connector-dispatch request (the producer side).
func MarshalConnectorDispatchRequest(req *ConnectorDispatchRequest) ([]byte, error) {
	return json.Marshal(req)
}

// UnmarshalConnectorDispatchRequest decodes a connector-dispatch request (the consumer side).
func UnmarshalConnectorDispatchRequest(encoded []byte) (*ConnectorDispatchRequest, error) {
	req := &ConnectorDispatchRequest{}
	if err := json.Unmarshal(encoded, req); err != nil {
		return nil, err
	}
	return req, nil
}
