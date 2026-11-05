// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
)

// IsUnavailable reports whether err means the peer could not be REACHED or could not serve right
// now, so that asking again later is the right answer: a transport failure (connection refused or
// reset, a timeout, a cancelled or expired context, a truncated read) or a 5xx or 429 status.
//
// 🔴 IT IS NARROW ON PURPOSE. A peer that ANSWERED is not unavailable: a GraphQLError is the peer
// refusing (FORBIDDEN, a bad argument) and asking again changes nothing; ErrResponseTooLarge and a
// decode error are about what the peer sent, and a 4xx is about what was asked. A caller that
// reported any of those as "unavailable" would have its clients resend a request that can never
// succeed. An error it does not recognise is not unavailable.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var gql *GraphQLError
	if errors.As(err, &gql) || errors.Is(err, ErrResponseTooLarge) {
		return false
	}
	// A caller that gave up is not the peer being down, whichever way the cancellation arrived
	// (while waiting for a token, or inside the HTTP call).
	if errors.Is(err, context.Canceled) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status >= http.StatusInternalServerError || status.Status == http.StatusTooManyRequests
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// http.Client wraps every failure in a *url.Error, which is itself a net.Error whatever its
	// cause; the cause decides: a refused or reset connection or a timeout is an outage, an
	// unsupported scheme or a redirect loop is a request that can never succeed.
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Timeout() || (ue.Err != nil && IsUnavailable(ue.Err))
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
