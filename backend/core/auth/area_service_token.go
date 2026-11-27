// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"path/filepath"
	"sort"
)

// The AREA-BOUND service-token mint: a second way to obtain a service token, for an area that must
// not be able to obtain more than its own ceiling under its own name.
//
// The shared-secret mint (ServiceTokenPath) trusts its caller completely: whoever holds the secret
// names the subject and the authorities, and the ceiling is only observed. An area in
// AreaBoundCallers never holds that secret. It presents a credential of its own instead, minted per
// area by dcctl and mounted only into that area and user-management, and the endpoint:
//
//   - derives the subject from the credential that matched — a request body naming one is refused;
//   - ENFORCES the ceiling: every requested authority must be in ServiceCeiling(area), and the
//     super-authority is refused outright;
//   - is served by no previous release, so an area-bound client talking to an older user-management
//     gets a 404 and no token, which is the fail-closed outcome (it has no shared secret to fall back
//     to, and svcclient would not use one if it had).
//
// The shared-secret endpoint is unchanged by any of this.

const (
	// AreaServiceTokenPath is the user-management endpoint (internal listener only, like
	// ServiceTokenPath) that mints a service token for an area-bound caller.
	AreaServiceTokenPath = "/auth/area-service-token"

	// AreaCredentialHeader carries the caller's area credential on an area-bound mint request. Like
	// the shared secret it travels only from the caller to user-management, never onward.
	AreaCredentialHeader = "X-DC-Area-Credential" //nolint:gosec // header name, not a credential

	// AreaMintCredentialDir is where an area-bound credential is mounted, in user-management (one
	// directory per area-bound area) and in the area itself (its own directory only):
	// <dir>/<area>/<AreaMintCredentialKey>.
	AreaMintCredentialDir = "/etc/dci-mint"

	// AreaMintCredentialKey is the Secret key, and so the file name, that holds a credential.
	AreaMintCredentialKey = "credential"

	// MinAreaCredentialLen is the shortest credential either side accepts. dcctl mints 32 random
	// bytes (43 characters, base64url); anything shorter is a mis-provisioned file, and an empty
	// one must never match an empty header.
	MinAreaCredentialLen = 32
)

// AreaServiceTokenRequest is the body a caller POSTs to AreaServiceTokenPath. There is no subject:
// the subject is the area whose credential matched. The endpoint decodes strictly, so a body that
// names a subject (or anything else) is refused, not ignored.
type AreaServiceTokenRequest struct {
	Authorities []string `json:"authorities"`
}

// areaBoundCallers is the closed list of areas that mint through AreaServiceTokenPath and never
// through the shared secret. Unexported so nothing can join it at run time; the chart's
// devicechain.areaBoundAreas helper is held to it by a test.
var areaBoundCallers = map[string]struct{}{
	"automation": {},
}

// AreaBoundCallerNames returns the area-bound callers, sorted.
func AreaBoundCallerNames() []string {
	out := make([]string, 0, len(areaBoundCallers))
	for a := range areaBoundCallers {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// IsAreaBoundCaller reports whether area mints only through the area-bound endpoint.
func IsAreaBoundCaller(area string) bool {
	_, ok := areaBoundCallers[area]
	return ok
}

// AreaMintCredentialFile is the path of one area's credential under dir.
func AreaMintCredentialFile(dir, area string) string {
	return filepath.Join(dir, area, AreaMintCredentialKey)
}
