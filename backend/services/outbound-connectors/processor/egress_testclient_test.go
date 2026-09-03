// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"net/http"
	"net/netip"

	"github.com/devicechain-io/dc-microservice/egress"
)

// loopbackClient is the HTTP client every executor in this package's tests is given.
//
// It exists because httpsink.DefaultClient now dials through an egress guard with no
// allowances, and every test server here binds 127.0.0.1 — so passing nil would have the
// tests refused by the very boundary they are not testing.
//
// It is built as a guard WITH an explicit loopback allowance rather than as an unguarded
// client, and that is deliberate twice over. It keeps the tests running through the same
// dial path production uses, so a change that breaks the transport still shows up here.
// And it makes the allowance visible: production is constructed with no allowances, these
// tests with exactly one, and the difference is the thing a reader should see.
func loopbackClient() *http.Client {
	return &http.Client{
		Transport: egress.NewGuard([]netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		}).Transport(),
	}
}
