// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

// The pure checks of the X.509 surface: the INVALID_VALUE refusal every anchor, certificate
// credential and certificate profile write uses, the refusals of certificate fields where they do
// not apply, the display of a stored certificate, and the chain links of a verified path. None
// reads the database.

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/x509trust"
	"gorm.io/gorm"
)

// InvalidAnchor refuses an X.509 request that cannot be honoured (a trust anchor, a certificate
// credential, a certificate profile). INVALID_VALUE.
func InvalidAnchor(format string, args ...any) error {
	return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
}

// ReferenceToNotFound turns the foreign-key refusal of an insert that named a trust anchor
// deleted since it was read into the NOT_FOUND the caller would have had a moment later.
func ReferenceToNotFound(err error, anchorToken string) error {
	if class, ok := integrity.Refused(err); ok && class == integrity.ClassReference {
		return fmt.Errorf("trust anchor %q: %w", anchorToken, gorm.ErrRecordNotFound)
	}
	return err
}

// RequireUsableAnchor is the check a write that BINDS something to an anchor makes: the anchor
// exists in the tenant and is enabled (binding to a withdrawn CA would store a credential that
// can never authenticate).
func RequireUsableAnchor(anchor *DeviceTrustAnchor) error {
	if !anchor.Enabled {
		return InvalidAnchor("trust anchor %q is disabled", anchor.Token)
	}
	return nil
}

// TrimmedToken is a token argument with surrounding blanks removed; "" means none.
func TrimmedToken(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// RefuseX509Elsewhere is the answer of every path that would build a credential from a request
// without resolving a trust anchor (a device replacement, an import row): a certificate
// credential's chain is checked at create, against the anchor, and only CreateDeviceCredential
// does that.
func RefuseX509Elsewhere() error {
	return InvalidAnchor("%s credentials are created with createDeviceCredential, which checks the "+
		"certificate's chain against a trust anchor; add it to the device afterwards", CredentialX509Certificate)
}

// RefuseX509Fields refuses the certificate-only fields of a create for any other type, so a
// certificate sent with an ACCESS_TOKEN is an error and not a silently ignored value.
func RefuseX509Fields(request *DeviceCredentialCreateRequest) error {
	if TrimmedToken(request.TrustAnchorToken) != "" || (request.CertificatePem != nil && *request.CertificatePem != "") {
		return InvalidAnchor("certificatePem and trustAnchorToken apply only to %s credentials", CredentialX509Certificate)
	}
	return nil
}

// X509CertificateInfo describes the pinned leaf of a certificate credential for display. Every
// field is read back from the stored certificate; none is trusted for authentication.
type X509CertificateInfo struct {
	FingerprintSha256 string
	SubjectCn         string
	SerialHex         string
	NotAfter          time.Time
	TrustAnchorId     uint
}

// X509Info parses the stored leaf of a certificate credential. It is nil for any other type, and
// for a row whose certificate is absent or unreadable (a stored defect is shown as no detail,
// never as a made-up one).
func (c *DeviceCredential) X509Info() *X509CertificateInfo {
	if CredentialType(c.CredentialType) != CredentialX509Certificate || !c.CertificatePem.Valid {
		return nil
	}
	block, _ := pem.Decode([]byte(c.CertificatePem.String))
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	info := &X509CertificateInfo{
		FingerprintSha256: x509trust.Fingerprint(cert),
		SubjectCn:         cert.Subject.CommonName,
		SerialHex:         fmt.Sprintf("%x", cert.SerialNumber),
		NotAfter:          cert.NotAfter.UTC(),
	}
	if c.TrustAnchorId != nil {
		info.TrustAnchorId = *c.TrustAnchorId
	}
	return info
}

// ChainLinksOf builds the links of a credential from the verified path it was created through.
// The tenant is stamped by the create callback from the context, as for every tenant-scoped row.
func ChainLinksOf(credentialId, anchorId uint, chain []*x509.Certificate) ([]DeviceCredentialChainLink, error) {
	path, ok := x509trust.PathLinks(chain)
	if !ok {
		return nil, errors.New("a credential's chain links need a verified path")
	}
	out := make([]DeviceCredentialChainLink, 0, len(path))
	for depth, l := range path {
		if !l.SerialOK {
			// A serial no CRL can name (zero or negative) has no link to match on; the
			// fingerprint-keyed manual entries still apply to the certificate itself.
			continue
		}
		key := l.Issuer.Key
		out = append(out, DeviceCredentialChainLink{
			CredentialId: credentialId, Depth: int16(depth), TrustAnchorId: anchorId,
			Fingerprint: l.Fingerprint, IssuerKey: &key, IssuerName: l.Issuer.Name, Serial: l.Serial,
		})
	}
	return out, nil
}
