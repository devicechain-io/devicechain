// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"net/url"
	"strings"
	"testing"
)

// 🔴 EVERY PASSWORD HERE IS ONE THIS CHECK WILL SEE THE MOMENT dcctl MINTS ITS OWN.
// Today the value is a constant with no syntax byte in it, which is the only reason
// the previous Sprintf form was never caught: it produced a well-formed URL for the
// one input it was ever given. The property being pinned is not "the string looks
// right" but "the credential survives the round trip", because a URL that parses and
// yields the WRONG password is the failure mode — it reports an authentication error
// against a working database.
func TestTheVerifierDSNSurvivesEveryCredentialByte(t *testing.T) {
	cases := []struct {
		name string
		user string
		pass string
		db   string
	}{
		{"an at sign ends the userinfo early", "app", "pa@ss", "devicechain"},
		{"a slash opens the path", "app", "pa/ss", "devicechain"},
		{"a question mark opens the query", "app", "pa?ss", "devicechain"},
		{"a hash opens the fragment", "app", "pa#ss", "devicechain"},
		{"a percent is an escape introducer", "app", "pa%ss", "devicechain"},
		{"a colon splits user from password", "app", "pa:ss", "devicechain"},
		{"the user is equally able to be syntax", "ap@p", "secret", "devicechain"},
		{"the database name reaches the path", "app", "secret", "dc/db?x"},
		{"base64 padding and slashes", "app", "aG9sZA==/+x", "devicechain"},
		{"every one of them at once", "a@b/c", "p@s/s?w#o%r:d", "d@b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := localPostgresURL(tc.user, tc.pass, 15432, tc.db)

			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("the DSN is not a parseable URL: %v\n%s", err, dsn)
			}
			if u.User == nil {
				t.Fatalf("the DSN carries no credentials at all:\n%s", dsn)
			}
			if got := u.User.Username(); got != tc.user {
				t.Errorf("username round-tripped as %q, want %q\n%s", got, tc.user, dsn)
			}
			got, set := u.User.Password()
			if !set {
				t.Fatalf("the DSN carries no password:\n%s", dsn)
			}
			if got != tc.pass {
				t.Errorf("password round-tripped as %q, want %q — this is the shape that "+
					"reports an auth failure against a working database\n%s", got, tc.pass, dsn)
			}
			if want := "/" + tc.db; u.Path != want {
				t.Errorf("database round-tripped as %q, want %q\n%s", u.Path, want, dsn)
			}
			if u.Host != "127.0.0.1:15432" {
				t.Errorf("host is %q; a credential byte has escaped into it\n%s", u.Host, dsn)
			}
			if u.Query().Get("sslmode") != "disable" {
				t.Errorf("sslmode did not survive: %q\n%s", u.Query().Get("sslmode"), dsn)
			}
		})
	}
}

// The counterweight: an ordinary credential must still produce the plain DSN it
// always did, so the fix cannot be "escape everything into something pgx rejects".
func TestAnOrdinaryCredentialIsUnchanged(t *testing.T) {
	dsn := localPostgresURL("devicechain", "devicechain", 15432, "devicechain")
	const want = "postgres://devicechain:devicechain@127.0.0.1:15432/devicechain?sslmode=disable"
	if dsn != want {
		t.Fatalf("the everyday DSN changed shape:\n got %s\nwant %s", dsn, want)
	}
	if strings.Contains(dsn, "%") {
		t.Errorf("an unremarkable credential came back percent-encoded:\n%s", dsn)
	}
}
