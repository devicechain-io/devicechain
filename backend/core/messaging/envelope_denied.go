// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "strings"

// probeDenied reports whether err says the broker REFUSED the read-back (a permissions or
// authorization violation), as opposed to the read failing for the ordinary reasons (a timeout, a
// message that is gone). The two need different people: a denied read is a missing grant
// (core/natsauth, Stream.ReadBackWriters) that no retry will cure, and it must be loud.
func probeDenied(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "permissions violation") || strings.Contains(s, "authorization violation") ||
		strings.Contains(s, "permission denied") || strings.Contains(s, "not authorized")
}
