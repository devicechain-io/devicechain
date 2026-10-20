// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloadredact

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// encoding/json matches a key to a field under Unicode simple case folding, so the long s
// (U+017F) and the Kelvin sign (U+212A) spell the same field as their ASCII letters. The
// keys below are JSON escapes in a raw string, so the body really carries the escape.
func TestRedactFoldsKeysAsEncodingJSONDoes(t *testing.T) {
	bs := string(rune(92)) // a backslash: the keys below are real JSON escapes
	var env struct {
		CredentialSecret string `json:"credentialSecret"`
		APIKey           string `json:"apikey"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"credential`+bs+`u017fecret":"a","api`+bs+`u212aey":"b"}`), &env))
	require.Equal(t, "a", env.CredentialSecret, "premise: the decoder folds the long s")
	require.Equal(t, "b", env.APIKey, "premise: the decoder folds the Kelvin sign")

	for _, key := range []string{`credential` + bs + `u017fecret`, `api` + bs + `u212aey`, `API` + bs + `u212aEY`, `CREDENTIAL` + bs + `u017fECRET`, `x-api` + bs + `u212aey`} {
		out := string(Redact([]byte(`{"`+key+`":"`+secret+`","keep":"v"}`), 0))
		require.NotContains(t, out, secret, key)
		require.Contains(t, out, `"keep":"v"`, key)
	}
}

func TestRedactHyphenAndExtraKeyNames(t *testing.T) {
	for _, key := range []string{"x-api-key", "X_Api_Key", "api-key", "pwd", "pass", "credential", "credentials", "private-key"} {
		out := string(Redact([]byte(`{"`+key+`":{"inner":"`+secret+`"}}`), 0))
		require.NotContains(t, out, secret, key)
		require.Contains(t, out, Marker, key)
	}
	out := string(Redact([]byte(`{"credential-secret":"`+secret+`","credentialType":"keep"}`), 0))
	require.NotContains(t, out, secret)
	require.Contains(t, out, `"credentialType":"keep"`)
}
