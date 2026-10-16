// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"testing"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
)

// testSecretTransit is the key tests seal presented secrets under and give a resolver to
// open them with.
func testSecretTransit(t testing.TB) *credential.SecretTransit {
	t.Helper()
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// presentSealed makes e present the MQTT credential cid with secret, sealed for the
// tenant in ctx, the way event-sources publishes it.
func presentSealed(t testing.TB, ctx context.Context, e *esmodel.UnresolvedEvent, ctype, cid, secret string) {
	t.Helper()
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		t.Fatal("presentSealed needs a tenant in the context")
	}
	sealed, err := testSecretTransit(t).Seal(tenant, cid, secret)
	if err != nil {
		t.Fatal(err)
	}
	e.CredentialType, e.CredentialId, e.SealedCredentialSecret = &ctype, &cid, sealed
}

// presentSealedToken makes e present the ACCESS_TOKEN credential token, sealed for the
// tenant in ctx and the credential type, the way event-sources publishes it: the clear
// credential id is left empty.
func presentSealedToken(t testing.TB, ctx context.Context, e *esmodel.UnresolvedEvent, ctype, token string) {
	t.Helper()
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		t.Fatal("presentSealedToken needs a tenant in the context")
	}
	sealed, err := testSecretTransit(t).SealToken(tenant, ctype, token)
	if err != nil {
		t.Fatal(err)
	}
	e.CredentialType, e.CredentialId, e.SealedCredentialId = &ctype, nil, sealed
}
