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

// Frozen from v1.3.0:backend/core/cmddownlink/wire.go:76-91 (field order and tags),
// and :108-113 (PublishResponse marshals the struct directly).
type responseEnvelopeV130 struct {
	CommandToken  string  `json:"commandToken"`
	Success       bool    `json:"success"`
	Payload       *string `json:"payload,omitempty"`
	Error         *string `json:"error,omitempty"`
	Reach         string  `json:"reach,omitempty"`
	DispatchNonce string  `json:"dispatchNonce"`
}

// v1.3.0:backend/services/outbound-connectors/lpwan/adapter.go:299-302 refuses
// confirmed:true BEFORE ClaimDispatch; :401-406 quotes j.nonce (the envelope's
// nonce), :410-411 publishes it, and :445-447 formats the reason payload.
// N1 stands for the particular dispatch nonce; only that runtime value is substituted.
const refusalV130 = `{"commandToken":"cmd-1","success":false,"payload":"{\"reason\":\"UNSUPPORTED\"}","error":"confirmed downlinks are not available yet: the acknowledgement feed is not built; send the command unconfirmed","reach":"UNSENT","dispatchNonce":"N1"}`
const refusalErrorV130 = "confirmed downlinks are not available yet: the acknowledgement feed is not built; send the command unconfirmed"
const refusalPayloadV130 = `{"reason":"UNSUPPORTED"}`

func TestPrevReleaseLPWANConfirmedWireSnapshot(t *testing.T) {
	payload, text := refusalPayloadV130, refusalErrorV130
	encoded, err := json.Marshal(responseEnvelopeV130{
		CommandToken: "cmd-1", Success: false, Payload: &payload, Error: &text,
		Reach: "UNSENT", DispatchNonce: "N1",
	})
	if err != nil || string(encoded) != refusalV130 {
		t.Fatalf("v1.3.0 refusal encoding = %s, err=%v; a changed fixture would stop proving rolling-upgrade compatibility", encoded, err)
	}
}

// Use the existing SQLite realApi harness and the real dispatch/publish path.
// MarkSent in model/api.go stamps SENT and N1 before publishing; SENT here does
// not imply any adapter has claimed or any network server has received the command.
func dispatchedConfirmedV130(t *testing.T) (*model.Api, context.Context, string) {
	t.Helper()
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	params := `{"fPort":1,"base64":"AQ==","confirmed":true}`
	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "cmd-1", DeviceToken: "pump-1", Name: "downlink", Payload: &params,
	})
	if err != nil {
		t.Fatalf("create confirmed downlink: %v; the upgrade drill needs a real queued command", err)
	}
	writer := &failingWriter{} // records the publish, with no injected failure
	producer := procWith(api, writer)
	producer.LpwanDownlink = true
	if err := producer.deliverCommand(ctx, created, pathSweep, routingOf(true, integrationSource)); err != nil {
		t.Fatalf("dispatch integration downlink: %v; no old pod could consume this command", err)
	}
	n1 := dispatchNonceOf(t, writer.messages)
	h := writer.messages[0].Headers
	if h[cmddownlink.HeaderTransport] != "integration" || h[cmddownlink.HeaderSource] != integrationSource {
		t.Fatalf("routing headers = %v; the command would not reach the LPWAN adapter during an upgrade", h)
	}
	row := rowOf(t, api, "acme", "cmd-1")
	if row.Status != "SENT" || !row.DispatchNonce.Valid || row.DispatchNonce.String != n1 || row.Outcome.Valid {
		t.Fatalf("dispatched row = %+v; the upgrade test must start with the actual unsettled dispatch", row)
	}
	return api, ctx, n1
}

func consumeRefusalV130(t *testing.T, api *model.Api, nonce string) *CommandDeliveryProcessor {
	t.Helper()
	consumer := responseConsumer(api, &deadRecorder{}, []byte(strings.Replace(refusalV130, `"dispatchNonce":"N1"`, `"dispatchNonce":"`+nonce+`"`, 1)))
	if readAndHandleOne(consumer, context.Background()) {
		t.Fatal("response consumer stopped; a rolling-upgrade refusal must be processed rather than leave the command awaiting an answer")
	}
	return consumer
}

func TestPrevReleaseLPWANConfirmedBeforeClaim(t *testing.T) {
	api, _, n1 := dispatchedConfirmedV130(t)
	consumer := consumeRefusalV130(t, api, n1)
	row := rowOf(t, api, "acme", "cmd-1")
	if row.Status == "SENT" || row.Status != "FAILED" || !row.Outcome.Valid || row.Outcome.String != "NOT_DELIVERED" {
		t.Errorf("old pod refusal left status=%s outcome=%v; an unsent confirmed downlink must finish FAILED/NOT_DELIVERED, never remain SENT", row.Status, row.Outcome)
	}
	if !row.Error.Valid || row.Error.String != refusalErrorV130 {
		t.Errorf("error = %v; operators would lose the old pod's explanation that confirmed downlinks are unsupported", row.Error)
	}
	if row.ResponsePayload == nil {
		t.Error("stored payload is absent; operators would lose the UNSUPPORTED reason from the old pod")
	} else if string(*row.ResponsePayload) != refusalPayloadV130 {
		t.Errorf("stored payload = %s; the UNSUPPORTED refusal must survive response decoding and storage unchanged", *row.ResponsePayload)
	}
	if row.DeliveredTime.Valid || row.DeliveryEvidence.Valid {
		t.Errorf("delivery evidence = %v / %v; a refusal before the wire must never claim the device received the command", row.DeliveredTime, row.DeliveryEvidence)
	}
	if !row.RespondedTime.Valid || counterValue(t, consumer.ResponsesStaleNonce) != 0 {
		t.Error("current dispatch refusal was not recorded as a response; the command could remain unsettled during an upgrade")
	}
}

func TestPrevReleaseLPWANConfirmedAfterNewPodClaim(t *testing.T) {
	api, ctx, n1 := dispatchedConfirmedV130(t)
	// ConfirmDispatch is the model endpoint behind the v1.4 adapter's ClaimDispatch.
	n2, won, err := api.ConfirmDispatch(ctx, "cmd-1", n1)
	if err != nil || !won || n2 == "" || n2 == n1 {
		t.Fatalf("v1.4 claim: nonce=%q won=%v err=%v; the mixed-version case requires a rotated claim awaiting acknowledgement", n2, won, err)
	}
	before := rowOf(t, api, "acme", "cmd-1")
	if before.Status != "SENT" || before.DispatchNonce.String != n2 || before.Outcome.Valid {
		t.Fatalf("claimed row = %+v; the network-server downlink must still be pending under N2", before)
	}
	consumer := consumeRefusalV130(t, api, n1)
	after := rowOf(t, api, "acme", "cmd-1")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("stale refusal changed the claimed row: status=%s nonce=%s outcome=%v; the network server may already hold the downlink, so NOT_DELIVERED would be a lie", after.Status, after.DispatchNonce.String, after.Outcome)
	}
	if got := counterValue(t, consumer.ResponsesStaleNonce); got != 1 {
		t.Errorf("stale-nonce refusals = %v, want 1; an old pod's N1 refusal must be rejected while the v1.4 pod owns N2", got)
	}
	// Cheapest real terminal path: a response naming the winning claim N2. Drive
	// it through the same processor to prove the stale refusal did not strand SENT.
	terminal := responseConsumer(api, &deadRecorder{}, answer("cmd-1", n2))
	readAndHandleOne(terminal, context.Background())
	settled := rowOf(t, api, "acme", "cmd-1")
	if settled.Status != "SUCCESSFUL" || settled.Outcome.String != "SUCCEEDED" || !settled.RespondedTime.Valid {
		t.Errorf("N2 response left status=%s outcome=%v; rejecting an old pod must still let the winning pod settle the command instead of stranding SENT", settled.Status, settled.Outcome)
	}
}
