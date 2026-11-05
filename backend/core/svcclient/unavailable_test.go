// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
)

func TestIsUnavailable(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"an unsupported scheme", &url.Error{Op: "Post", URL: "ftp://x", Err: errors.New("unsupported protocol scheme")}, false},
		{"a redirect loop", &url.Error{Op: "Post", URL: "http://x", Err: errors.New("stopped after 10 redirects")}, false},
		{"cancelled by the caller", fmt.Errorf("svcclient: %w", context.Canceled), false},
		{"cancelled inside the call", &url.Error{Op: "Post", URL: "http://x", Err: context.Canceled}, false},
		{"the token endpoint says 503", fmt.Errorf("%w: %w", errors.New("service token"), &mintStatusError{Status: 503}), true},
		{"the token endpoint says 401", &mintStatusError{Status: 401}, false},
		{"connection refused", fmt.Errorf("svcclient: call x: %w", refused), true},
		{"deadline", fmt.Errorf("svcclient: call x: %w", context.DeadlineExceeded), true},
		{"truncated read", fmt.Errorf("svcclient: read response: %w", io.ErrUnexpectedEOF), true},
		{"503", &StatusError{Status: 503}, true},
		{"500", &StatusError{Status: 500}, true},
		{"429", &StatusError{Status: 429}, true},
		{"404", &StatusError{Status: 404}, false},
		{"401", &StatusError{Status: 401}, false},
		{"the peer refused: FORBIDDEN", &GraphQLError{Codes: []string{"FORBIDDEN"}, Messages: []string{"no"}}, false},
		{"wrapped GraphQLError", fmt.Errorf("lookup: %w", &GraphQLError{Messages: []string{"no"}}), false},
		{"response too large", fmt.Errorf("%w: x", ErrResponseTooLarge), false},
		{"decode error", errors.New("svcclient: decode data: bad json"), false},
	} {
		if got := IsUnavailable(c.err); got != c.want {
			t.Errorf("%s: IsUnavailable = %v, want %v", c.name, got, c.want)
		}
	}
}
