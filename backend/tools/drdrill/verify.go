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
	"strconv"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
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
	sslMode  string

	server string
	scheme string

	// secretAreaRefused says the area that STORES the secret is expected not to be
	// serving, because it refused to start on a root key that does not open its
	// stored ciphertext. Only the restore drill's negative control passes it.
	secretAreaRefused bool
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
	// Defaults to `prefer`, matching core/rdb: negotiate TLS when the store
	// offers it, fall back when it does not. Hard-coding `disable` here meant the
	// drill could not reach a TLS-requiring store at all — i.e. it failed exactly
	// where proving recoverability matters most.
	fs.StringVar(&o.sslMode, "db-sslmode", "prefer", "libpq sslmode for the database connection")
	fs.StringVar(&o.server, "server", "localhost", "instance ingress host the API is reachable on")
	fs.StringVar(&o.scheme, "scheme", "http", "http or https for the API check")
	fs.BoolVar(&o.secretAreaRefused, "secret-area-refused", false,
		"the secret-storing area is EXPECTED not to serve (it refused a wrong root key); "+
			"swaps the API precheck for a stronger one — see checkRestoredWithoutSecretArea")
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
	// There is deliberately no flag to SKIP it. One existed, and it was a way to
	// silently delete the precheck without any assertion changing: the rig reads
	// only the exit code, so a caller that passed --skip-api would still get a 3
	// and still be told the control held.
	//
	// --secret-area-refused is not that flag, and the difference is the whole point.
	// It does not remove the premise; it REPLACES it with one that is strictly
	// harder to satisfy by accident, and that FAILS on a healthy instance. See
	// checkRestoredWithoutSecretArea.
	if o.secretAreaRefused {
		if err := checkRestoredWithoutSecretArea(ctx, o, receipt); err != nil {
			return failWith(exitSetup, "%w", err)
		}
	} else {
		if err := checkChannelVisible(ctx, o, receipt); err != nil {
			return failWith(exitSetup, "%w", err)
		}
		fmt.Printf("ok   the instance still lists channel %q, and reports it holds a secret\n", receipt.ChannelToken)
	}

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
	// sslmode comes from the flag, and the values are QUOTED (ADR-020 A2.1).
	//
	// Both matter for an instrument whose whole job is proving recoverability.
	// Hard-coding `disable` meant that the moment a store required TLS, the drill
	// could not connect to the deployment where recoverability matters most — it
	// would report a failed restore for a database that had restored perfectly.
	// And an unquoted password containing a space parses as a runtime parameter
	// rather than a password, which leaves no host and sends libpq to a unix
	// socket inside this process's own container: the drill would then blame the
	// restore for a connection it mis-assembled itself.
	dsn := strings.Join([]string{
		"user=" + rdb.QuoteDSNValue(o.user),
		"password=" + rdb.QuoteDSNValue(o.password),
		"host=" + rdb.QuoteDSNValue(o.host),
		"dbname=" + rdb.QuoteDSNValue(o.database),
		"port=" + rdb.QuoteDSNValue(strconv.Itoa(o.port)),
		"sslmode=" + rdb.QuoteDSNValue(o.sslMode),
	}, " ")
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

	if err := db.WithContext(ctx).Exec(fmt.Sprintf("SET search_path TO %s, public", rdb.QuoteIdentifier(schema))).Error; err != nil {
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

// checkChannelVisible asks the RESTORED instance, over its own API, whether the
// channel is back and still reports holding a secret.
func checkChannelVisible(ctx context.Context, o verifyOptions, r Receipt) error {
	if r.Identity == "" || r.Password == "" {
		// No "pass --skip-api" here, whatever an operator might wish for: that flag
		// was removed on purpose (see the note at the call site) and telling
		// someone to reach for it sends them looking for a way to delete the
		// precheck. A receipt with no identity was written by a `seed` that did
		// not finish, so the fix is upstream.
		return fmt.Errorf("the receipt carries no identity, so the API check cannot run; re-seed — this receipt was written by a seed that did not complete")
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

// checkRestoredWithoutSecretArea is the API precheck for the one case where the
// ordinary one cannot run: the area that STORES the secret has refused to start.
//
// 🔴 WHY THIS EXISTS AT ALL, because "the check could not run, so skip it" is the
// exact reasoning this drill refuses everywhere else.
//
// A service that stores secrets checks its instance root key against its own stored
// ciphertext as it builds the secret store, and refuses to start if the key does not
// open it. That is correct, and it is the behaviour the negative control is trying to
// provoke — but it also means notification-management cannot answer the ordinary
// precheck's channel query in that phase. Not "is slow to", not "usually will not":
// cannot, by design. A premise that is impossible to satisfy is not a premise, and
// leaving it in place made the control die on a wrong-reason setup failure.
//
// So the premise is REPLACED, not dropped, and the replacement is asserted BOTH ways:
//
//  1. POSITIVE — the relational store really did restore, proved by logging in as the
//     identity `seed` minted and finding this run's tenant among its memberships. That
//     exercises identity, credential and membership rows written by THIS run, which is
//     a stronger statement about the restore than "a channel is listed" was.
//  2. NEGATIVE — the secret-storing area must NOT answer. If it does, the instance did
//     not refuse, so whatever this invocation was called for, it is not a control, and
//     saying so is a setup failure rather than a verdict.
//
// Together those make the flag impossible to use as a way to quietly delete the
// precheck: passing it against a healthy instance fails at (2), and passing it against
// an instance that restored nothing fails at (1).
//
// It deliberately does NOT try to establish WHY the area is not serving. A pod that is
// down for any other reason would satisfy (2) just as well, which is why the caller
// asserts the specific root-key refusal in that area's own log before running this —
// see assert_startup_refusal in hack/dr-rig.sh. This function checks that the world is
// in the shape that assertion described; it is not a second copy of it.
func checkRestoredWithoutSecretArea(ctx context.Context, o verifyOptions, r Receipt) error {
	if r.Identity == "" || r.Password == "" {
		return fmt.Errorf("the receipt carries no identity, so the restore cannot be confirmed at all; re-seed — this receipt was written by a seed that did not complete")
	}
	base := fmt.Sprintf("%s://%s", o.scheme, o.server)

	auth, err := userclient.Login(ctx, drillHTTPClient(o.scheme), base+"/api/user-management/graphql", r.Identity, r.Password)
	if err != nil {
		return fmt.Errorf("logging in to the restored instance as %q: %w\nThe relational store did not come back, or did not come back with this run's identity in it. "+
			"Nothing below would be evidence about the root key", r.Identity, err)
	}
	found := false
	for _, m := range auth.Memberships {
		if m.Tenant == r.Tenant {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("identity %q is back but is not a member of tenant %q, which this run created; "+
			"the relational restore is not the one this receipt describes", r.Identity, r.Tenant)
	}
	fmt.Printf("ok   the relational store restored: identity %q is back and still a member of tenant %q\n", r.Identity, r.Tenant)

	serving, detail, err := areaIsServing(ctx, o, areaNotification)
	if err != nil {
		return fmt.Errorf("could not determine whether %s is serving: %w\n"+
			"This mode's whole premise is that it is NOT, and an unanswered question is not a premise", areaNotification, err)
	}
	if serving {
		return fmt.Errorf("--secret-area-refused was given, but %s IS serving (%s).\n"+
			"That area stores the secret, so a service that is answering built its secret store — its root "+
			"key OPENS the stored ciphertext. The instance did not refuse, and this run is not a negative "+
			"control. Refusing rather than reporting a verdict, because a decrypt result from here would be "+
			"read as evidence about a key that was never wrong", areaNotification, detail)
	}
	fmt.Printf("ok   %s is NOT serving (%s), which is what this mode requires\n", areaNotification, detail)
	return nil
}

// areaIsServing answers ONE narrow question: is there a live backend behind the
// instance's ingress for this functional area?
//
// 🔴 IT IS NOT `checkChannelVisible` INVERTED, and the first draft of this code made
// exactly that mistake. checkChannelVisible fails for a refused login, a failed tenant
// selection, a network blip, a channel that is absent, a channel that reports no
// secret, AND for an area that is down — one error path, six causes. Reading "it
// returned an error" as "the area refused its root key" is a control that holds for
// any reason at all, which is the shape this whole rig exists to argue against. It was
// caught by TestRefusedAreaModeRefusesAHealthyInstance, which passed a HEALTHY instance
// and was told the area had refused.
//
// So this asks the question directly and unauthenticated. A trivial introspection POST
// needs no token: any answer the SERVICE produces — 200, 400, 401 — means something
// live is behind the route, and only a gateway error or a dead connection means there
// is not. That is the same rule wait_for_api uses in hack/dr-rig.sh, read in the
// opposite direction.
//
// It deliberately does not try to say WHY a silent area is silent. The caller asserts
// the specific root-key refusal in that area's own log before getting here.
func areaIsServing(ctx context.Context, o verifyOptions, area string) (bool, string, error) {
	url := fmt.Sprintf("%s://%s/api/%s/graphql", o.scheme, o.server, area)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(`{"query":"{__typename}"}`))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := drillHTTPClient(o.scheme).Do(req)
	if err != nil {
		// Nothing accepted the connection at all. Distinguishable from a gateway
		// error, and both mean the same thing here.
		return false, "the connection was refused", nil //nolint:nilerr // the transport error IS the answer
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode >= 500:
		// 502/503/504 is the ingress saying it has no healthy upstream — the shape a
		// crash-looping area produces. A 500 from the service itself would also land
		// here; that is a service which is up but broken, and calling it "not serving"
		// understates it, so the detail carries the code for a reader of the log.
		return false, fmt.Sprintf("HTTP %d from the ingress, no healthy backend", resp.StatusCode), nil
	case resp.StatusCode == http.StatusNotFound:
		// ingress-nginx's default backend, served until the route is admitted.
		return false, "HTTP 404 — the route is not admitted, so nothing is behind it", nil
	default:
		return true, fmt.Sprintf("HTTP %d — the service answered", resp.StatusCode), nil
	}
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
