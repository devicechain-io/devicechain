// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloadredact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const secret = "fixture-credential-secret-value"

func TestRedactReplacesCredentialValuesAtAnyDepthAndKeepsTheRest(t *testing.T) {
	in := `{"device":"d1","credentialId":"c1","CredentialSecret":"` + secret + `","n":1.50,` +
		`"payload":{"entries":[{"name":"t","value":21.5,"password":"` + secret + `"}],"credential_secret":"` + secret + `"}}`
	out := string(Redact([]byte(in), 0))
	require.NotContains(t, out, secret)
	require.NotContains(t, out, `"c1"`)
	for _, want := range []string{`"device":"d1"`, `"credentialId":"` + Marker + `"`, `"n":1.50`, `"name":"t"`, `"value":21.5`,
		`"CredentialSecret":"` + Marker + `"`, `"password":"` + Marker + `"`, `"credential_secret":"` + Marker + `"`} {
		require.Contains(t, out, want)
	}
	require.True(t, json.Valid([]byte(out)))
}

func TestRedactDoesNotKeepAnEscapedSpellingOfTheKey(t *testing.T) {
	bs := string(rune(92)) // a backslash, so the body carries a real JSON escape
	body := `{"credential` + bs + `u0053ecret":"` + secret + `"}`
	require.Contains(t, body, bs+`u0053`, "the fixture must carry a real JSON escape")
	out := string(Redact([]byte(body), 0))
	require.NotContains(t, out, secret)
}

func TestRedactKeepsNonStringCredentialValuesOut(t *testing.T) {
	out := string(Redact([]byte(`{"token":{"a":"`+secret+`"},"password":12345678}`), 0))
	require.NotContains(t, out, secret)
	require.NotContains(t, out, "12345678")
}

func TestRedactDropsAnUnparseableBodyKeepingLengthAndDigest(t *testing.T) {
	for name, in := range map[string]string{
		"truncated": `{"credentialSecret":"` + secret + `","x":[`,
		"trailing":  `{"a":1} {"credentialSecret":"` + secret + `"}`,
		"not json":  "password=" + secret,
		"binary":    "\x00\x01" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			out := string(Redact([]byte(in), 0))
			require.NotContains(t, out, secret)
			require.Contains(t, out, `"bytes":`)
			require.Contains(t, out, `"sha256":`)
		})
	}
}

func TestRedactBoundsTheResultAfterRedacting(t *testing.T) {
	in := `{"credentialSecret":"` + secret + `","note":"` + strings.Repeat("x", 10_000) + `"}`
	out := Redact([]byte(in), 200)
	require.NotContains(t, string(out), secret)
	require.LessOrEqual(t, len(out), 200+len("...[truncated 99999999 bytes]"))
	require.Contains(t, string(out), "[truncated")
}

func TestRedactLeavesAShortResultUntruncated(t *testing.T) {
	out := string(Redact([]byte(`{"a":1}`), 200))
	require.Equal(t, `{"a":1}`, out)
}
