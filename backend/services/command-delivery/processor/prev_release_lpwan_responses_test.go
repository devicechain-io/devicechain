// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/cmddownlink"
	"github.com/devicechain-io/dc-microservice/core"
)

const prevRelease = "v1.4.0"

// Frozen from v1.4.0:backend/core/cmddownlink/wire.go:ResponseEnvelope.
// Field order and tags are verbatim; PublishResponseContext marshals this struct.
type responseEnvelopePrev struct {
	CommandToken string  `json:"commandToken"`
	Success      bool    `json:"success"`
	Payload      *string `json:"payload,omitempty"`
	Error        *string `json:"error,omitempty"`

	// Reach is omitted on success and empty when the device itself answered no.
	Reach string `json:"reach,omitempty"`

	// DispatchNonce names the dispatch this outcome answers, and command-delivery REFUSES a
	// response that carries none. It is the nonce the adapter's own claim returned, never the
	// envelope's: the confirmation rotated it.
	DispatchNonce string `json:"dispatchNonce"`
}

// Citations below are paths in prevRelease, read with git show, not current imports.
const (
	adapterHandle   = "backend/services/outbound-connectors/lpwan/adapter.go:Adapter.handle"
	adapterAwaitAck = "backend/services/outbound-connectors/lpwan/adapter.go:Adapter.awaitAck"
	ackHandle       = "backend/services/outbound-connectors/lpwan/ackfeed.go:AckFeed.handle"
	ackSweep        = "backend/services/outbound-connectors/lpwan/ackfeed.go:AckFeed.Sweep"
)

// N1 is the dispatch envelope nonce; N2 is the rotated ClaimDispatch nonce.
// Adapter.refuse uses N1 before the claim; finish/awaitAck use N2 afterwards.
// AckFeed.handle and Sweep quote Pending.DispatchNonce (N2).
// reasonPayload encodes {"reason":"X"}; success maps marshal with sorted keys.
// Variable error inputs are pinned to concrete examples: ErrBadLink, an out-of-range
// fPort, HTTP 400 with no gRPC name, and the egress guard's ErrBlocked.
// Their text originates in adapter.go, params.go:ParamError.Error/ParseParams and
// chirpstack.go:ChirpStack.Enqueue/classifyTransport in the same tag.
const (
	wire_pre_NO_ROUTE                      = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_ROUTE\"}","error":"the command carries no integration for the device, so it cannot be routed to a network server","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_EXPIRED                       = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"EXPIRED\"}","error":"the command expired before it could be sent","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_INVALID_PARAMETERS_definition = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"INVALID_PARAMETERS\"}","error":"the command definition is invalid","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_INVALID_PARAMETERS_parse      = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"INVALID_PARAMETERS\"}","error":"parameter \"fPort\": must be 1 to 223","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_UNSUPPORTED_no_ack_feed       = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"UNSUPPORTED\"}","error":"confirmed downlinks are not available in this build: it has no acknowledgement feed; send the command unconfirmed","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_NO_NETWORK_SERVER_LINK        = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_NETWORK_SERVER_LINK\"}","error":"no lorawan_downlink connector names this device's integration; create and publish one","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_AMBIGUOUS_NETWORK_SERVER_LINK = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"AMBIGUOUS_NETWORK_SERVER_LINK\"}","error":"more than one lorawan_downlink connector names this device's integration; keep one","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_NETWORK_SERVER_LINK_INVALID   = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NETWORK_SERVER_LINK_INVALID\"}","error":"the network-server link for this device's integration is not usable: lpwan: the network-server link is not usable","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_NO_DEVICE_ID                  = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_DEVICE_ID\"}","error":"the device has no tts.device-id alias","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_NO_DEVEUI                     = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_DEVEUI\"}","error":"the device has no lorawan.deveui alias","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_pre_RATE_LIMITED                  = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"RATE_LIMITED\"}","error":"the tenant is over its outbound call ceiling; retry shortly","reach":"UNSENT","dispatchNonce":"N1"}`
	wire_post_queued                       = `{"commandToken":"cmd-1","success":true,"payload":"{\"queueItemId\":\"queue-1\",\"stage\":\"queued\"}","dispatchNonce":"N2"}`
	wire_post_EXPIRED                      = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"EXPIRED\"}","error":"the command expired before it could be sent","reach":"UNSENT","dispatchNonce":"N2"}`
	wire_post_NETWORK_SERVER_REFUSED       = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NETWORK_SERVER_REFUSED\"}","error":"the network server refused the downlink (HTTP 400)","reach":"UNSENT","dispatchNonce":"N2"}`
	wire_post_EGRESS_BLOCKED               = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"EGRESS_BLOCKED\"}","error":"the platform egress policy refused the network server's address","reach":"UNSENT","dispatchNonce":"N2"}`
	wire_post_unclassified                 = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NETWORK_SERVER_REFUSED\"}","error":"the downlink could not be completed","reach":"UNANSWERED","dispatchNonce":"N2"}`
	wire_post_ACK_UNTRACKED                = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"ACK_UNTRACKED\"}","error":"the network server queued the confirmed downlink but the platform could not track its acknowledgement","reach":"UNANSWERED","dispatchNonce":"N2"}`
	wire_ack_acknowledged                  = `{"commandToken":"cmd-1","success":true,"payload":"{\"fCntDown\":42,\"queueItemId\":\"queue-1\",\"stage\":\"acknowledged\"}","dispatchNonce":"N2"}`
	wire_ack_NOT_ACKNOWLEDGED              = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NOT_ACKNOWLEDGED\"}","error":"the network server reported the confirmed downlink was not acknowledged by the device in time; the device may have received it","reach":"UNANSWERED","dispatchNonce":"N2"}`
	wire_ack_DOWNLINK_FAILED               = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"DOWNLINK_FAILED\"}","error":"the network server reported it dropped the downlink before transmitting it","reach":"UNSENT","dispatchNonce":"N2"}`
	wire_ack_DOWNLINK_FAILED_MAYBE_SENT    = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"DOWNLINK_FAILED_MAYBE_SENT\"}","error":"the network server reported the downlink failed, and it cannot be shown that it was never transmitted; the device may have received it","reach":"UNANSWERED","dispatchNonce":"N2"}`
	wire_deadline_not_transmitted          = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_ACKNOWLEDGEMENT\"}","error":"the network server reported no outcome for the confirmed downlink in time; it may still be queued or may have been delivered","reach":"UNANSWERED","dispatchNonce":"N2"}`
	wire_deadline_transmitted              = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"NO_ACKNOWLEDGEMENT\"}","error":"a gateway transmitted the confirmed downlink but no acknowledgement was reported in time; the device may have received it","reach":"UNANSWERED","dispatchNonce":"N2"}`
)

type responseCasePrev struct {
	name, source, nonce, wire string
	success                   bool
	payload, errorText, reach string
	status, outcome           string
}

var responseCasesPrev = []responseCasePrev{
	{"pre_NO_ROUTE", adapterHandle, "N1", wire_pre_NO_ROUTE, false, `{"reason":"NO_ROUTE"}`, "the command carries no integration for the device, so it cannot be routed to a network server", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_EXPIRED", adapterHandle, "N1", wire_pre_EXPIRED, false, `{"reason":"EXPIRED"}`, "the command expired before it could be sent", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_INVALID_PARAMETERS_definition", adapterHandle, "N1", wire_pre_INVALID_PARAMETERS_definition, false, `{"reason":"INVALID_PARAMETERS"}`, "the command definition is invalid", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_INVALID_PARAMETERS_parse", adapterHandle, "N1", wire_pre_INVALID_PARAMETERS_parse, false, `{"reason":"INVALID_PARAMETERS"}`, "parameter \"fPort\": must be 1 to 223", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_UNSUPPORTED_no_ack_feed", adapterHandle, "N1", wire_pre_UNSUPPORTED_no_ack_feed, false, `{"reason":"UNSUPPORTED"}`, "confirmed downlinks are not available in this build: it has no acknowledgement feed; send the command unconfirmed", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_NO_NETWORK_SERVER_LINK", adapterHandle, "N1", wire_pre_NO_NETWORK_SERVER_LINK, false, `{"reason":"NO_NETWORK_SERVER_LINK"}`, "no lorawan_downlink connector names this device's integration; create and publish one", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_AMBIGUOUS_NETWORK_SERVER_LINK", adapterHandle, "N1", wire_pre_AMBIGUOUS_NETWORK_SERVER_LINK, false, `{"reason":"AMBIGUOUS_NETWORK_SERVER_LINK"}`, "more than one lorawan_downlink connector names this device's integration; keep one", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_NETWORK_SERVER_LINK_INVALID", adapterHandle, "N1", wire_pre_NETWORK_SERVER_LINK_INVALID, false, `{"reason":"NETWORK_SERVER_LINK_INVALID"}`, "the network-server link for this device's integration is not usable: lpwan: the network-server link is not usable", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_NO_DEVICE_ID", adapterHandle, "N1", wire_pre_NO_DEVICE_ID, false, `{"reason":"NO_DEVICE_ID"}`, "the device has no tts.device-id alias", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_NO_DEVEUI", adapterHandle, "N1", wire_pre_NO_DEVEUI, false, `{"reason":"NO_DEVEUI"}`, "the device has no lorawan.deveui alias", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"pre_RATE_LIMITED", adapterHandle, "N1", wire_pre_RATE_LIMITED, false, `{"reason":"RATE_LIMITED"}`, "the tenant is over its outbound call ceiling; retry shortly", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"post_queued", adapterHandle, "N2", wire_post_queued, true, `{"queueItemId":"queue-1","stage":"queued"}`, "", "", "SUCCESSFUL", "SUCCEEDED"},
	{"post_EXPIRED", adapterHandle, "N2", wire_post_EXPIRED, false, `{"reason":"EXPIRED"}`, "the command expired before it could be sent", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"post_NETWORK_SERVER_REFUSED", adapterHandle, "N2", wire_post_NETWORK_SERVER_REFUSED, false, `{"reason":"NETWORK_SERVER_REFUSED"}`, "the network server refused the downlink (HTTP 400)", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"post_EGRESS_BLOCKED", adapterHandle, "N2", wire_post_EGRESS_BLOCKED, false, `{"reason":"EGRESS_BLOCKED"}`, "the platform egress policy refused the network server's address", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"post_unclassified", adapterHandle, "N2", wire_post_unclassified, false, `{"reason":"NETWORK_SERVER_REFUSED"}`, "the downlink could not be completed", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
	{"post_ACK_UNTRACKED", adapterAwaitAck, "N2", wire_post_ACK_UNTRACKED, false, `{"reason":"ACK_UNTRACKED"}`, "the network server queued the confirmed downlink but the platform could not track its acknowledgement", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
	{"ack_acknowledged", ackHandle, "N2", wire_ack_acknowledged, true, `{"fCntDown":42,"queueItemId":"queue-1","stage":"acknowledged"}`, "", "", "SUCCESSFUL", "SUCCEEDED"},
	{"ack_NOT_ACKNOWLEDGED", ackHandle, "N2", wire_ack_NOT_ACKNOWLEDGED, false, `{"reason":"NOT_ACKNOWLEDGED"}`, "the network server reported the confirmed downlink was not acknowledged by the device in time; the device may have received it", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
	{"ack_DOWNLINK_FAILED", ackHandle, "N2", wire_ack_DOWNLINK_FAILED, false, `{"reason":"DOWNLINK_FAILED"}`, "the network server reported it dropped the downlink before transmitting it", "UNSENT", "FAILED", "NOT_DELIVERED"},
	{"ack_DOWNLINK_FAILED_MAYBE_SENT", ackHandle, "N2", wire_ack_DOWNLINK_FAILED_MAYBE_SENT, false, `{"reason":"DOWNLINK_FAILED_MAYBE_SENT"}`, "the network server reported the downlink failed, and it cannot be shown that it was never transmitted; the device may have received it", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
	{"deadline_not_transmitted", ackSweep, "N2", wire_deadline_not_transmitted, false, `{"reason":"NO_ACKNOWLEDGEMENT"}`, "the network server reported no outcome for the confirmed downlink in time; it may still be queued or may have been delivered", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
	{"deadline_transmitted", ackSweep, "N2", wire_deadline_transmitted, false, `{"reason":"NO_ACKNOWLEDGEMENT"}`, "a gateway transmitted the confirmed downlink but no acknowledgement was reported in time; the device may have received it", "UNANSWERED", "TIMEOUT", "UNKNOWN"},
}

// Use the existing SQLite realApi harness and the real dispatch/publish path.
// MarkSent in model/api.go stamps SENT and N1 before publishing; SENT here does
// not imply any adapter has claimed or any network server has received the command.
func dispatchedLPWANPrev(t *testing.T, confirmed bool) (*model.Api, context.Context, string) {
	t.Helper()
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	params := `{"fPort":1,"base64":"AQ==","confirmed":false}`
	if confirmed {
		params = `{"fPort":1,"base64":"AQ==","confirmed":true}`
	}
	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "downlink", Payload: &params,
	})
	if err != nil {
		t.Fatalf("%s create downlink: %v; the upgrade drill needs a real queued command", prevRelease, err)
	}
	writer := &failingWriter{} // records the publish, with no injected failure
	producer := procWith(api, writer)
	producer.LpwanDownlink = true
	if err := producer.deliverCommand(ctx, created, pathSweep, routingOf(true, integrationSource)); err != nil {
		t.Fatalf("%s dispatch integration downlink: %v; no old pod could consume this command", prevRelease, err)
	}
	n1 := dispatchNonceOf(t, writer.messages)
	h := writer.messages[0].Headers
	if h[cmddownlink.HeaderTransport] != "integration" || h[cmddownlink.HeaderSource] != integrationSource {
		t.Fatalf("%s routing headers = %v; the command would not reach the LPWAN adapter during an upgrade", prevRelease, h)
	}
	row := rowOf(t, api, "acme", "cmd-1")
	if row.Status != "SENT" || !row.DispatchNonce.Valid || row.DispatchNonce.String != n1 || row.Outcome.Valid {
		t.Fatalf("%s dispatched row = %+v; the upgrade test must start with the actual unsettled dispatch", prevRelease, row)
	}
	return api, ctx, n1
}

func TestPrevReleaseLPWANResponses(t *testing.T) {
	for _, tc := range responseCasesPrev {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("%s:%s carries %s; want %s/%s", prevRelease, tc.source, tc.nonce, tc.status, tc.outcome)
			var errText *string
			if tc.errorText != "" {
				errText = &tc.errorText
			}
			encoded, err := json.Marshal(responseEnvelopePrev{
				CommandToken: "cmd-1", Success: tc.success, Payload: &tc.payload,
				Error: errText, Reach: tc.reach, DispatchNonce: tc.nonce,
			})
			if err != nil || string(encoded) != tc.wire {
				t.Fatalf("%s:%s encoding = %s, err=%v; want frozen bytes %s", prevRelease, tc.source, encoded, err, tc.wire)
			}
			// The queued success is an unconfirmed command; all other cases exercise confirmed downlinks.
			api, ctx, nonce := dispatchedLPWANPrev(t, tc.name != "post_queued")
			if tc.nonce == "N2" {
				nonce = claimLPWANPrev(t, api, ctx, nonce)
			}
			consumer := consumeResponsePrev(t, api, tc.wire, tc.nonce, nonce)
			assertResponsePrev(t, api, tc)
			if got := counterValue(t, consumer.ResponsesStaleNonce); got != 0 {
				t.Errorf("%s current %s response counted stale: %v", prevRelease, tc.nonce, got)
			}
		})
	}
}

func assertResponsePrev(t *testing.T, api *model.Api, tc responseCasePrev) {
	t.Helper()
	row := rowOf(t, api, "acme", "cmd-1")
	if row.Status == "SENT" || row.Status != tc.status || !row.Outcome.Valid || row.Outcome.String != tc.outcome {
		t.Errorf("%s response: status=%s outcome=%v; want %s/%s, never SENT", prevRelease, row.Status, row.Outcome, tc.status, tc.outcome)
	}
	if row.Error.Valid != (tc.errorText != "") || row.Error.String != tc.errorText {
		t.Errorf("%s stored error=%v; want %q", prevRelease, row.Error, tc.errorText)
	}
	if row.ResponsePayload == nil || string(*row.ResponsePayload) != tc.payload {
		t.Errorf("%s stored payload=%v; want %s", prevRelease, row.ResponsePayload, tc.payload)
	}
	if !row.RespondedTime.Valid {
		t.Errorf("%s response time absent", prevRelease)
	}
	if tc.success && tc.name == "post_queued" {
		// An unconfirmed downlink the network server only queued: the device never answered,
		// so it settles SUCCESSFUL with no delivery evidence (the previous release's wire shape
		// is mapped to UNANSWERED by the current command-delivery).
		if row.DeliveredTime.Valid || row.DeliveryEvidence.Valid {
			t.Errorf("%s queued unconfirmed downlink stamped delivery evidence=%v/%v; want none", prevRelease, row.DeliveredTime, row.DeliveryEvidence)
		}
	} else if tc.success {
		if !row.DeliveredTime.Valid || !row.DeliveryEvidence.Valid || row.DeliveryEvidence.String != model.EvidenceDeviceResponse {
			t.Errorf("%s success delivery evidence=%v/%v; want DEVICE_RESPONSE under current rules", prevRelease, row.DeliveredTime, row.DeliveryEvidence)
		}
	} else if row.DeliveredTime.Valid || row.DeliveryEvidence.Valid {
		t.Errorf("%s %s failure stamped delivery evidence=%v/%v", prevRelease, tc.reach, row.DeliveredTime, row.DeliveryEvidence)
	}
}

func claimLPWANPrev(t *testing.T, api *model.Api, ctx context.Context, n1 string) string {
	t.Helper()
	n2, won, err := api.ConfirmDispatch(ctx, "cmd-1", n1)
	if err != nil || !won || n2 == "" || n2 == n1 {
		t.Fatalf("%s claim: nonce=%q won=%v err=%v; need rotated N2", prevRelease, n2, won, err)
	}
	row := rowOf(t, api, "acme", "cmd-1")
	if row.Status != "SENT" || row.DispatchNonce.String != n2 || row.Outcome.Valid {
		t.Fatalf("%s claimed row=%+v; need unsettled SENT under N2", prevRelease, row)
	}
	return n2
}

// Only the runtime nonce is substituted; every other byte is the frozen literal.
func consumeResponsePrev(t *testing.T, api *model.Api, wire, placeholder, nonce string) *CommandDeliveryProcessor {
	t.Helper()
	body := strings.Replace(wire, `"dispatchNonce":"`+placeholder+`"`, `"dispatchNonce":"`+nonce+`"`, 1)
	consumer := responseConsumer(api, &deadRecorder{}, []byte(body))
	if readAndHandleOne(consumer, context.Background()) {
		t.Fatalf("%s response consumer stopped", prevRelease)
	}
	return consumer
}

func TestPrevReleaseLPWANStaleRefusalThenAcknowledgement(t *testing.T) {
	api, ctx, n1 := dispatchedLPWANPrev(t, true)
	n2 := claimLPWANPrev(t, api, ctx, n1)
	before := rowOf(t, api, "acme", "cmd-1")
	// A pod claimed, may have enqueued, then crashed before acknowledgement. Redelivery
	// reaches another pod whose pre-claim expiry check refuses with the envelope's N1.
	consumer := consumeResponsePrev(t, api, wire_pre_EXPIRED, "N1", n1)
	after := rowOf(t, api, "acme", "cmd-1")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("%s stale N1 refusal changed claimed row: before=%+v after=%+v", prevRelease, before, after)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 1 {
		t.Errorf("%s stale-nonce count=%v; want 1", prevRelease, got)
	}
	consumeResponsePrev(t, api, wire_ack_acknowledged, "N2", n2)
	for _, tc := range responseCasesPrev {
		if tc.name == "ack_acknowledged" {
			assertResponsePrev(t, api, tc)
		}
	}
}

func TestPrevReleaseLPWANLateAcknowledgementIgnored(t *testing.T) {
	api, ctx, n1 := dispatchedLPWANPrev(t, true)
	n2 := claimLPWANPrev(t, api, ctx, n1)
	// A sweep settled first. A late positive ack must preserve even its error/payload,
	// outcome and lack of delivery evidence, not turn a timeout into success.
	consumeResponsePrev(t, api, wire_deadline_transmitted, "N2", n2)
	before := rowOf(t, api, "acme", "cmd-1")
	if before.Status != "TIMEOUT" || before.Outcome.String != "UNKNOWN" {
		t.Fatalf("%s deadline did not settle row: %+v", prevRelease, before)
	}
	consumer := consumeResponsePrev(t, api, wire_ack_acknowledged, "N2", n2)
	after := rowOf(t, api, "acme", "cmd-1")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("%s late N2 acknowledgement changed terminal row: before=%+v after=%+v", prevRelease, before, after)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 0 {
		t.Errorf("%s late current-nonce settlement counted stale: %v", prevRelease, got)
	}
}
