// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bulkchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/devicechain-io/dc-microservice/userclient"
	graphql "github.com/graph-gophers/graphql-go"
)

type sessionClient struct{ *userclient.TenantSession }

// NewClient wraps a schema-negotiating tenant session with a fresh capability
// probe. Production disables GraphQL introspection; OPTIONS is its supported,
// authenticated discovery contract. The session still owns authentication/MFA.
func NewClient(session *userclient.TenantSession) Client {
	return &sessionClient{session}
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
