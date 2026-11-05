// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package x509trust

import (
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"time"
)

// derOf wraps content in one DER element.
func derOf(class, tag int, compound bool, content ...[]byte) []byte {
	var b []byte
	for _, c := range content {
		b = append(b, c...)
	}
	out, err := asn1.Marshal(asn1.RawValue{Class: class, Tag: tag, IsCompound: compound, Bytes: b})
	if err != nil {
		panic(err)
	}
	return out
}

func seqOf(content ...[]byte) []byte {
	return derOf(asn1.ClassUniversal, asn1.TagSequence, true, content...)
}

func mustMarshal(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func extDER(oid asn1.ObjectIdentifier, critical bool, value []byte) []byte {
	parts := [][]byte{mustMarshal(oid)}
	if critical {
		parts = append(parts, mustMarshal(true))
	}
	return seqOf(append(parts, mustMarshal(value))...)
}

func generalizedTime(t time.Time) []byte {
	b, err := asn1.MarshalWithParams(t.UTC(), "generalized")
	if err != nil {
		panic(err)
	}
	return b
}

// Go's parser leaves bytes unread inside a sequence once it has what it wants. Those bytes are
// inside the signature, so a CA-signed CRL can carry content the extension rules never see.
func TestParseRevocationListRefusesUnreadBytesInsideTheSignedStructure(t *testing.T) {
	root := issue(t, nil, spec{cn: "root", ca: true})
	idpExt := extDER(oidExtIssuingDistPoint, false, []byte{0x30, 0x03, 0x85, 0x01, 0xff})

	if _, err := parseAt(craftCRL(t, goodOpts(root)), nil, root); err != nil {
		t.Fatalf("control: %v", err)
	}
	t.Run("second extensions container carrying an IDP", func(t *testing.T) {
		o := goodOpts(root)
		o.rawTBS = func(c []byte) []byte {
			return append(append([]byte(nil), c...), derOf(asn1.ClassContextSpecific, 0, true, seqOf(idpExt))...)
		}
		_, err := parseAt(craftCRL(t, o), nil, root)
		wantRule(t, err, RuleMalformed)
	})
	t.Run("trailing field after the TBS extensions", func(t *testing.T) {
		o := goodOpts(root)
		o.rawTBS = func(c []byte) []byte { return append(append([]byte(nil), c...), mustMarshal(5)...) }
		_, err := parseAt(craftCRL(t, o), nil, root)
		wantRule(t, err, RuleMalformed)
	})
	t.Run("CRL number with trailing bytes in its value", func(t *testing.T) {
		o := goodOpts(root)
		o.noNumber = true
		o.exts = []pkix.Extension{{Id: oidExtCRLNumber, Value: append(mustMarshal(big.NewInt(3)), 0x05, 0x00)}}
		_, err := parseAt(craftCRL(t, o), nil, root)
		wantRule(t, err, RuleBadCRLNumber)
	})
	t.Run("reasonCode with trailing bytes in its value", func(t *testing.T) {
		o := goodOpts(root)
		o.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: oidExtReasonCode, Value: append(mustMarshal(asn1.Enumerated(1)), 0x05, 0x00)})}
		_, err := parseAt(craftCRL(t, o), nil, root)
		wantRule(t, err, RuleMalformed)
	})
	t.Run("invalidityDate", func(t *testing.T) {
		gt := generalizedTime(testNow.Add(-2 * time.Hour))
		ok := goodOpts(root)
		ok.entries = []pkix.RevokedCertificate{entry(3, pkix.Extension{Id: oidExtInvalidityDate, Value: gt})}
		if _, err := parseAt(craftCRL(t, ok), nil, root); err != nil {
			t.Fatalf("control (a well-formed, non-critical invalidityDate): %v", err)
		}
		for name, ex := range map[string]struct {
			e    pkix.Extension
			rule ListRule
		}{
			"critical":         {pkix.Extension{Id: oidExtInvalidityDate, Critical: true, Value: gt}, RuleEntryCriticalExt},
			"not a time":       {pkix.Extension{Id: oidExtInvalidityDate, Value: []byte{0x05, 0x00}}, RuleMalformed},
			"trailing bytes":   {pkix.Extension{Id: oidExtInvalidityDate, Value: append(append([]byte(nil), gt...), 0x05, 0x00)}, RuleMalformed},
			"critical garbage": {pkix.Extension{Id: oidExtInvalidityDate, Critical: true, Value: []byte{0x05, 0x00}}, RuleEntryCriticalExt},
		} {
			o := goodOpts(root)
			o.entries = []pkix.RevokedCertificate{entry(3, ex.e)}
			_, err := parseAt(craftCRL(t, o), nil, root)
			if ListRuleOf(err) != ex.rule {
				t.Errorf("%s: %v, want %s", name, err, ex.rule)
			}
		}
	})
	t.Run("PEM: a malformed first block then a valid one", func(t *testing.T) {
		good := craftCRL(t, goodOpts(root))
		bad := []byte("-----BEGIN X509 CRL-----\n!!!notbase64!!!\n-----END X509 CRL-----\n")
		_, err := parseAt(append(append([]byte(nil), bad...), pemCRL(good)...), nil, root)
		wantRule(t, err, RuleMalformed)
		wrong := []byte("-----BEGIN GARBAGE-----\nAAAA\n-----END GARBAGE-----\n")
		_, err = parseAt(append(append([]byte(nil), wrong...), pemCRL(good)...), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(append([]byte("-----BEGIN X509 CRL-----\n"), pemCRL(good)...), nil, root)
		wantRule(t, err, RuleMalformed)
		_, err = parseAt(append([]byte("preamble text\n"), pemCRL(good)...), nil, root)
		wantRule(t, err, RuleMalformed)
		if _, err := parseAt(pemCRL(good), nil, root); err != nil {
			t.Fatalf("control: %v", err)
		}
	})
}

// The structure walk, alone, on hand-built DER: every constructed element must be consumed.
func TestStrictCRLStructure(t *testing.T) {
	alg := seqOf(mustMarshal(oidECDSAWithSHA256))
	tm := mustMarshal(testNow.UTC())
	ext := extDER(oidExtCRLNumber, false, mustMarshal(big.NewInt(1)))
	entryOf := func(extra ...[]byte) []byte {
		return seqOf(append([][]byte{mustMarshal(big.NewInt(7)), tm}, extra...)...)
	}
	build := func(tbsExtra [][]byte, entries []byte, exts []byte, topExtra []byte) []byte {
		parts := [][]byte{mustMarshal(1), alg, seqOf(), tm, tm}
		if entries != nil {
			parts = append(parts, seqOf(entries))
		}
		if exts != nil {
			parts = append(parts, derOf(asn1.ClassContextSpecific, 0, true, seqOf(exts)))
		}
		parts = append(parts, tbsExtra...)
		sig := mustMarshal(asn1.BitString{Bytes: []byte{1, 2}, BitLength: 16})
		return seqOf(seqOf(parts...), alg, sig, topExtra)
	}
	if !strictCRLStructure(build(nil, entryOf(), ext, nil)) {
		t.Fatal("control: a well-formed structure was refused")
	}
	if !strictCRLStructure(build(nil, nil, nil, nil)) {
		t.Fatal("control: no entries and no extensions is well-formed")
	}
	for name, der := range map[string][]byte{
		"trailing top-level element":   build(nil, entryOf(), ext, mustMarshal(1)),
		"trailing TBS field":           build([][]byte{mustMarshal(1)}, entryOf(), ext, nil),
		"second extensions container":  build([][]byte{derOf(asn1.ClassContextSpecific, 0, true, seqOf(ext))}, entryOf(), ext, nil),
		"entry with four parts":        build(nil, entryOf(seqOf(ext), mustMarshal(1)), ext, nil),
		"entry extension not sequence": build(nil, entryOf(mustMarshal(1)), ext, nil),
		"extension with a fourth part": build(nil, entryOf(), seqOf(mustMarshal(oidExtCRLNumber), mustMarshal(false), mustMarshal([]byte{1}), mustMarshal(1)), nil),
		"extension without a value":    build(nil, entryOf(), seqOf(mustMarshal(oidExtCRLNumber)), nil),
		"extension oid not an OID":     build(nil, entryOf(), seqOf(mustMarshal(1), mustMarshal([]byte{1})), nil),
		"empty input":                  nil,
		"not a sequence":               mustMarshal(1),
	} {
		if strictCRLStructure(der) {
			t.Errorf("%s: accepted", name)
		}
	}
}
