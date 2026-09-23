// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

// ObserveCompares wraps c's compare so a test sees the hash every check paid for.
func ObserveCompares(c *Checker, seen func(hash []byte)) {
	inner := c.compare
	c.compare = func(hash, secret []byte) error {
		seen(hash)
		return inner(hash, secret)
	}
}
