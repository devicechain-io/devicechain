// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509"
	"fmt"
	"time"
)

// VerifyCAChain is the CA-certificate twin of VerifyClientChain, for the upload path: chain[0]
// must itself BE a CA that may sign CRLs (cRLSign), and chain (that CA first, then the CAs
// linking it to the anchor) must build to anchor at now. VerifyClientChain cannot be reused for
// this: it refuses a CA leaf and demands clientAuth.
//
// The same rules apply as for a device chain: at most MaxPresentedCerts certificates and
// MaxPresentedDERBytes of DER, a valid anchor, no SHA-1 (or weaker) signature and no key outside
// the key policy on any presented certificate, no name constraints on any CA (the anchor
// included), the NotBeforeSkew allowance for a freshly issued CA, and the verifier's own path
// building (signatures, validity, basicConstraints, path length). No usage is demanded of the
// path beyond cRLSign on chain[0]: a CA's extended key usage says nothing about the CRLs it signs.
//
// Pure, and not reachable from the connect path: it is for the operator's upload.
func VerifyCAChain(anchor *x509.Certificate, chain []*x509.Certificate, now time.Time) error {
	if len(chain) == 0 {
		return refuse(ReasonNoCertificate, "")
	}
	if len(chain) > MaxPresentedCerts {
		return refuse(ReasonTooLong, fmt.Sprintf("more than %d certificates", MaxPresentedCerts))
	}
	size := 0
	for _, c := range chain {
		if c == nil {
			return refuse(ReasonMalformed, "")
		}
		size += len(c.Raw)
	}
	if size > MaxPresentedDERBytes {
		return refuse(ReasonTooLarge, "")
	}
	if anchor == nil {
		return refuse(ReasonAnchorInvalid, "no anchor")
	}
	if hasNameConstraints(anchor) {
		return refuse(ReasonNameConstraints, "")
	}
	for _, c := range chain {
		if hasNameConstraints(c) {
			return refuse(ReasonNameConstraints, "")
		}
	}
	if err := CheckAnchor(anchor, now); err != nil {
		return refuse(ReasonAnchorInvalid, "")
	}

	signer := chain[0]
	if !signer.BasicConstraintsValid || !signer.IsCA {
		return refuse(ReasonNotCA, "")
	}
	// An absent keyUsage extension is not "any": the signer must say it may sign CRLs.
	if signer.KeyUsage&x509.KeyUsageCRLSign == 0 {
		return refuse(ReasonNoCRLSign, "")
	}
	for _, c := range chain {
		if weakSignature(c) {
			return refuse(ReasonWeakSignature, "")
		}
		if err := CheckPublicKey(c.PublicKey); err != nil {
			return refuse(ReasonKeyPolicy, "")
		}
	}

	at := now
	for _, c := range []*x509.Certificate{signer, anchor} {
		if c.NotBefore.After(at) && c.NotBefore.Sub(now) <= NotBeforeSkew {
			at = c.NotBefore
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(anchor)
	inters := x509.NewCertPool()
	for _, c := range chain[1:] {
		inters.AddCert(c)
	}
	if _, err := signer.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return refuse(classify(err, at), "")
	}
	return nil
}
