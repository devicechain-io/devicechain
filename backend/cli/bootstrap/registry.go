// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "strings"

// isPublishedRegistry compares registry identities without changing the spelling
// the operator supplied for stored state or rendered image references.
func isPublishedRegistry(r string) bool {
	return normalizeRegistry(r) == DefaultImageRegistry
}

// normalizeRegistry reduces the spellings of one registry to a comparable form:
// surrounding space, an https:// scheme, trailing slashes, case (including the
// path) and the default :443 are not differences. Other ports and paths remain.
func normalizeRegistry(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimRight(r, "/")
	host, rest, hasPath := strings.Cut(r, "/")
	host = strings.TrimSuffix(host, ":443")
	if hasPath {
		return host + "/" + rest
	}
	return host
}
