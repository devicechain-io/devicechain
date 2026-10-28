// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"strings"
	"testing"
)

// The PEM parsers read text an administrator (or a device's TLS peer) controls. These fuzz
// tests state what must hold for ANY input; under plain `go test` they run the seeds, and
// fuzz-nightly runs the search.

func addSeeds(f *testing.F) {
	ca := issue(f, nil, spec{cn: "seed root", ca: true})
	leaf := issue(f, ca, spec{cn: "seed device"})
	for _, s := range []string{
		"",
		"hello",
		ca.pem(),
		ca.pem() + leaf.pem(),
		ca.pem() + ca.pem(),
		"-----BEGIN CERTIFICATE-----\n!!!\n-----END CERTIFICATE-----\n",
		"-----BEGIN CERTIFICATE-----\nMIIB\n",
		ca.pem() + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"-----BEGIN CERTIFICATE-----\nProc-Type: 4,ENCRYPTED\n\nAAAA\n-----END CERTIFICATE-----\n",
		strings.Repeat("-----BEGIN ", 100),
	} {
		f.Add(s)
	}
}

func FuzzParseCaBundle(f *testing.F) {
	addSeeds(f)
	f.Fuzz(func(t *testing.T, text string) {
		canon, certs, err := ParseCaBundle(text)
		if err != nil {
			if canon != "" || certs != nil {
				t.Fatalf("a refusal returned data")
			}
			// Errors are fixed templates; one that grows with the input is echoing it.
			if len(err.Error()) > 300 {
				t.Fatalf("the error is %d bytes long and may be echoing the input", len(err.Error()))
			}
			return
		}
		if strings.Contains(strings.ToUpper(text), "PRIVATE KEY") {
			t.Fatalf("accepted text containing a private key marker")
		}
		if len(certs) > MaxBundleCerts || len(text) > MaxBundleBytes {
			t.Fatalf("accepted past a bound: %d certificates, %d bytes", len(certs), len(text))
		}
		// The canonical form is a fixed point and describes the same certificates.
		canon2, certs2, err := ParseCaBundle(canon)
		if err != nil || canon2 != canon || len(certs2) != len(certs) {
			t.Fatalf("canonical form is not stable: %v", err)
		}
		for i := range certs {
			if certs[i] != certs2[i] {
				t.Fatalf("certificate %d changed on re-parse", i)
			}
		}
		if (canon == "") != (len(certs) == 0) {
			t.Fatalf("canonical text and certificate list disagree")
		}
	})
}

func FuzzParseTrustAnchor(f *testing.F) {
	addSeeds(f)
	f.Fuzz(func(t *testing.T, text string) {
		a, err := ParseTrustAnchor(text, testNow)
		if err != nil {
			if a != nil {
				t.Fatalf("a refusal returned an anchor")
			}
			return
		}
		// Whatever is accepted is exactly one CA certificate that passes the anchor rules.
		if err := CheckAnchor(a.Certificate, testNow); err != nil {
			t.Fatalf("accepted an anchor CheckAnchor refuses: %v", err)
		}
		if _, certs, err := ParseCaBundle(a.PEM); err != nil || len(certs) != 1 {
			t.Fatalf("stored PEM does not re-parse to one certificate: %v", err)
		}
	})
}

func FuzzParseChainPEM(f *testing.F) {
	addSeeds(f)
	f.Fuzz(func(t *testing.T, a string) {
		// Split on the PEM boundary so the fuzzer can shape the list as well as the entries.
		parts := strings.SplitAfter(a, "-----END CERTIFICATE-----")
		certs, err := ParseChainPEM(parts)
		if err != nil {
			if certs != nil || ReasonOf(err) == "" {
				t.Fatalf("a refusal must return nothing and carry a reason: %v", err)
			}
			return
		}
		if len(certs) == 0 || len(certs) > MaxPresentedCerts {
			t.Fatalf("accepted %d certificates", len(certs))
		}
	})
}
