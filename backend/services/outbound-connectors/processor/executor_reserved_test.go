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

// Reserved credentials are refused before resolution, terminally, without reaching the endpoint.
func TestABearerHandleInAReservedNamespaceIsRefused(t *testing.T) {
	cur, prev := webhooksig.KeyNames(model.WebhookSigningBase("hooks/prod"))
	for _, handle := range []string{cur, prev, webhooksig.ReservedPrefix + "anything", model.ConnectorSecretName(7), "connector/anything"} {
		srv, got := recordingEndpoint(t)
		store := signingStore(keyNew, keyOld)
		store.values[model.ConnectorSecretName(7)] = []byte("connector-token")
		store.values["connector/anything"] = []byte("other-connector-token")
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
	store.values["http-credential/hooks/auth"] = []byte("tok")
	req := signedCall(srv.URL)
	req.HTTPCall.SecretRef = "hooks/auth"
	res := newTestExecutor(store).Execute(context.Background(), req)

	assert.Equal(t, outcomeSent, res.outcome, "%v", res.err)
	_, h, _ := got.get()
	assert.Equal(t, "Bearer tok", h.Get("Authorization"))
	assert.NotEmpty(t, h.Get(webhooksig.HeaderSignature))
}

// Raw secret names cannot be addressed by an authored HTTP credential handle.
func TestHTTPCallBearerCredentialNamespace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		storedName string
		wantSent   bool
	}{
		{"raw handle refused", "hooks/prod", false},
		{"HTTP credential sent", "http-credential/hooks/prod", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := recordingEndpoint(t)
			store := &fakeSecretStore{values: map[string][]byte{tc.storedName: []byte("http-token")}}
			req := httpCallWithHandle(srv.URL)
			req.HTTPCall.SecretRef = "hooks/prod"
			res := newTestExecutor(store).Execute(context.Background(), req)
			hits, headers, _ := got.get()
			if tc.wantSent {
				assert.Equal(t, outcomeSent, res.outcome, "%v", res.err)
				assert.NoError(t, res.err)
				assert.Equal(t, 1, hits)
				assert.Equal(t, "Bearer http-token", headers.Get("Authorization"))
			} else {
				assert.Equal(t, outcomeInvalid, res.outcome)
				assert.False(t, res.retryable)
				assert.ErrorIs(t, res.err, httpsink.ErrMissingCredential)
				assert.Zero(t, hits)
			}
		})
	}
}
