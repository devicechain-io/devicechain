// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import "github.com/devicechain-io/dc-device-management/model/identity"

// Device identity is being carved out of this package into model/identity, leaf first: the
// trust-anchor and revocation-store ENTITIES have moved (they depend on nothing else here),
// as have the credential vocabulary and requests and the pure x509 helpers, and the code that works on them through
// *Api has not yet. The credential ROW has moved too, but model.DeviceCredential is a
// definition, not an alias: it adds the device association (see model_device_credentials.go). Everything below re-exports a
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

	CredentialType                 = identity.CredentialType
	DeviceCredentialCreateRequest  = identity.DeviceCredentialCreateRequest
	DeviceCredentialUpdateRequest  = identity.DeviceCredentialUpdateRequest
	DeviceCredentialSearchCriteria = identity.DeviceCredentialSearchCriteria

	X509CertificateInfo                    = identity.X509CertificateInfo
	OcspPolicy                             = identity.OcspPolicy
	RevocationLimits                       = identity.RevocationLimits
	RevocationListPutResult                = identity.RevocationListPutResult
	DeviceCertificateRevokeRequest         = identity.DeviceCertificateRevokeRequest
	DeviceRevokedCertificateSearchCriteria = identity.DeviceRevokedCertificateSearchCriteria
	DeviceRevokedCertificateSearchResults  = identity.DeviceRevokedCertificateSearchResults
)

const (
	RevocationSourceUpload = identity.RevocationSourceUpload
	RevocationSourceFetch  = identity.RevocationSourceFetch
	RevokedKindCRL         = identity.RevokedKindCRL
	RevokedKindManual      = identity.RevokedKindManual

	DefaultRevocationStaleGrace = identity.DefaultRevocationStaleGrace
	MaxRevocationStaleGrace     = identity.MaxRevocationStaleGrace

	CredentialAccessToken     = identity.CredentialAccessToken
	CredentialMqttBasic       = identity.CredentialMqttBasic
	CredentialX509Certificate = identity.CredentialX509Certificate

	MaxOcspResponderUrlLength     = identity.MaxOcspResponderUrlLength
	MaxManualRevocationsPerTenant = identity.MaxManualRevocationsPerTenant

	RevocationStatusNone     = identity.RevocationStatusNone
	RevocationStatusFresh    = identity.RevocationStatusFresh
	RevocationStatusExpiring = identity.RevocationStatusExpiring
	RevocationStatusStale    = identity.RevocationStatusStale

	RuleCrlNumberNotNewer = identity.RuleCrlNumberNotNewer
	RuleConflictingIssuer = identity.RuleConflictingIssuer
	RuleTooManyIssuers    = identity.RuleTooManyIssuers
)

// The pure x509 helpers that callers outside this package use. Each is the moved function
// itself, named here; none may be reassigned.
var (
	DefaultRevocationLimits  = identity.DefaultRevocationLimits
	RevocationListStatus     = identity.RevocationListStatus
	RevocationAnchorStatus   = identity.RevocationAnchorStatus
	CrlTooLargeRefusal       = identity.CrlTooLargeRefusal
	RevocationReasonFromEnum = identity.RevocationReasonFromEnum
	RevocationReasonEnum     = identity.RevocationReasonEnum
)
