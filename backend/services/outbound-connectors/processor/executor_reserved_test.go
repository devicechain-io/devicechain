// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	"github.com/devicechain-io/dc-microservice/httpsink"
	"github.com/devicechain-io/dc-microservice/webhooksig"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/stretchr/testify/assert"
)

// A signing key is never readable after it is issued. A rule that names one as its BEARER
// credential would send it to the rule's own endpoint, so the executor refuses before it resolves
// anything, terminally, and the endpoint is never reached.
func TestABearerHandleInTheSigningNamespaceIsRefused(t *testing.T) {
	cur, prev := webhooksig.KeyNames(model.WebhookSigningBase("hooks/prod"))
	for _, handle := range []string{cur, prev, webhooksig.ReservedPrefix + "anything"} {
		srv, got := recordingEndpoint(t)
		store := signingStore(keyNew, keyOld)
		req := &connectorwire.ConnectorDispatchRequest{
			Kind: connectorwire.ConnectorKindHTTPCall, Tenant: "acme", IdempotencyKey: "k1", Payload: `{}`,
			HTTPCall: &connectorwire.HTTPCallDispatch{URL: srv.URL, SecretRef: handle},
		}
		res := newTestExecutor(store).Execute(context.Background(), req)

		assert.Equal(t, outcomeInvalid, res.outcome, handle)
		assert.False(t, res.retryable, handle)
		assert.ErrorIs(t, res.err, httpsink.ErrAuthRefused, handle)
		hits, _, _ := got.get()
		assert.Zero(t, hits, "%s: the endpoint received the key", handle)
		assert.Zero(t, store.resolveCalls(), "%s: the store was asked for the key", handle)
	}
}

// The counterweight: an ordinary Bearer handle still resolves and is presented.
func TestAnOrdinaryBearerHandleStillWorksBesideSigning(t *testing.T) {
	srv, got := recordingEndpoint(t)
	store := signingStore(keyNew, "")
	store.values["hooks/auth"] = []byte("tok")
	req := signedCall(srv.URL)
	req.HTTPCall.SecretRef = "hooks/auth"
	res := newTestExecutor(store).Execute(context.Background(), req)

	assert.Equal(t, outcomeSent, res.outcome, "%v", res.err)
	_, h, _ := got.get()
	assert.Equal(t, "Bearer tok", h.Get("Authorization"))
	assert.NotEmpty(t, h.Get(webhooksig.HeaderSignature))
}
