// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"net/netip"

	"github.com/devicechain-io/dc-microservice/egress"
)

// loopbackGuard is the egress guard every executor in this package's tests is given.
//
// It exists because every test server here binds 127.0.0.1, and a guard with no allowances
// — production's default — would have the tests refused by the very boundary they are not
// testing.
//
// It is a real guard WITH an explicit loopback allowance rather than no guard, and that is
// deliberate twice over. It keeps the tests running through the same dial path production
// uses, so a change that breaks the transport or the publish dial still shows up here. And
// it makes the allowance visible: production is constructed with the operator's allowances,
// these tests with exactly loopback, and the difference is the thing a reader should see.
func loopbackGuard() *egress.Guard {
	return egress.NewGuard([]netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	})
}
