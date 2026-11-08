// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// graphqlPort is the shared data-plane port every service serves /graphql on.
const graphqlPort = 8080

// maxGraphQLResponseBytes caps a downstream GraphQL response so a pathological
// query result cannot exhaust memory (defence in depth; tool queries are bounded).
const maxGraphQLResponseBytes = 8 << 20 // 8 MiB

// GraphQLClient calls the per-area DeviceChain GraphQL endpoints on behalf of the
// MCP caller (ADR-047). It forwards the CALLER'S validated access token as the
// bearer credential — never a service token — so every downstream read inherits
// the user's tenant scope and role checks, and the MCP server structurally cannot
// exceed the calling user (the anti-confused-deputy design).
//
// 🔴 IT DOES NOT INHERIT AN AUDIT TRAIL, AND THIS COMMENT USED TO SAY IT DID. The
// platform's audit journal records mutations and sign-ins, never a query, so a read
// made here leaves no row in any area's journal. The record of a tool call is the
// audit line this server writes itself — see audit.go.
type GraphQLClient struct {
	http    *http.Client
	baseURL func(area string) string
}

// NewGraphQLClient builds a client that reaches services by in-cluster Service DNS
// (http://<area>:8080/graphql) — not through the external ingress.
func NewGraphQLClient() *GraphQLClient {
	return &GraphQLClient{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: func(area string) string { return fmt.Sprintf("http://%s:%d/graphql", area, graphqlPort) },
	}
}

// gqlError is one entry of a GraphQL response's errors array.
type gqlError struct {
	Message string `json:"message"`
}

// document is one GraphQL document MCP sends, together with the area that serves it.
//
// Every document is a STRING the compiler does not read, sent to a schema that lives in
// another module, so a field dropped from a schema used to break the tool selecting it
// only at runtime, with no CI signal. Carrying the area WITH the text is what lets
// documents_test.go validate every document against the schema it is actually sent to,
// and it means a call site can no longer send a document to the wrong area.
//
// Build one only through newDocument, which records it in documents: a document built as
// a literal would be sent but never validated. documents_test.go refuses such a literal in
// this package's non-test code, whether written as document{...} or with its type elided
// inside a slice or map literal of documents, and checks that every newDocument call site
// was recorded under the area it names.
type document struct {
	area string
	text string
}

// documents is every document built by newDocument, in declaration order. It is read
// only by documents_test.go.
var documents []document

// newDocument declares a document MCP sends to area and records it for validation.
func newDocument(area, text string) document {
	d := document{area: area, text: text}
	documents = append(documents, d)
	return d
}

// maxErrorTextRunes bounds how much of a downstream error message reaches the model.
const maxErrorTextRunes = 300

// markedErrorText renders a downstream GraphQL error message as DATA.
//
// 🔴 A TOOL ERROR IS PLAIN TEXT TO THE MODEL, AND A GRAPHQL MESSAGE CAN ECHO TENANT TEXT. A
// resolver that says `device "<name>" is not allowed` has copied a string a device or operator
// chose into the one place the `untrusted` envelope did not reach. So the message is never
// returned bare: it is cleaned of control characters, bounded, and wrapped in the same
// envelope a successful result uses, as JSON the model can parse as a value rather than read
// as prose. The least trusting source class is used because the message's origin is unknown.
func markedErrorText(msg string) string {
	var b strings.Builder
	n := 0
	for _, r := range msg {
		if n >= maxErrorTextRunes {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	out, err := json.Marshal(map[string]any{"untrusted": map[string]any{
		"message": Marked{Value: b.String(), Source: SourceDevice},
	}})
	if err != nil {
		return `{"untrusted":{}}`
	}
	return string(out)
}

// Query executes a GraphQL document against its area's endpoint, forwarding the
// caller's bearer token, and unmarshals the `data` field into out. A transport
// failure, a non-2xx status, or any GraphQL `errors` entry is returned as an error
// (the tool surfaces it to the model as a failed call).
func (c *GraphQLClient) Query(ctx context.Context, doc document, token string, variables map[string]any, out any) error {
	area := doc.area
	body, err := json.Marshal(map[string]any{"query": doc.text, "variables": variables})
	if err != nil {
		return fmt.Errorf("marshaling query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(area), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s graphql: %w", area, err)
	}
	defer resp.Body.Close()

	// Read one byte past the cap so that outgrowing it is DETECTED rather than inferred.
	// io.LimitReader stops silently at its limit, so without the extra byte an oversized
	// response arrives as half a JSON document and is reported as a decoding failure —
	// which sends the model, and whoever is reading its output, after a peer that is
	// working correctly. Status is still decided first: a large error page is a failing
	// peer, not a request that asked for too much.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphQLResponseBytes+1))
	if err != nil {
		return fmt.Errorf("reading %s graphql response: %w", area, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s graphql returned HTTP %d", area, resp.StatusCode)
	}
	if len(raw) > maxGraphQLResponseBytes {
		return fmt.Errorf("the %s response exceeded %d bytes; narrow the request (fewer tokens, "+
			"a smaller page, or a shorter time range) and try again", area, maxGraphQLResponseBytes)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decoding %s graphql response: %w", area, err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("%s graphql error: %s", area, markedErrorText(envelope.Errors[0].Message))
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("unmarshaling %s graphql data: %w", area, err)
		}
	}
	return nil
}
