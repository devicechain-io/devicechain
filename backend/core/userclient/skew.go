// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	graphql "github.com/graph-gophers/graphql-go"
)

// LegacySchema supplies the conservative contract for an older endpoint that
// predates capability discovery. It is consulted only for endpoints whose release
// was independently identified with WithLegacyEndpointVersion. An empty result
// refuses undiscoverable targets.
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
// before using the session. Successful contracts are cached for this session only,
// by exact endpoint URL. Use a new session for each command or after an upgrade.
// Authentication exchanges retain their stable contract. Legacy fallback also
// requires WithLegacyEndpointVersion; HTTP status alone cannot identify a release.
func (s *TenantSession) WithSchemaNegotiation(legacy LegacySchema) *TenantSession {
	if s.schemaNegotiation == nil {
		s.schemaNegotiation = &schemaNegotiation{}
	}
	s.schemaNegotiation.legacy = legacy
	return s
}

// WithSchemaNegotiation enables schema checks before admin operations. Call it
// before using the session. Its cache and legacy policy are the same as the tenant
// session's WithSchemaNegotiation.
func (a *AdminSession) WithSchemaNegotiation(legacy LegacySchema) *AdminSession {
	if a.schemaNegotiation == nil {
		a.schemaNegotiation = &schemaNegotiation{}
	}
	a.schemaNegotiation.legacy = legacy
	return a
}

// WithLegacyEndpointVersion records an independently verified release for one
// exact endpoint URL, including its plane and query string. Obtain the version
// from trusted deployment metadata, never from an OPTIONS status or this client's
// version. Only stable v1.7.x and v1.8.x releases permit the conservative fallback;
// other versions still require discovery. Configure before using the session,
// together with WithSchemaNegotiation's legacy schema provider, in either order.
func (s *TenantSession) WithLegacyEndpointVersion(endpoint, version string) *TenantSession {
	if s.schemaNegotiation == nil {
		s.WithSchemaNegotiation(nil)
	}
	s.schemaNegotiation.setLegacyEndpointVersion(endpoint, version)
	return s
}

// WithLegacyEndpointVersion records a verified endpoint release for admin legacy
// fallback; see TenantSession.WithLegacyEndpointVersion. Configure before use.
func (a *AdminSession) WithLegacyEndpointVersion(endpoint, version string) *AdminSession {
	if a.schemaNegotiation == nil {
		a.WithSchemaNegotiation(nil)
	}
	a.schemaNegotiation.setLegacyEndpointVersion(endpoint, version)
	return a
}

const schemaCapabilityMediaType = "application/vnd.devicechain.schema+json"

type schemaNegotiation struct {
	legacy         LegacySchema
	legacyVersions map[string]string
	mu             sync.Mutex
	contracts      map[string]*schemaContract
}

type schemaContract struct {
	ready        chan struct{}
	schema       *graphql.Schema
	conservative bool
	err          error
}

var legacyVersionPattern = regexp.MustCompile(`^v?1\.(7|8)\.(0|[1-9][0-9]*)$`)

func supportedLegacyVersion(version string) bool { return legacyVersionPattern.MatchString(version) }

func (n *schemaNegotiation) setLegacyEndpointVersion(endpoint, version string) {
	if n.legacyVersions == nil {
		n.legacyVersions = make(map[string]string)
	}
	n.legacyVersions[endpoint] = version
}

// contractFor shares discovery AND the parsed schema across this command's
// operations, including concurrent calls. Endpoints never share entries. Failed
// or cancelled discovery is not cached, and a waiting caller can cancel its wait.
func (n *schemaNegotiation) contractFor(ctx context.Context, httpc *http.Client, endpoint string, headers map[string]string) (*schemaContract, error) {
	n.mu.Lock()
	if cached := n.contracts[endpoint]; cached != nil {
		n.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-cached.ready:
			return cached, cached.err
		}
	}
	if n.contracts == nil {
		n.contracts = make(map[string]*schemaContract)
	}
	cached := &schemaContract{ready: make(chan struct{})}
	n.contracts[endpoint] = cached
	n.mu.Unlock()

	sdl, conservative, err := n.discover(ctx, httpc, endpoint, headers)
	cached.conservative = conservative
	if err != nil {
		cached.err = fmt.Errorf("cannot negotiate this instance's schema; discovery is required unless its legacy endpoint version was independently verified; dcctl: select --instance <id> with a readable Instance release record, or upgrade to v1.9.0+ and allow OPTIONS discovery through the proxy: %w", err)
	} else {
		cached.schema, err = graphql.ParseSchema(sdl, nil, graphql.UseStringDescriptions())
		if err != nil {
			cached.err = fmt.Errorf("instance returned an invalid schema contract: %w", err)
		}
	}
	n.mu.Lock()
	if cached.err != nil {
		delete(n.contracts, endpoint)
	}
	close(cached.ready)
	n.mu.Unlock()
	return cached, cached.err
}

// check validates every operation against this session's endpoint contract.
// Never retry a dispatched mutation or rewrite the caller's operation or input.
func (n *schemaNegotiation) check(ctx context.Context, httpc *http.Client, endpoint string, headers map[string]string, query string, variables map[string]any) error {
	if n == nil {
		return nil
	}
	cached, err := n.contractFor(ctx, httpc, endpoint, headers)
	if err != nil {
		return err
	}
	contract, conservative := cached.schema, cached.conservative
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
	// The manifest belongs to this exact endpoint. Refuse every redirect before
	// another request can carry the bearer to a different plane, origin or scheme.
	// Copy the client so its transport, timeout and jar remain intact and the
	// caller's redirect policy is never mutated.
	discoveryClient := *defaultHTTP(httpc)
	discoveryClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := discoveryClient.Do(req)
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
	// Proxies also return these statuses for current servers, so they are NOT
	// version evidence. A fallback additionally requires the caller's independent,
	// exact-endpoint identification within the supported legacy release window.
	legacy := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNoContent
	if legacy && supportedLegacyVersion(n.legacyVersions[endpoint]) && n.legacy != nil {
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
