// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// These exist because a vacuity audit found that the sslMode knob was pinned
// only at the pure function. Reverting EITHER connection-string builder to a
// hardcoded `sslmode=disable` left the entire core module green — an operator
// could set `verify-full`, every test would pass, and every service would speak
// plaintext. The tests below assert the CALL SITES, which is where the value
// either arrives or silently does not.
//
// The same audit found a third builder that had been missed entirely
// (computePostgresInstanceDatabaseUrl). That is why the last test in this file
// enumerates the SET of builders rather than checking the three we know about:
// fixing the instance is worth one bug, testing the set is worth the next one.

// rdbFixture builds the minimum RdbManager needed to render a connection
// string. It deliberately uses values that would be VISIBLE if they landed in
// the wrong slot of the URL.
func rdbFixture(t *testing.T, sslMode string) (*RdbManager, *PostgresConfig) {
	t.Helper()
	pg := &PostgresConfig{
		Hostname: "dc-postgresql.dc-system",
		Port:     5432,
		Username: "devicechain",
		Password: "devicechain",
		SslMode:  sslMode,
	}
	return &RdbManager{
		Microservice: &core.Microservice{
			InstanceId:     "prod",
			FunctionalArea: "device-management",
		},
		InstanceConfig: config.DatastoreConfiguration{
			Configuration: map[string]interface{}{
				"hostname": pg.Hostname,
				"port":     pg.Port,
				"username": pg.Username,
				"password": pg.Password,
			},
		},
	}, pg
}

// connectionStringBuilders is the set under test. A builder added to
// postgres.go and not added here is caught by
// TestEveryConnectionStringBuilderIsCovered below.
type connectionStringBuilder struct {
	// fn is the name as it appears in the source, which is how the coverage
	// guard correlates this table with the file.
	fn    string
	build func(*RdbManager, *PostgresConfig) (string, error)
}

func connectionStringBuilders() []connectionStringBuilder {
	return []connectionStringBuilder{
		{"computePostgresDsn", (*RdbManager).computePostgresDsn},
		{"computePostgresRootUrl", (*RdbManager).computePostgresRootUrl},
		{"computePostgresInstanceDatabaseUrl", (*RdbManager).computePostgresInstanceDatabaseUrl},
	}
}

// The mutation that survived the original commit: hardcode the mode in the
// builder and leave resolveSslMode perfectly tested. Every builder must carry
// the CONFIGURED value, and `disable` is deliberately not the value asserted —
// asserting the default would pass against a hardcoded default.
func TestEveryBuilderCarriesTheConfiguredSslMode(t *testing.T) {
	for _, b := range connectionStringBuilders() {
		t.Run(b.fn, func(t *testing.T) {
			for _, mode := range []string{"require", "verify-full", "disable"} {
				mgr, pg := rdbFixture(t, mode)
				got, err := b.build(mgr, pg)
				if err != nil {
					t.Fatalf("%s(%q): %v", b.fn, mode, err)
				}
				if !strings.Contains(got, "sslmode="+mode) {
					t.Errorf("%s did not carry the configured sslMode %q.\n  got: %s\n"+
						"  An operator setting this would get a different TLS posture than they asked for.",
						b.fn, mode, redactForTest(got))
				}
				// A builder that appended a SECOND sslmode would "contain" the
				// right one while libpq used the last. Count, don't just find.
				if n := strings.Count(got, "sslmode="); n != 1 {
					t.Errorf("%s emitted %d sslmode parameters, want exactly 1: %s",
						b.fn, n, redactForTest(got))
				}
			}
		})
	}
}

// The fail-closed contract, asserted at the builders rather than at
// resolveSslMode. Swallowing the error here turns a config verdict into a
// silent plaintext downgrade, which is the failure this whole knob exists to
// make impossible.
func TestEveryBuilderFailsClosedOnAnUnusableSslMode(t *testing.T) {
	for _, b := range connectionStringBuilders() {
		t.Run(b.fn, func(t *testing.T) {
			mgr, pg := rdbFixture(t, "disable sslrootcert=/tmp/attacker.crt")
			got, err := b.build(mgr, pg)
			if err == nil {
				t.Fatalf("%s accepted an injecting sslMode and returned: %s", b.fn, redactForTest(got))
			}
			if got != "" {
				t.Errorf("%s returned a connection string alongside its error: %s", b.fn, redactForTest(got))
			}
		})
	}
}

// 🔑 Test the SET, not the members.
//
// The sslMode omission that shipped was not a mistake in a builder — it was a
// builder nobody remembered existed. This parses postgres.go, finds every
// function whose body constructs something that looks like a Postgres
// connection string, and requires it to be in the table above. A fourth builder
// therefore fails this test until someone decides whether it needs the mode,
// rather than silently inheriting libpq's default.
func TestEveryConnectionStringBuilderIsCovered(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "postgres.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing postgres.go: %v", err)
	}

	covered := map[string]bool{}
	for _, b := range connectionStringBuilders() {
		covered[b.fn] = true
	}

	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		// A connection string here is any literal carrying the postgres URL
		// scheme or libpq's keyword form.
		builds := false
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			lit, ok := inner.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, "postgres://") ||
				(strings.Contains(lit.Value, "host=") && strings.Contains(lit.Value, "dbname=")) {
				builds = true
				return false
			}
			return true
		})
		if builds {
			found = append(found, fn.Name.Name)
		}
		return true
	})

	// Positive control: if the scan finds nothing, it is broken, not clean.
	// Without this the test would pass loudest exactly when it stopped working.
	if len(found) == 0 {
		t.Fatal("the scan found NO connection-string builders in postgres.go, so it is not measuring anything")
	}
	t.Logf("connection-string builders found in postgres.go: %v", found)

	for _, name := range found {
		if !covered[name] {
			t.Errorf("%s builds a Postgres connection string but is not in connectionStringBuilders().\n"+
				"  Add it there (so the sslMode assertions cover it) or, if it genuinely must not carry\n"+
				"  sslMode, say why in a comment and add it to the table anyway with that expectation.\n"+
				"  A builder that quietly omits sslMode gets libpq's default, which is how the TLS\n"+
				"  posture came to differ across three connections on the same hop.", name)
		}
	}
}

// redactForTest keeps the password out of failure output. The fixture's
// password is not a secret, but the habit is: these strings are printed on
// failure and failure output travels.
func redactForTest(connectionString string) string {
	return strings.ReplaceAll(connectionString, "devicechain:devicechain", "devicechain:REDACTED")
}
