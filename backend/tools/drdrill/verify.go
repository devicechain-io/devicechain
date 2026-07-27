// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/userclient"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RootKeyEnv is the environment variable verify reads the base64 instance root
// key from when --root-key-file is not given. It is never a flag: a flag value is
// visible in the process table to every user on the box.
const RootKeyEnv = "DRDRILL_ROOT_KEY"

type verifyOptions struct {
	receipt     string
	rootKeyFile string

	host     string
	port     int
	user     string
	password string
	database string

	server string
	scheme string
}

func runVerify(ctx context.Context, argv []string) error {
	fs := flagSetFor("verify")
	var o verifyOptions
	fs.StringVar(&o.receipt, "receipt", "", "receipt written by `drdrill seed` (required)")
	fs.StringVar(&o.rootKeyFile, "root-key-file", "", "file holding the base64 instance root key (or set "+RootKeyEnv+")")
	fs.StringVar(&o.host, "db-host", "127.0.0.1", "Postgres host (a port-forward to the instance database)")
	fs.IntVar(&o.port, "db-port", 5432, "Postgres port")
	fs.StringVar(&o.user, "db-user", "devicechain", "Postgres user")
	fs.StringVar(&o.password, "db-password", "", "Postgres password (or set PGPASSWORD)")
	fs.StringVar(&o.database, "db-name", "", "instance database name (defaults to the receipt's instance)")
	fs.StringVar(&o.server, "server", "localhost", "instance ingress host the API is reachable on")
	fs.StringVar(&o.scheme, "scheme", "http", "http or https for the API check")
	if err := fs.Parse(argv); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if strings.TrimSpace(o.receipt) == "" {
		return failWith(exitSetup, "--receipt is required")
	}

	receipt, err := ReadReceipt(o.receipt)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	if o.database == "" {
		o.database = receipt.Instance
	}
	if o.password == "" {
		o.password = os.Getenv("PGPASSWORD")
	}

	rootKey, err := loadRootKey(o.rootKeyFile)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}

	// CHECK 1 — the live instance can see the restored row.
	//
	// This runs first on purpose. It establishes that the restore actually
	// happened and the service is up, so that when the decrypt below fails, the
	// only remaining explanation is the key. Without it, a negative control could
	// "fail at the decrypt" on an instance where nothing had been restored at all.
	//
	// There is deliberately no flag to skip it. One existed, and it was a way to
	// silently delete the precheck without any assertion changing: the rig reads
	// only the exit code, so a caller that passed --skip-api would still get a 3
	// and still be told the control held.
	if err := checkChannelVisible(ctx, o, receipt); err != nil {
		return failWith(exitSetup, "%w", err)
	}
	fmt.Printf("ok   the instance still lists channel %q, and reports it holds a secret\n", receipt.ChannelToken)

	// CHECK 2 — the row decrypts under the root key this cluster carries.
	db, err := openInstanceDB(ctx, o, receipt.Schema)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	defer closeDB(db)

	row, err := readSecretRow(ctx, db, receipt)
	if err != nil {
		return failWith(exitSetup, "looking for the secret row: %w", err)
	}
	if row == nil {
		return failWith(exitNotFound,
			"no secret row for handle %q under tenant %q in schema %q — the restore did not bring it back, so this run says nothing about the root key",
			receipt.SecretName, receipt.Tenant, receipt.Schema)
	}
	fmt.Printf("ok   the secret row is present in %s.secrets under tenant %q (%s, KEK v%d)\n",
		receipt.Schema, receipt.Tenant, row.Alg, row.KEKVersion)

	provider, err := secrets.NewInstanceKeyProvider(rootKey)
	if err != nil {
		return failWith(exitSetup, "%w", err)
	}
	store := secrets.NewStore(db, provider)
	ref := secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: receipt.Tenant, Name: receipt.SecretName}

	plaintext, resolveErr := store.Resolve(ctx, ref)
	outcome := resolveOutcome{
		Err:       resolveErr,
		Plaintext: plaintext,
		Expected:  receipt.Secret,
		Envelope:  *row,
	}
	if resolveErr != nil {
		// Everything below is gathered so that classify can tell a failed unwrap
		// apart from the several other things that reach the same error return.
		// It is collected here, where the I/O lives, and judged there, where it
		// can be tested.
		outcome.CtxErr = ctx.Err()
		outcome.ReRead, outcome.ReReadErr = readSecretRow(ctx, db, receipt)
	}
	if verdict := classify(outcome); verdict != nil {
		return verdict
	}

	fmt.Printf("ok   the secret decrypts and matches what the platform sealed before the disaster\n")
	fmt.Printf("\nRESTORE DRILL PASSED for instance %q: a secret written by the old cluster is readable in this one.\n", receipt.Instance)
	return nil
}

// storedEnvelope is the envelope metadata of the row under test — everything
// except the secret material itself.
type storedEnvelope struct {
	Alg           string
	KEKVersion    int
	CiphertextLen int
	NonceLen      int
	WrappedDEKLen int
}

// damaged reports whether the stored envelope is malformed independently of any
// key, and why.
//
// This exists because secrets.Open fails closed on an unsupported algorithm, an
// unknown KEK version and a truncated wrapped DEK, and returns a plain error for
// each — indistinguishable, at the call site, from "the key did not fit". Without
// this check a restore that rehydrated these columns badly would be reported as a
// negative control that HELD, which is the strongest claim this tool can make and
// the wrong one.
func (e storedEnvelope) damaged() (string, bool) {
	switch {
	case e.Alg != secrets.AlgAES256GCM:
		return fmt.Sprintf("the row records value algorithm %q, not %s", e.Alg, secrets.AlgAES256GCM), true
	case e.KEKVersion != expectedKEKVersion:
		return fmt.Sprintf("the row records KEK version %d, and this build only wraps with v%d",
			e.KEKVersion, expectedKEKVersion), true
	case e.CiphertextLen == 0:
		return "the row's ciphertext is empty", true
	case e.NonceLen == 0:
		return "the row's nonce is empty", true
	case e.WrappedDEKLen == 0:
		return "the row's wrapped DEK is empty", true
	}
	return "", false
}

// expectedKEKVersion is the single generation the instance KEK provider seals
// with. It is stated here rather than imported because the secrets package keeps
// it unexported; a future multi-version provider makes this a range check.
const expectedKEKVersion = 1

// resolveOutcome is everything runVerify learns from attempting the decrypt.
//
// It is gathered by the caller, which does the I/O, and judged by classify, which
// does none — so the one piece of logic that decides what this tool REPORTS can
// be tested against every combination without a cluster. That split is the point:
// before it existed, inverting the comparison or swapping the decrypt verdict for
// success left every test green.
type resolveOutcome struct {
	Err       error  // what store.Resolve returned; nil on success
	Plaintext []byte // what it returned; meaningful only when Err is nil
	Expected  string // what the receipt says was sealed

	Envelope storedEnvelope // the row's envelope metadata, read before the attempt

	// The following are gathered only after a failure.
	CtxErr    error           // ctx.Err() at that moment
	ReRead    *storedEnvelope // the row re-read by the IDENTICAL query; nil if gone
	ReReadErr error           // why the re-read failed, if it did
}

// classify turns an outcome into the process's verdict, or nil for success.
//
// The ordering is the argument. Exit 3 is the strongest thing this tool says —
// the rig reads it as "the escrow is what protects the data" — so every other
// explanation for a failed Resolve is eliminated first, and anything left
// unexplained falls to exit 1 rather than to a verdict.
func classify(o resolveOutcome) error {
	if o.Err == nil {
		if subtle.ConstantTimeCompare(o.Plaintext, []byte(o.Expected)) != 1 {
			// Never print either value: one is the expected plaintext and the other
			// is whatever came out of the store, and both are secret material.
			return failWith(exitMismatch,
				"the secret decrypted but does not match what was sealed (%d bytes out, %d expected) — this is data corruption, not a key problem",
				len(o.Plaintext), len(o.Expected))
		}
		return nil
	}

	if errors.Is(o.Err, secrets.ErrSecretNotFound) {
		return failWith(exitNotFound,
			"the row is present but the store did not match it — scope or tenant mismatch, not a key failure")
	}

	// An interrupted run is not evidence about anything. gorm propagates the
	// context into the query, so a SIGTERM from a CI timeout or an operator's
	// Ctrl-C lands here as an ordinary error — and used to be reported as a
	// decrypt failure, i.e. as a negative control that held because somebody
	// pressed a key.
	if o.CtxErr != nil {
		return failWith(exitSetup,
			"the run was interrupted (%v) while resolving the secret — inconclusive, nothing was learned about the key", o.CtxErr)
	}

	// The row was there a moment ago. If the IDENTICAL query no longer works, the
	// failure is the connection and not the envelope.
	switch {
	case o.ReReadErr != nil:
		return failWith(exitSetup,
			"resolve failed (%v) AND re-reading the same row failed (%v) — inconclusive, fix the connection and re-run",
			o.Err, o.ReReadErr)
	case o.ReRead == nil:
		return failWith(exitSetup,
			"resolve failed (%v) and the row then disappeared — something is writing to this table concurrently, so this run is inconclusive",
			o.Err)
	}

	// A malformed envelope is a data-integrity finding, not a key finding.
	if why, bad := o.ReRead.damaged(); bad {
		return failWith(exitMismatch,
			"the stored envelope is damaged and could not have opened under ANY key: %s (resolve said: %v)", why, o.Err)
	}

	return failWith(exitDecryptFailed,
		"the stored secret CANNOT be decrypted with this instance's root key: %w", o.Err)
}

// loadRootKey reads the base64 instance root key from a file or the environment
// and decodes it. It refuses an empty value rather than falling through to a
// zero key, which would produce a decrypt failure indistinguishable from the real
// negative control.
func loadRootKey(path string) ([]byte, error) {
	var encoded string
	switch {
	case path != "":
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read root key: %w", err)
		}
		encoded = strings.TrimSpace(string(raw))
	default:
		encoded = strings.TrimSpace(os.Getenv(RootKeyEnv))
	}
	if encoded == "" {
		return nil, fmt.Errorf("no instance root key: pass --root-key-file or set %s (an empty key would fail the decrypt for the wrong reason)", RootKeyEnv)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("the instance root key is not valid base64: %w", err)
	}
	return key, nil
}

// openInstanceDB connects to the instance database with the search path pinned to
// the seeding service's functional-area schema.
//
// The pin is done with an explicit SET on a single pooled connection rather than
// through the DSN's search_path parameter, because the schema names carry hyphens
// and only a quoted identifier survives that. gorm's Tabler interface means the
// secrets table resolves as a bare `secrets`, so the search path is what decides
// which schema is read — getting it wrong would report "not found" and look like
// a failed restore.
func openInstanceDB(ctx context.Context, o verifyOptions, schema string) (_ *gorm.DB, err error) {
	dsn := fmt.Sprintf("user=%s password=%s host=%s dbname=%s port=%d sslmode=disable",
		o.user, o.password, o.host, o.database, o.port)
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s@%s:%d/%s: %w", o.user, o.host, o.port, o.database, err)
	}
	// Every return below this point leaves an open pool behind otherwise; the
	// process is short-lived, but describeSecretsTables also runs a query on a
	// connection nobody would ever close.
	defer func() {
		if err != nil {
			closeDB(db)
		}
	}()
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	// One connection, so the SET below governs every statement this process makes.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)

	if err := db.WithContext(ctx).Exec(fmt.Sprintf("SET search_path TO %s, public", quoteIdentifier(schema))).Error; err != nil {
		return nil, fmt.Errorf("pin search_path to %q: %w", schema, err)
	}

	// Confirm the pin took AND that the table is where we think it is. A silent
	// miss here would surface as "the restore did not bring the row back", which
	// is a completely different — and much more alarming — conclusion.
	var located *string
	if err := db.WithContext(ctx).Raw("SELECT to_regclass('secrets')::text").Scan(&located).Error; err != nil {
		return nil, fmt.Errorf("locate the secrets table: %w", err)
	}
	if located == nil {
		return nil, fmt.Errorf("no `secrets` table is reachable with search_path pinned to %q in database %q%s",
			schema, o.database, describeSecretsTables(ctx, db))
	}
	return db, nil
}

// describeSecretsTables reports every schema that actually has a `secrets` table,
// so a wrong --schema or a restore into the wrong database says so instead of
// leaving the operator to guess.
func describeSecretsTables(ctx context.Context, db *gorm.DB) string {
	var schemas []string
	if err := db.WithContext(ctx).Raw("SELECT table_schema FROM information_schema.tables WHERE table_name = 'secrets' ORDER BY table_schema").
		Scan(&schemas).Error; err != nil {
		return ""
	}
	if len(schemas) == 0 {
		return " (this database has no `secrets` table in any schema)"
	}
	return fmt.Sprintf(" (it exists in: %s)", strings.Join(schemas, ", "))
}

// readSecretRow answers "is the ciphertext there, and what shape is it in",
// separately from whether it opens. Keeping the two apart is what lets verify
// distinguish a restore that did not happen from a key that does not fit.
//
// It reads the SAME columns store.Resolve reads, through the same model, on
// purpose. The earlier version counted rows instead, which meant the check used
// to rule out "the database is gone" touched neither the envelope columns nor the
// context — so a failure specific to reading them passed the check and was
// reported as a failed decrypt. A probe narrower than the thing it is vouching
// for cannot vouch for it.
//
// Returns (nil, nil) when the row is absent, which gorm's soft-delete scope makes
// identical to Resolve's own notion of absent.
func readSecretRow(ctx context.Context, db *gorm.DB, r Receipt) (*storedEnvelope, error) {
	var row secrets.Secret
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND scope = ? AND name = ?", r.Tenant, string(secrets.ScopeTenant), r.SecretName).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &storedEnvelope{
		Alg:           row.Alg,
		KEKVersion:    row.KEKVersion,
		CiphertextLen: len(row.Ciphertext),
		NonceLen:      len(row.Nonce),
		WrappedDEKLen: len(row.WrappedDEK),
	}, nil
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// quoteIdentifier renders a SQL identifier safely. The schema names in play are
// functional areas from the chart, but this is a SET statement built by string
// formatting and it is not going to be the place that gets it wrong.
func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// checkChannelVisible asks the RESTORED instance, over its own API, whether the
// channel is back and still reports holding a secret.
func checkChannelVisible(ctx context.Context, o verifyOptions, r Receipt) error {
	if r.Identity == "" || r.Password == "" {
		return fmt.Errorf("the receipt carries no identity, so the API check cannot run; pass --skip-api to test the decrypt alone")
	}
	base := fmt.Sprintf("%s://%s", o.scheme, o.server)
	session := userclient.NewTenantSession(drillHTTPClient(o.scheme), base+"/api/user-management/graphql",
		r.Identity, r.Password, r.Tenant)

	var out struct {
		Channels []struct {
			ID        string `json:"id"`
			Token     string `json:"token"`
			HasSecret bool   `json:"hasSecret"`
		} `json:"notificationChannelsByToken"`
	}
	url := fmt.Sprintf("%s/api/%s/graphql", base, areaNotification)
	if err := session.Query(ctx, url, channelsByTokenQuery, map[string]any{"tokens": []string{r.ChannelToken}}, &out); err != nil {
		return fmt.Errorf("querying the restored instance for channel %q: %w", r.ChannelToken, err)
	}
	// Match the channel we asked about rather than trusting position. The query is
	// by token and returns a list; taking [0] would silently vouch for a different
	// channel if the API ever returned more than was asked for.
	for _, c := range out.Channels {
		if c.Token != r.ChannelToken {
			continue
		}
		if !c.HasSecret {
			return fmt.Errorf("channel %q is back but reports NO secret; the secrets table did not restore with it", r.ChannelToken)
		}
		return nil
	}
	return fmt.Errorf("the restored instance does not have channel %q — the relational restore did not land", r.ChannelToken)
}

// drillHTTPClient builds the client used for every API call. With https it skips
// certificate verification, because a local instance is served by a self-signed
// cert that nothing on the drill box trusts; the drill authenticates to the API
// with a password and is not a place where TLS trust is the thing under test.
func drillHTTPClient(scheme string) *http.Client {
	c := &http.Client{Timeout: 30 * time.Second}
	if strings.EqualFold(scheme, "https") {
		c.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // see the comment above
		}
	}
	return c
}
