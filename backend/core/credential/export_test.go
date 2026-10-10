// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package credential

// ObserveCompares wraps c's compares so a test sees the stored value every check paid for.
func ObserveCompares(c *Checker, seen func(hash []byte)) { c.observeCompares(seen) }

// Dummy is what c compares an unknown principal of kind k against.
func Dummy(c *Checker, k Kind) []byte { return c.dummies[k] }

// DigestWithSalt is Digest under a chosen salt, for a known-answer vector.
func DigestWithSalt(k *DeviceSecretKey, tenant string, salt []byte, secret string) string {
	return k.digestWithSalt(tenant, salt, secret)
}

// RecordConstantTimeCompareLengths replaces the constant-time compare DeviceSecretKey.Verify
// calls with one that records its two inputs' lengths, until the returned restore runs.
func RecordConstantTimeCompareLengths(record func(a, b int)) (restore func()) {
	inner := constantTimeCompare
	constantTimeCompare = func(a, b []byte) int {
		record(len(a), len(b))
		return inner(a, b)
	}
	return func() { constantTimeCompare = inner }
}
