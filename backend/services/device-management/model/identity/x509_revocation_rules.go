// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

// The pure rules of the x509 revocation store: its fixed-configuration limits, a list's
// freshness, the mode, grace and reason vocabularies, and the checks a manual revoke makes before
// anything is read. Nothing here touches the database; the store itself is model's.

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/x509trust"
)

// RevocationLimits are the fixed-configuration bounds of the store (deviceManagement.callout.
// revocation). Zero fields are not "unbounded": SetRevocationLimits refuses them.
type RevocationLimits struct {
	// MaxCrlBytes bounds a CRL as submitted (PEM or DER), checked before any parsing.
	MaxCrlBytes int
	// MaxCrlEntries bounds the revoked certificates one CRL lists.
	MaxCrlEntries int
	// MaxListsPerAnchor bounds the current lists one trust anchor holds.
	MaxListsPerAnchor int
	// MaxManualEntriesPerTenant bounds a tenant's manual revocations.
	MaxManualEntriesPerTenant int
	// DefaultStaleGrace is the grace an anchor that sets none gets.
	DefaultStaleGrace time.Duration
	// MaxGraphqlCrlBytes bounds the CRL the GraphQL mutation accepts, as DER; the mutation's PEM
	// text is held to GraphqlCrlTextBound of it.
	MaxGraphqlCrlBytes int
	// MaxConcurrentWrites bounds the CRL uploads one replica works on at once.
	MaxConcurrentWrites int
}

// DefaultRevocationLimits are the limits an Api runs with until SetRevocationLimits says otherwise;
// they equal the configuration's defaults.
func DefaultRevocationLimits() RevocationLimits {
	return RevocationLimits{
		MaxCrlBytes:               2 << 20,
		MaxCrlEntries:             100_000,
		MaxListsPerAnchor:         16,
		MaxManualEntriesPerTenant: MaxManualRevocationsPerTenant,
		DefaultStaleGrace:         DefaultRevocationStaleGrace,
		MaxGraphqlCrlBytes:        2 << 20,
		MaxConcurrentWrites:       1,
	}
}

// ListRefused is the refusal of a CRL the store (as opposed to the parser) will not take.
func ListRefused(rule string, format string, args ...any) error {
	msg := "revocation list refused: " + rule
	if format != "" {
		msg += " (" + fmt.Sprintf(format, args...) + ")"
	}
	return InvalidAnchor("%s", msg)
}

// Freshness of a stored list, worst first in severity order for an anchor.
const (
	RevocationStatusNone     = "NONE"
	RevocationStatusFresh    = "FRESH"
	RevocationStatusExpiring = "EXPIRING"
	RevocationStatusStale    = "STALE"
)

// RevocationListStatus is a list's freshness at now. STALE means the list is past nextUpdate plus
// the grace (the instant REQUIRE_CURRENT stops counting it as current); EXPIRING means three
// quarters of its validity have passed (the instant the operator should have pushed the next one);
// otherwise FRESH.
func RevocationListStatus(l *DeviceRevocationList, grace time.Duration, now time.Time) string {
	if now.After(l.NextUpdate.Add(grace)) {
		return RevocationStatusStale
	}
	validity := l.NextUpdate.Sub(l.ThisUpdate)
	if validity > 0 && !now.Before(l.ThisUpdate.Add(validity/4*3)) {
		return RevocationStatusExpiring
	}
	return RevocationStatusFresh
}

// RevocationAnchorStatus is the worst status over the anchor's lists, NONE when it has none.
func RevocationAnchorStatus(lists []DeviceRevocationList, grace time.Duration, now time.Time) string {
	rank := map[string]int{RevocationStatusNone: 0, RevocationStatusFresh: 1, RevocationStatusExpiring: 2, RevocationStatusStale: 3}
	worst := RevocationStatusNone
	for i := range lists {
		if s := RevocationListStatus(&lists[i], grace, now); rank[s] > rank[worst] {
			worst = s
		}
	}
	return worst
}

// RevocationModeFromEnum is the stored mode for a GraphQL enum name.
func RevocationModeFromEnum(name string) (x509trust.Mode, bool) {
	switch m := x509trust.Mode(name); m {
	case x509trust.ModeEntries, x509trust.ModeRequireCurrent:
		return m, true
	}
	return "", false
}

// ParseRevocationGrace reads a Go duration for an anchor's grace: whole seconds, 0 to the maximum.
func ParseRevocationGrace(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, InvalidAnchor("revocationStaleGrace must be a duration such as \"24h\" or \"90m\"")
	}
	if d < 0 || d > MaxRevocationStaleGrace || d%time.Second != 0 {
		return 0, InvalidAnchor("revocationStaleGrace must be a whole number of seconds between 0s and %s", MaxRevocationStaleGrace)
	}
	return d, nil
}

// CrlTooLargeRefusal is the refusal of a CRL over a size bound, for a door that checks the size
// before it hands the CRL on.
func CrlTooLargeRefusal(bound int) error {
	return ListRefused(string(x509trust.RuleTooLarge), "at most %d bytes", bound)
}

// MaxManualRevocationsPerTenant is the default bound on the manual revocation entries one tenant
// holds (RevocationLimits.MaxManualEntriesPerTenant, fixed configuration
// callout.revocation.maxManualEntriesPerTenant).
const MaxManualRevocationsPerTenant = 100_000

// DeviceCertificateRevokeRequest revokes one certificate by DeviceChain's own word.
type DeviceCertificateRevokeRequest struct {
	AnchorToken string
	// CertificatePem or FingerprintSha256, exactly one. With a certificate the fingerprint is
	// computed. The certificate may be a leaf or an intermediate, registered or not.
	CertificatePem    *string
	FingerprintSha256 *string
	// Reason is the GraphQL enum name; nil is UNSPECIFIED.
	Reason *string
}

// DeviceRevokedCertificateSearchCriteria is the search of a revocation store's entries.
type DeviceRevokedCertificateSearchCriteria struct {
	rdb.Pagination
	// Kind restricts to MANUAL or CRL entries.
	Kind *string
}

// DeviceRevokedCertificateSearchResults is a page of revoked certificates.
type DeviceRevokedCertificateSearchResults struct {
	Results    []DeviceRevokedCertificate
	Pagination rdb.SearchResultsPagination
}

var revocationReasonNames = map[string]x509trust.RevocationReason{
	"UNSPECIFIED":            x509trust.RevocationUnspecified,
	"KEY_COMPROMISE":         x509trust.RevocationKeyCompromise,
	"CA_COMPROMISE":          x509trust.RevocationCACompromise,
	"AFFILIATION_CHANGED":    x509trust.RevocationAffiliationChanged,
	"SUPERSEDED":             x509trust.RevocationSuperseded,
	"CESSATION_OF_OPERATION": x509trust.RevocationCessationOfOperation,
	"CERTIFICATE_HOLD":       x509trust.RevocationCertificateHold,
	"PRIVILEGE_WITHDRAWN":    x509trust.RevocationPrivilegeWithdrawn,
	"AA_COMPROMISE":          x509trust.RevocationAACompromise,
}

// RevocationReasonFromEnum maps the GraphQL enum name to the stored reason; ok is false for a
// name outside the vocabulary.
func RevocationReasonFromEnum(name string) (x509trust.RevocationReason, bool) {
	r, ok := revocationReasonNames[name]
	return r, ok
}

// RevocationReasonEnum is the GraphQL enum name of a stored reason. A stored value outside the
// vocabulary reads as UNSPECIFIED rather than failing a listing.
func RevocationReasonEnum(reason string) string {
	for name, r := range revocationReasonNames {
		if string(r) == reason {
			return name
		}
	}
	return "UNSPECIFIED"
}

// ValidFingerprint is whether s is the lowercase hex of a SHA-256.
func ValidFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// RevokeFingerprint is the fingerprint a revoke names: exactly one of a certificate and a
// fingerprint, the certificate parsed and hashed.
func RevokeFingerprint(req *DeviceCertificateRevokeRequest) (string, error) {
	hasCert := req.CertificatePem != nil && strings.TrimSpace(*req.CertificatePem) != ""
	hasFp := req.FingerprintSha256 != nil && strings.TrimSpace(*req.FingerprintSha256) != ""
	switch {
	case hasCert == hasFp:
		return "", InvalidAnchor("name the certificate to revoke with exactly one of certificatePem and fingerprintSha256")
	case hasFp:
		fp := strings.ToLower(strings.TrimSpace(*req.FingerprintSha256))
		if !ValidFingerprint(fp) {
			return "", InvalidAnchor("fingerprintSha256 must be 64 hexadecimal characters")
		}
		return fp, nil
	}
	_, certs, err := x509trust.ParseCertificates(*req.CertificatePem)
	if err != nil {
		return "", InvalidAnchor("certificatePem: %v", err)
	}
	if len(certs) != 1 {
		return "", InvalidAnchor("certificatePem must hold exactly one certificate")
	}
	return x509trust.Fingerprint(certs[0]), nil
}

// RevokesACA is whether the revoke names a certificate (PEM) that is a CA. A bare fingerprint says
// nothing about what it fingerprints, so it is not.
func RevokesACA(req *DeviceCertificateRevokeRequest) bool {
	if req.CertificatePem == nil || strings.TrimSpace(*req.CertificatePem) == "" {
		return false
	}
	_, certs, err := x509trust.ParseCertificates(*req.CertificatePem)
	return err == nil && len(certs) == 1 && certs[0].BasicConstraintsValid && certs[0].IsCA
}
