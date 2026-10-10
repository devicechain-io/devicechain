// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
)

// MaxSmallBodyBytes is the body ceiling for this service's plain HTTP endpoints that
// take no body, or only a small one: userinfo, the discovery documents, the JWK sets.
const MaxSmallBodyBytes = 1 << 16

// bodyReadTimeout and execTimeout are the deadlines BoundRequests applies; zero means
// core's defaults. Variables only so a test can shorten them; nothing else assigns
// them.
var bodyReadTimeout, execTimeout time.Duration

// BoundRequests wraps one of user-management's plain (non-GraphQL) HTTP handlers with
// a body-delivery deadline and an execution deadline, buffering the body up to
// maxBodyBytes before the handler runs (see core.BoundRequests). The OAuth endpoints,
// the service-token mint, the discovery documents and the JWK sets go through it:
// each reads a small body before deciding anything, or takes none. Without it only
// the server's header timeout applies, and a client that stalls its body holds the
// connection for as long as it likes.
func BoundRequests(h http.Handler, maxBodyBytes int64) http.Handler {
	return core.BoundRequests(h, core.RequestBounds{
		MaxBodyBytes:    maxBodyBytes,
		BodyReadTimeout: bodyReadTimeout,
		ExecTimeout:     execTimeout,
	})
}

// RequestDeadlines applies the same two deadlines without buffering anything, for a
// handler that authenticates before it reads its body and bounds that read itself —
// the branding-logo upload. Buffering in front of it would hold up to a logo's worth
// of memory for a caller who has not yet shown a token.
func RequestDeadlines(h http.Handler) http.Handler {
	return core.RequestDeadlines(h, core.RequestBounds{
		BodyReadTimeout: bodyReadTimeout,
		ExecTimeout:     execTimeout,
	})
}
