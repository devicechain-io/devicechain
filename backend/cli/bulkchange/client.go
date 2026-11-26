// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	graphql "github.com/graph-gophers/graphql-go"
)

type sessionClient struct {
	*userclient.TenantSession
	// fresh builds a new, not yet signed-in session for the same person; nil when the caller
	// cannot sign in again.
	fresh func() *userclient.TenantSession
}

// NewClient wraps a schema-negotiating tenant session with a fresh capability
// probe. Production disables GraphQL introspection; OPTIONS is its supported,
// authenticated discovery contract. The session still owns authentication/MFA.
func NewClient(session *userclient.TenantSession) Client {
	return &sessionClient{TenantSession: session}
}

// NewStepUpClient is NewClient that can also sign in again for a call that needs a recent
// second factor. fresh must build a session for the same person, server and tenant, and must
// answer its second-factor challenge with a NEW code: a refreshed token keeps the old
// verification time, so only a new sign-in proves a fresh factor.
func NewStepUpClient(session *userclient.TenantSession, fresh func() *userclient.TenantSession) StepUpClient {
	return &sessionClient{TenantSession: session, fresh: fresh}
}

// StepUp signs in again now, so the one-time code is asked for here, and uses the new session
// from then on.
func (c *sessionClient) StepUp(ctx context.Context) error {
	if c.fresh == nil {
		return errors.New("this client cannot sign in again")
	}
	session := c.fresh()
	if session == nil {
		return errors.New("could not build a session to sign in again")
	}
	if _, err := session.AccessToken(ctx); err != nil {
		return err
	}
	c.TenantSession = session
	return nil
}

func (c *sessionClient) ServesBulkChanges(ctx context.Context, endpoint string) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodOptions, endpoint, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/vnd.devicechain.schema+json")
	httpc := c.HTTPClient(endpoint)
	// Probe this exact endpoint. A redirect cannot establish its capabilities.
	httpc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpc.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("schema capability probe returned HTTP %d", response.StatusCode)
	}
	const maxSchemaBytes = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSchemaBytes+1))
	if err != nil {
		return false, err
	}
	if len(raw) > maxSchemaBytes {
		return false, fmt.Errorf("schema capability probe exceeded %d bytes", maxSchemaBytes)
	}
	var manifest struct {
		Format int    `json:"format"`
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return false, err
	}
	if manifest.Format != 1 || strings.TrimSpace(manifest.Schema) == "" {
		return false, fmt.Errorf("unsupported or empty schema capability format %d", manifest.Format)
	}
	schema, err := graphql.ParseSchema(manifest.Schema, nil, graphql.UseStringDescriptions())
	if err != nil {
		return false, err
	}
	return schema.AST().Types["BulkChange"] != nil, nil
}
