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
// a body-delivery deadline and an execution deadline (see core.BoundRequests). Every
// such handler this service registers goes through it: the OAuth endpoints and the
// service-token mint here, and the JWK sets and the branding-logo endpoints from
// their own registrations. Without it only the server's header timeout applies, and a
// client that stalls its body holds the connection for as long as it likes.
func BoundRequests(h http.Handler, maxBodyBytes int64) http.Handler {
	return core.BoundRequests(h, core.RequestBounds{
		MaxBodyBytes:    maxBodyBytes,
		BodyReadTimeout: bodyReadTimeout,
		ExecTimeout:     execTimeout,
	})
}
