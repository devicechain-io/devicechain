// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package payloadredact prepares a device-supplied message body for a place that keeps it
// for diagnosis: the failed-decode stream and the dead-letter archive.
//
// A device's message may carry the credential it authenticates with. That credential is
// live authentication material only for as long as the message is being authenticated, so
// anything that keeps the body for later reading keeps it through Redact first. The
// guarantee is structural rather than best-effort: a body that can be read as JSON comes
// back with the value of every credential-named field replaced and everything else intact,
// and a body that cannot be read as JSON comes back as a fixed-size description (length and
// digest) with none of its content, so there is no spelling of a credential that survives.
package payloadredact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Marker replaces the value of every credential-named field.
const Marker = "[redacted]"

// credentialKeys are the field names whose values are credential material, compared after
// lower-casing and dropping '_' and '-' (so credentialSecret, credential_secret and
// CredentialSecret are one name). The device envelope's own field is credentialSecret; the
// rest are the names a device or gateway commonly uses for the same thing.
var credentialKeys = map[string]struct{}{
	"credentialsecret": {},
	"secret":           {},
	"clientsecret":     {},
	"password":         {},
	"passwd":           {},
	"passphrase":       {},
	"token":            {},
	"accesstoken":      {},
	"refreshtoken":     {},
	"authtoken":        {},
	"bearer":           {},
	"authorization":    {},
	"apikey":           {},
	"privatekey":       {},
	"psk":              {},
	"presharedkey":     {},
}

func isCredentialKey(k string) bool {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "")
	k = strings.ReplaceAll(k, "-", "")
	_, ok := credentialKeys[k]
	return ok
}

// Redact returns raw with credential material removed, for storage.
//
// A JSON body keeps every field name and every non-credential value; only the values of
// credential-named fields (at any depth) become Marker. The result is re-encoded, so key
// order and insignificant whitespace are not preserved. If max > 0 and the result is
// longer, it is cut to max bytes and suffixed with a note of how much was dropped; the cut
// happens after redaction, so it cannot expose anything. A max <= 0 means no bound.
//
// A body that is not exactly one JSON value is not kept: the result is a small JSON
// object giving its length and SHA-256, which still identifies the message against the
// capture it came from.
func Redact(raw []byte, max int) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || trailing(dec) {
		return opaque(raw)
	}
	out, err := encode(scrub(v))
	if err != nil {
		return opaque(raw)
	}
	if max > 0 && len(out) > max {
		note := fmt.Sprintf("...[truncated %d bytes]", len(out)-max)
		out = append(out[:max:max], note...)
	}
	return out
}

// trailing reports whether anything but whitespace follows the first JSON value.
func trailing(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err != io.EOF
}

func scrub(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isCredentialKey(k) {
				t[k] = Marker
				continue
			}
			t[k] = scrub(child)
		}
	case []any:
		for i, child := range t {
			t[i] = scrub(child)
		}
	}
	return v
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func opaque(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return []byte(fmt.Sprintf(`{"redacted":"not json","bytes":%d,"sha256":%q}`, len(raw), hex.EncodeToString(sum[:])))
}
