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
	"github.com/jackc/pgx/v5/pgconn"
)

// These assert the CALL SITES, and they assert them by handing the built string
// to pgconn's OWN parser — the same code that will consume it at runtime.
//
// That choice is the point. An earlier version of this file checked
// `strings.Contains(dsn, "sslmode=require")`, which is a restatement of the
// formatting rather than a test of the meaning: it passed for a DSN whose
// password had silently redirected the connection to a unix socket, and it broke
// the moment values started being quoted even though the DSN got strictly more
// correct. Asking the real parser what a string MEANS cannot be fooled by either.
//
// The adversarial values below are not hypothetical — every one of them was
// found by review against the fmt.Sprintf implementation this replaced.

func rdbFixture(t *testing.T, pg *PostgresConfig) *RdbManager {
	t.Helper()
	return &RdbManager{
		Microservice: &core.Microservice{
			InstanceId:     "prod",
			FunctionalArea: "device-management",
		},
		InstanceConfig: config.DatastoreConfiguration{
			Configuration: map[string]interface{}{},
		},
	}
}

func basePg(sslMode string) *PostgresConfig {
	return &PostgresConfig{
		Hostname: "dc-postgresql.dc-system",
		Port:     5432,
		Username: "devicechain",
		Password: "devicechain",
		SslMode:  sslMode,
	}
}

type builder struct {
	fn    string
	build func(*RdbManager, *PostgresConfig) (string, error)
	// db is the database this builder is expected to target.
	db string
}

func builders() []builder {
	return []builder{
		{"computePostgresDsn", (*RdbManager).computePostgresDsn, "prod"},
		{"computePostgresRootUrl", (*RdbManager).computePostgresRootUrl, "postgres"},
		{"computePostgresInstanceDatabaseUrl", (*RdbManager).computePostgresInstanceDatabaseUrl, "prod"},
	}
}

// parse hands the string to pgconn and returns what it actually resolved to.
func parse(t *testing.T, connString string) *pgconn.Config {
	t.Helper()
	cfg, err := pgconn.ParseConfig(connString)
	if err != nil {
		t.Fatalf("pgconn could not parse the connection string we produced: %v", err)
	}
	return cfg
}

// Every builder must carry the configured mode, and pgconn must AGREE about what
// it means — TLS actually configured for require/verify-full, actually absent for
// disable. Asserting the substring would pass for a string pgconn rejects.
func TestEveryBuilderCarriesTheConfiguredSslMode(t *testing.T) {
	for _, b := range builders() {
		t.Run(b.fn, func(t *testing.T) {
			for _, tc := range []struct {
				mode   string
				wantTL bool
			}{
				{"disable", false},
				{"require", true},
				{"verify-full", true},
				{"prefer", true}, // prefer configures TLS with a plaintext fallback
			} {
				got, err := b.build(rdbFixture(t, nil), basePg(tc.mode))
				if err != nil {
					t.Fatalf("%s(%q): %v", b.fn, tc.mode, err)
				}
				cfg := parse(t, got)
				if hasTLS := cfg.TLSConfig != nil; hasTLS != tc.wantTL {
					t.Errorf("%s with sslMode=%q resolved to TLSConfig!=nil == %v, want %v.\n"+
						"  The operator asked for %q and pgconn understood something else.",
						b.fn, tc.mode, hasTLS, tc.wantTL, tc.mode)
				}
			}
		})
	}
}

// The coordinates must survive the round trip unchanged. This is what catches a
// value that has become syntax: a password containing a space used to leave
// `host` unset, so pgconn silently fell back to a unix socket and the service
// dialled itself.
func TestEveryBuilderRoundTripsHostileValues(t *testing.T) {
	hostile := []struct {
		name, password string
	}{
		{"a space", "hunter 2"},
		{"empty", ""},
		{"a percent escape that must stay literal", "p%41ss"},
		{"a single quote", "it's"},
		{"a backslash", `back\slash`},
		{"an at sign", "p@ss"},
		{"a slash", "p/ss"},
		{"a question mark", "p?ss"},
		{"a hash", "p#ss"},
		{"an equals and a space, i.e. an injection attempt", "x= y sslmode=disable"},
	}
	for _, b := range builders() {
		for _, h := range hostile {
			t.Run(b.fn+"/"+h.name, func(t *testing.T) {
				pg := basePg("require")
				pg.Password = h.password
				got, err := b.build(rdbFixture(t, nil), pg)
				if err != nil {
					t.Fatalf("%s: %v", b.fn, err)
				}
				cfg := parse(t, got)

				if cfg.Password != h.password {
					t.Errorf("%s mangled the password: got %q, want %q.\n"+
						"  The root and per-database connections would then authenticate differently.",
						b.fn, cfg.Password, h.password)
				}
				if cfg.Host != "dc-postgresql.dc-system" {
					t.Errorf("%s lost the host: got %q.\n"+
						"  A host of \"/tmp\" means the value became syntax and pgconn fell back to a unix socket.",
						b.fn, cfg.Host)
				}
				if cfg.Port != 5432 {
					t.Errorf("%s lost the port: got %d", b.fn, cfg.Port)
				}
				if cfg.Database != b.db {
					t.Errorf("%s targeted database %q, want %q", b.fn, cfg.Database, b.db)
				}
				// The injection case must not have turned TLS off.
				if cfg.TLSConfig == nil {
					t.Errorf("%s: sslMode=require did not survive a password of %q — TLS is off",
						b.fn, h.password)
				}
			})
		}
	}
}

// The trailing-position defect: `search_path` is emitted last, so a functional
// area carrying its own keywords used to be able to append an sslmode that WON,
// because libpq takes the last occurrence.
func TestATrailingRuntimeParameterCannotOverrideSslMode(t *testing.T) {
	mgr := rdbFixture(t, nil)
	mgr.Microservice.FunctionalArea = "usermgmt sslmode=disable search_path=usermgmt"
	mgr.Microservice.InstanceId = "prod sslrootcert=/tmp/attacker.crt"

	got, err := mgr.computePostgresDsn(basePg("verify-full"))
	if err != nil {
		t.Fatalf("computePostgresDsn: %v", err)
	}
	cfg := parse(t, got)

	if cfg.TLSConfig == nil {
		t.Errorf("a hostile functional area turned off TLS.\n  rendered: %s", redactForTest(got))
	}
	if cfg.Database != "prod sslrootcert=/tmp/attacker.crt" {
		t.Errorf("the instance id leaked out of its value slot: database=%q", cfg.Database)
	}
	if cfg.Host != "dc-postgresql.dc-system" {
		t.Errorf("the connection was redirected to %q", cfg.Host)
	}
}

// Fail closed at the builders, not only in resolveSslMode.
func TestEveryBuilderFailsClosedOnAnUnusableSslMode(t *testing.T) {
	for _, b := range builders() {
		t.Run(b.fn, func(t *testing.T) {
			pg := basePg("disable sslrootcert=/tmp/attacker.crt")
			got, err := b.build(rdbFixture(t, nil), pg)
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
// The sslMode omission that shipped was a builder nobody remembered existed.
// This scans the files where connection strings are constructed and requires
// every function that builds one to be in the table above.
func TestEveryConnectionStringBuilderIsCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, b := range builders() {
		covered[b.fn] = true
	}
	// The low-level helpers in connstring.go are the escaping primitives, not
	// per-connection builders; they are exercised by the round-trip tests above.
	covered["postgresURL"] = true
	covered["postgresKeywordDSN"] = true

	var found []string
	for _, src := range []string{"postgres.go", "connstring.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", src, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			builds := false
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				lit, ok := inner.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v := lit.Value
				if strings.Contains(v, "postgres://") || strings.Contains(v, "postgres") && strings.Contains(v, "://") ||
					strings.Contains(v, "sslmode=") || strings.Contains(v, "dbname=") {
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
	}

	// Positive control: an empty scan is a broken test, not a clean one. This
	// fired for real when the string literals moved into connstring.go.
	if len(found) == 0 {
		t.Fatal("the scan found NO connection-string builders, so it is not measuring anything")
	}
	t.Logf("connection-string builders found: %v", found)

	for _, name := range found {
		if !covered[name] {
			t.Errorf("%s builds a Postgres connection string but is not covered.\n"+
				"  Add it to builders() so the sslMode and round-trip assertions reach it.\n"+
				"  A builder that quietly omits sslMode inherits pgx's default, which is how the\n"+
				"  TLS posture came to differ across three connections on the same hop.", name)
		}
	}
}

func redactForTest(connectionString string) string {
	return strings.ReplaceAll(connectionString, "devicechain", "REDACTED")
}
