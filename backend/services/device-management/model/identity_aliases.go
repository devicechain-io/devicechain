// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "github.com/devicechain-io/dc-device-management/model/identity"

// Device identity is being carved out of this package into model/identity, leaf first: the
// trust-anchor and revocation-store ENTITIES have moved (they depend on nothing else here),
// and the code that works on them through *Api has not yet. Everything below re-exports a
// moved name under the name this package has always used for it, so the rest of the service
// reads exactly as before. The types are ALIASES: model.DeviceTrustAnchor and
// identity.DeviceTrustAnchor are one type, with one method set, one table and one encoding.
//
// Nothing here may be a definition: a shape that has moved is defined in identity and only
// named here.

type (
	DeviceTrustAnchor               = identity.DeviceTrustAnchor
	DeviceTrustAnchorCreateRequest  = identity.DeviceTrustAnchorCreateRequest
	DeviceTrustAnchorUpdateRequest  = identity.DeviceTrustAnchorUpdateRequest
	DeviceTrustAnchorSearchCriteria = identity.DeviceTrustAnchorSearchCriteria
	DeviceTrustAnchorSearchResults  = identity.DeviceTrustAnchorSearchResults
	OptionalOcspMode                = identity.OptionalOcspMode
	OptionalRevocationMode          = identity.OptionalRevocationMode

	DeviceRevocationIssuer    = identity.DeviceRevocationIssuer
	DeviceRevocationList      = identity.DeviceRevocationList
	DeviceRevokedCertificate  = identity.DeviceRevokedCertificate
	DeviceCredentialChainLink = identity.DeviceCredentialChainLink
)

const (
	RevocationSourceUpload = identity.RevocationSourceUpload
	RevocationSourceFetch  = identity.RevocationSourceFetch
	RevokedKindCRL         = identity.RevokedKindCRL
	RevokedKindManual      = identity.RevokedKindManual

	DefaultRevocationStaleGrace = identity.DefaultRevocationStaleGrace
	MaxRevocationStaleGrace     = identity.MaxRevocationStaleGrace
)
