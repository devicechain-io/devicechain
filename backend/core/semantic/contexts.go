// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package semantic

import (
	"embed"
	"sort"
)

// The JSON-LD contexts a standards facade may serve, or accept by exact URL.
// vocabgen copies each one byte for byte from a hash-verified upstream file into
// contexts/, and writes pinnedContexts (contexts_gen.go) to say which URL each
// file answers for. Nothing here fetches a context at run time: a facade that
// meets a context URL not listed below has no business dereferencing it.
//
//go:embed contexts/*.jsonld
var contextFS embed.FS

// PinnedContextInfo describes one embedded context.
type PinnedContextInfo struct {
	// URL is the exact URL the context is served from and accepted at.
	URL string
	// File is its name inside the embedded contexts directory.
	File string
	// SHA256 is the hex digest of the file; it equals the digest in the
	// source lock for the upstream file it was copied from.
	SHA256 string
	// Licence is the SPDX identifier of the upstream licence.
	Licence string
}

// PinnedContexts lists the embedded contexts ordered by URL.
func PinnedContexts() []PinnedContextInfo {
	out := append([]PinnedContextInfo(nil), pinnedContexts...)
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// PinnedContext returns the embedded context for an exact URL. A URL that is
// not listed -- including one differing only by case, a trailing slash or a
// query -- is not found.
func PinnedContext(url string) ([]byte, bool) {
	for _, c := range pinnedContexts {
		if c.URL == url {
			b, err := contextFS.ReadFile("contexts/" + c.File)
			if err != nil {
				return nil, false
			}
			return b, true
		}
	}
	return nil, false
}
