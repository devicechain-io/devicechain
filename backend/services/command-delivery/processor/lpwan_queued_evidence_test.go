// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

func TestQueuedDownlinkResponsesRecordNoDeliveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		payload  string
		evidence bool
	}{
		{"v1.4", `{"commandToken":"cmd-1","success":true,"payload":"{\"queueItemId\":\"queue-1\",\"stage\":\"queued\"}","dispatchNonce":"<nonce>"}`, `{"queueItemId":"queue-1","stage":"queued"}`, false},
		{"new", `{"commandToken":"cmd-1","success":true,"reach":"UNANSWERED","payload":"{\"queueItemId\":\"queue-1\",\"stage\":\"queued\"}","dispatchNonce":"<nonce>"}`, `{"queueItemId":"queue-1","stage":"queued"}`, false},
		{"direct object", `{"commandToken":"cmd-1","success":true,"payload":{"stage":"queued","queueItemId":"q"},"dispatchNonce":"<nonce>"}`, `{"stage":"queued","queueItemId":"q"}`, false},
		{"native answer", `{"commandToken":"cmd-1","success":true,"payload":{"ok":true},"dispatchNonce":"<nonce>"}`, `{"ok":true}`, true},
		{"extra key", `{"commandToken":"cmd-1","success":true,"payload":{"stage":"queued","queueItemId":"q","extra":1},"dispatchNonce":"<nonce>"}`, `{"stage":"queued","queueItemId":"q","extra":1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := realApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
				Token: "cmd-1", DeviceToken: "pump-1", Name: "downlink",
			})
			require.NoError(t, err)
			writer := &recordingWriter{}
			require.NoError(t, procWith(api, writer).deliverCommand(ctx, created, pathSweep, routing{}))
			require.Equal(t, model.CommandSent.String(), loadByToken(t, api, ctx, "cmd-1").Status)
			body := strings.ReplaceAll(tc.body, "<nonce>", dispatchNonceOf(t, writer.messages))
			dead := &deadRecorder{}
			consumer := responseConsumer(api, dead, []byte(body))
			require.False(t, readAndHandleOne(consumer, context.Background()))

			row := loadByToken(t, api, ctx, "cmd-1")
			require.Equal(t, model.CommandSuccessful.String(), row.Status)
			require.True(t, row.Outcome.Valid)
			require.Equal(t, model.OutcomeSucceeded.String(), row.Outcome.String)
			require.Equal(t, tc.evidence, row.DeliveredTime.Valid)
			require.Equal(t, tc.evidence, row.DeliveryEvidence.Valid)
			if tc.evidence {
				require.Equal(t, model.EvidenceDeviceResponse, row.DeliveryEvidence.String)
			}
			require.NotNil(t, row.ResponsePayload)
			require.JSONEq(t, tc.payload, string(*row.ResponsePayload))
			require.Empty(t, dead.msgs)
		})
	}
}

func TestResponseReachLegacyQueuedCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		success bool
		reach   model.Reach
		want    model.Reach
	}{
		{"legacy string", `"{\"queueItemId\":\"q\",\"stage\":\"queued\"}"`, true, "", model.ReachUnanswered},
		{"object", `{"stage":"queued","queueItemId":"q"}`, true, "", model.ReachUnanswered},
		{"failure", `{"stage":"queued","queueItemId":"q"}`, false, "", model.ReachAnswered},
		{"explicit reach", `{"stage":"queued","queueItemId":"q"}`, true, model.ReachUnsent, model.ReachUnsent},
		{"unknown reach", `{"ok":true}`, true, "future", model.ReachUnanswered},
		{"ordinary answer", `{"ok":true}`, true, "", model.ReachAnswered},
		{"extra key", `{"stage":"queued","queueItemId":"q","extra":1}`, true, "", model.ReachAnswered},
		{"extra string key", `{"stage":"queued","queueItemId":"q","extra":"x"}`, true, "", model.ReachAnswered},
		{"empty id", `{"stage":"queued","queueItemId":""}`, true, "", model.ReachAnswered},
		{"non-string id", `{"stage":"queued","queueItemId":1}`, true, "", model.ReachAnswered},
		{"null id", `{"stage":"queued","queueItemId":null}`, true, "", model.ReachAnswered},
		{"missing id", `{"stage":"queued"}`, true, "", model.ReachAnswered},
		{"acknowledged", `{"stage":"acknowledged","queueItemId":"q"}`, true, "", model.ReachAnswered},
		{"null", `null`, true, "", model.ReachAnswered},
		{"malformed", `"{broken"`, true, "", model.ReachAnswered},
		{"absent", "", true, "", model.ReachAnswered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := responseEnvelope{Success: tc.success, Reach: tc.reach}
			if tc.payload != "" {
				payload := json.RawMessage(tc.payload)
				response.Payload = &payload
			}
			require.Equal(t, tc.want, responseReach(response))
		})
	}
}
