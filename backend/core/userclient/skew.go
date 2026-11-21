// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	graphql "github.com/graph-gophers/graphql-go"
)

// LegacySchema supplies the conservative contract for an older endpoint that
// predates capability discovery. An empty result refuses undiscoverable targets.
type LegacySchema func(endpoint string) string

// UnsupportedFeatureError reports a request that cannot safely reach this instance.
type UnsupportedFeatureError struct {
	Detail       string
	Conservative bool
}

func (e *UnsupportedFeatureError) Error() string {
	if e.Conservative {
		return fmt.Sprintf("cannot confirm support for %s using the legacy v1.7 contract; upgrade to v1.9.0 for schema negotiation", e.Detail)
	}
	return fmt.Sprintf("this instance is too old for %s; upgrade to v1.9.0", e.Detail)
}

// WithSchemaNegotiation enables schema checks before tenant operations. Call it
// before using the session. Authentication exchanges retain their stable contract.
func (s *TenantSession) WithSchemaNegotiation(legacy LegacySchema) *TenantSession {
	s.schemaNegotiation = &schemaNegotiation{legacy: legacy}
	return s
}

// WithSchemaNegotiation enables schema checks before admin operations. Call it
// before using the session.
func (a *AdminSession) WithSchemaNegotiation(legacy LegacySchema) *AdminSession {
	a.schemaNegotiation = &schemaNegotiation{legacy: legacy}
	return a
}

const schemaCapabilityMediaType = "application/vnd.devicechain.schema+json"

type schemaNegotiation struct {
	legacy LegacySchema
}

// check negotiates each operation independently. A session may address several
// services and planes, and their contracts can change during its lifetime. Never
// reuse one endpoint's schema for another, or retry a dispatched mutation.
func (n *schemaNegotiation) check(ctx context.Context, httpc *http.Client, endpoint string, headers map[string]string, query string, variables map[string]any) error {
	if n == nil {
		return nil
	}
	sdl, conservative, err := n.discover(ctx, httpc, endpoint, headers)
	if err != nil {
		return fmt.Errorf("cannot negotiate this instance's schema; upgrade to v1.9.0 if it predates discovery: %w", err)
	}
	contract, err := graphql.ParseSchema(sdl, nil, graphql.UseStringDescriptions())
	if err != nil {
		return fmt.Errorf("instance returned an invalid schema contract: %w", err)
	}
	// Validate the JSON shape actually sent on the wire, including structs, typed
	// slices, explicit nulls and nested input objects. Do not modify caller input.
	raw, err := json.Marshal(variables)
	if err != nil {
		return fmt.Errorf("encode GraphQL variables: %w", err)
	}
	var wireVariables map[string]any
	if err := json.Unmarshal(raw, &wireVariables); err != nil {
		return fmt.Errorf("decode GraphQL variables: %w", err)
	}
	if errs := contract.ValidateWithVariables(query, wireVariables); len(errs) > 0 {
		messages := make([]string, 0, len(errs))
		for _, e := range errs {
			messages = append(messages, e.Message)
		}
		return &UnsupportedFeatureError{Detail: strings.Join(messages, "; "), Conservative: conservative}
	}
	if conservative {
		if name := legacyPreviewCapability(contract, query, wireVariables); name != "" {
			return &UnsupportedFeatureError{Detail: fmt.Sprintf("preview capability %q (requires discovery)", name), Conservative: true}
		}
	}
	return nil
}

func (n *schemaNegotiation) discover(ctx context.Context, httpc *http.Client, endpoint string, headers map[string]string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, endpoint, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", schemaCapabilityMediaType)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := defaultHTTP(httpc).Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(raw) > maxResponseBytes {
		return "", false, fmt.Errorf("schema discovery exceeded %d bytes", maxResponseBytes)
	}
	// Pre-discovery GraphQL handlers try to decode an empty OPTIONS body (400).
	// Proxies may instead report an unsupported route/method. Only these legacy
	// answers permit the conservative baseline; authentication and outages never do.
	legacy := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNoContent
	if legacy && n.legacy != nil {
		if sdl := n.legacy(endpoint); sdl != "" {
			return sdl, true, nil
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, &StatusError{URL: endpoint, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	var manifest struct {
		Format int    `json:"format"`
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", false, fmt.Errorf("decode schema discovery: %w", err)
	}
	if manifest.Format != 1 || strings.TrimSpace(manifest.Schema) == "" {
		return "", false, fmt.Errorf("unsupported or empty schema discovery format %d", manifest.Format)
	}
	return manifest.Schema, false, nil
}
