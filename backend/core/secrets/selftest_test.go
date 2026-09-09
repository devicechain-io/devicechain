// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// differentRootKey returns a well-formed 256-bit key that is not the all-zero key
// goodRootKey supplies — the wrong-key case this check exists for is a key that is
// perfectly valid and simply not the right one.
func differentRootKey() ([]byte, error) {
	key := make([]byte, dekSize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	// Guarantee it differs from the all-zero key even in the astronomically
	// unlikely case rand produces one, so the negative control cannot be a no-op.
	key[0] |= 0x01
	return key, nil
}

// countSecrets reports how many live secret rows db holds. The tests use it as an
// explicit PREMISE: a self-test that refuses is only evidence if the store it read
// actually had something in it, otherwise the refusal and a skipped check look the
// same from outside.
func countSecrets(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.WithContext(core.WithSystemContext(context.Background())).
		Model(&Secret{}).Count(&n).Error; err != nil {
		t.Fatalf("count secrets: %v", err)
	}
	return n
}

// TestSelfTestVerifiesAgainstStoredCiphertext is the positive arm: with the key the
// secrets were sealed under, the check reports that it actually opened one.
func TestSelfTestVerifiesAgainstStoredCiphertext(t *testing.T) {
	db := newStoreDB(t)
	kp := newTestKP(t)
	store := NewStore(db, kp)
	if err := store.Put(t.Context(), instanceRef("ai/provider/x"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	result, err := SelfTest(t.Context(), db, kp)
	if err != nil {
		t.Fatalf("the correct key must pass the self-test: %v", err)
	}
	if result != SelfTestVerified {
		t.Fatalf("result = %q, want %q", result, SelfTestVerified)
	}
	if !result.Verified() {
		t.Fatalf("result %q must report Verified", result)
	}
}

// TestSelfTestDoesNotReportAnEmptyStoreAsVerified pins the distinction the check is
// built around. A fresh instance must start, but "there was nothing to check" is not
// a passed check: recorded as one, the check would report success in precisely the
// situation where it examined nothing.
func TestSelfTestDoesNotReportAnEmptyStoreAsVerified(t *testing.T) {
	db := newStoreDB(t)
	if n := countSecrets(t, db); n != 0 {
		t.Fatalf("premise broken: a fresh store must be empty, holds %d rows", n)
	}

	result, err := SelfTest(t.Context(), db, newTestKP(t))
	if err != nil {
		t.Fatalf("an empty store must not fail startup: %v", err)
	}
	if result != SelfTestNoStoredSecrets {
		t.Fatalf("an empty store must report %q, got %q", SelfTestNoStoredSecrets, result)
	}
	if result.Verified() {
		t.Fatalf("an empty store must NOT be reported as verified, got %q reporting Verified", result)
	}
	if result == SelfTestVerified {
		t.Fatalf("an empty store must not carry the verified result value")
	}
}

// TestSelfTestRefusesAWrongButWellFormedKey is the negative control. A different
// valid 32-byte key builds a perfectly good provider and opens nothing, and the
// refusal must name the root key rather than the database.
func TestSelfTestRefusesAWrongButWellFormedKey(t *testing.T) {
	db := newStoreDB(t)
	sealing := newTestKP(t)
	const plaintext = "the-stored-credential"
	if err := NewStore(db, sealing).Put(t.Context(), instanceRef("connector/1/auth"), []byte(plaintext)); err != nil {
		t.Fatalf("put: %v", err)
	}
	// The premise, stated so a pass cannot come from an empty store: the check has
	// something to read, and the SAME store passes under the right key.
	if n := countSecrets(t, db); n != 1 {
		t.Fatalf("premise broken: the store must hold exactly one row, holds %d", n)
	}
	if result, err := SelfTest(t.Context(), db, sealing); err != nil || result != SelfTestVerified {
		t.Fatalf("premise broken: the sealing key must verify against this store, got %q / %v", result, err)
	}

	raw, err := differentRootKey()
	if err != nil {
		t.Fatalf("generate a second key: %v", err)
	}
	wrong, err := NewInstanceKeyProvider(raw)
	if err != nil {
		t.Fatalf("premise broken: the second key must be well-formed: %v", err)
	}

	result, err := SelfTest(t.Context(), db, wrong)
	if err == nil {
		t.Fatalf("a wrong-but-well-formed key must be refused, got result %q", result)
	}
	if result != "" {
		t.Fatalf("a refusal must not also carry a result value, got %q", result)
	}
	msg := err.Error()
	if !strings.Contains(msg, "instance root key") {
		t.Fatalf("the refusal must name the root key as the cause, got %q", msg)
	}
	if strings.Contains(msg, "database failure") {
		t.Fatalf("a key failure must not be reported as a database failure, got %q", msg)
	}
	// The check reads real ciphertext; nothing it says may carry the value.
	if strings.Contains(msg, plaintext) {
		t.Fatal("the refusal message must not contain stored plaintext")
	}
}

// TestNewRefusesAWrongButWellFormedRootKey runs the same negative control through
// the wiring point every service uses, so the check is known to be reached at
// construction and not merely available.
func TestNewRefusesAWrongButWellFormedRootKey(t *testing.T) {
	db := newStoreDB(t)
	sealingKey, err := differentRootKey()
	if err != nil {
		t.Fatalf("generate the sealing key: %v", err)
	}
	sealingSource := func() ([]byte, error) { return sealingKey, nil }

	store, err := New(DefaultConfig(), db, sealingSource)
	if err != nil {
		t.Fatalf("an empty store must construct: %v", err)
	}
	const plaintext = "smtp-password"
	if err := store.Put(t.Context(), instanceRef("channel/tok/secret"), []byte(plaintext)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if n := countSecrets(t, db); n != 1 {
		t.Fatalf("premise broken: the store must hold exactly one row, holds %d", n)
	}
	// The same key still constructs over the now-populated store, so the refusal
	// below is attributable to the key and not to the store having become non-empty.
	if _, err := New(DefaultConfig(), db, sealingSource); err != nil {
		t.Fatalf("the sealing key must still construct over its own store: %v", err)
	}

	// goodRootKey is a different, equally well-formed 32-byte key.
	wrongStore, err := New(DefaultConfig(), db, goodRootKey)
	if err == nil {
		t.Fatalf("New must refuse a root key that does not open the stored secrets, got %#v", wrongStore)
	}
	if wrongStore != nil {
		t.Fatalf("New returned an error AND a non-nil store %#v", wrongStore)
	}
	if !strings.Contains(err.Error(), "instance root key") {
		t.Fatalf("the refusal must name the root key, got %q", err)
	}
	if strings.Contains(err.Error(), plaintext) {
		t.Fatal("the refusal message must not contain stored plaintext")
	}
}

// TestSelfTestNamesTheDatabaseWhenItCannotBeRead keeps the two failure modes apart.
// An operator meeting an unreachable database must not be sent to look at the key.
func TestSelfTestNamesTheDatabaseWhenItCannotBeRead(t *testing.T) {
	db := newStoreDB(t)
	kp := newTestKP(t)
	if err := NewStore(db, kp).Put(t.Context(), instanceRef("n"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result, err := SelfTest(t.Context(), db, kp)
	if err == nil {
		t.Fatalf("an unreadable database must be terminal, not skipped; got result %q", result)
	}
	msg := err.Error()
	if !strings.Contains(msg, "database failure") {
		t.Fatalf("the error must name the database as the cause, got %q", msg)
	}
	if !strings.Contains(msg, "not a root-key failure") {
		t.Fatalf("the error must say it is not a root-key failure, got %q", msg)
	}
}

// TestSelfTestRefusesAMissingSecretsTable proves a missing table is not folded into
// "nothing stored". That folding would make the check silently vacuous on exactly
// the installs whose wiring runs it before the schema exists.
func TestSelfTestRefusesAMissingSecretsTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// The platform callbacks a real service has, but deliberately no secrets table.
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}

	result, err := SelfTest(t.Context(), db, newTestKP(t))
	if err == nil {
		t.Fatalf("a missing secrets table must be terminal, got result %q", result)
	}
	if result == SelfTestNoStoredSecrets {
		t.Fatal("a missing table must not be reported as an empty store")
	}
	if !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("the error must point at the unrun migrations, got %q", err)
	}
}

// TestSelfTestReadsTheLowestIdRowThroughTheKeyProvider pins two things at once:
// WHICH row is read (the lowest primary key, so the check is the same on every
// restart and every replica rather than whichever row a plan returns first), and
// that the unwrap goes through the KeyProvider interface carrying the ROW's own
// recorded KEK version rather than an assumed current one — which is what makes a
// future multi-version provider correct here by construction.
func TestSelfTestReadsTheLowestIdRowThroughTheKeyProvider(t *testing.T) {
	db := newStoreDB(t)
	store := NewStore(db, newTestKP(t))
	for _, name := range []string{"first", "second", "third"} {
		if err := store.Put(t.Context(), instanceRef(name), []byte("v-"+name)); err != nil {
			t.Fatalf("put %s: %v", name, err)
		}
	}
	// Stamp a KEK version no current provider seals with, so an implementation that
	// passed a hardcoded version instead of the row's would be visible.
	const storedVersion = 7
	sys := db.WithContext(core.WithSystemContext(t.Context()))
	if err := sys.Model(&Secret{}).Where("name = ?", "first").
		Update("kek_version", storedVersion).Error; err != nil {
		t.Fatalf("stamp version: %v", err)
	}

	var rows []Secret
	if err := sys.Model(&Secret{}).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("premise broken: want 3 rows, got %d", len(rows))
	}
	if rows[0].Name != "first" {
		t.Fatalf("premise broken: lowest-id row is %q, want %q", rows[0].Name, "first")
	}

	spy := &recordingKeyProvider{}
	sql := &statementRecorder{}
	if _, err := SelfTest(t.Context(), db.Session(&gorm.Session{Logger: sql}), spy); err != nil {
		t.Fatalf("self-test with the recording provider: %v", err)
	}
	// Assert the ORDER BY directly rather than inferring it from which row came
	// back. sqlite returns a small table in rowid order anyway, so an unordered read
	// would produce the right row here by accident and the determinism claim would
	// rest on the engine's incidental behaviour instead of on the query.
	if got := sql.selectFromSecrets(t); !strings.Contains(strings.ToUpper(got), "ORDER BY") {
		t.Fatalf("the read must be explicitly ordered, got %q", got)
	}
	if spy.calls != 1 {
		t.Fatalf("the check must unwrap exactly one row, got %d unwrap calls", spy.calls)
	}
	if spy.version != storedVersion {
		t.Fatalf("unwrap was given KEK version %d, want the row's recorded %d", spy.version, storedVersion)
	}
	if !bytes.Equal(spy.wrapped, rows[0].WrappedDEK) {
		t.Fatalf("the check read a row other than the lowest-id one (id %d)", rows[0].ID)
	}
}

// statementRecorder is a gorm logger that keeps the SQL gorm actually issued, so a
// test can assert the shape of the query rather than only its result.
type statementRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *statementRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

func (r *statementRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, sql)
}

// selectFromSecrets returns the single SELECT the recorder saw against the secrets
// table, failing the test if there was not exactly one — more than one would mean
// the assertion could be reading a different statement than it thinks.
func (r *statementRecorder) selectFromSecrets(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []string
	for _, s := range r.statements {
		// Quote-stripped so the match does not depend on the driver's identifier
		// quoting, and anchored on FROM so the migrator's own catalog probe — which
		// mentions the table name but does not read the table — is not counted.
		upper := strings.NewReplacer("`", "", `"`, "").Replace(strings.ToUpper(s))
		if strings.HasPrefix(upper, "SELECT") && strings.Contains(upper, "FROM SECRETS") {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one SELECT against secrets, saw %d of %d statements", len(found), len(r.statements))
	}
	return found[0]
}

// recordingKeyProvider is a KeyProvider that records what it was asked to unwrap and
// always succeeds, so a test can observe WHICH row and WHICH version reached the
// interface without decrypting anything.
type recordingKeyProvider struct {
	calls   int
	wrapped []byte
	version int
}

func (p *recordingKeyProvider) Name() string { return "recording" }

func (p *recordingKeyProvider) Wrap(context.Context, []byte) ([]byte, int, error) {
	return make([]byte, dekSize), 1, nil
}

func (p *recordingKeyProvider) Unwrap(_ context.Context, wrapped []byte, version int) ([]byte, error) {
	p.calls++
	p.wrapped = append([]byte(nil), wrapped...)
	p.version = version
	return make([]byte, dekSize), nil
}

// TestSelfTestRejectsMissingDependencies keeps the guard clauses honest: the check
// must refuse rather than quietly do nothing when it has no database or no provider.
func TestSelfTestRejectsMissingDependencies(t *testing.T) {
	if _, err := SelfTest(t.Context(), nil, newTestKP(t)); err == nil {
		t.Fatal("a nil database handle must be refused")
	}
	if _, err := SelfTest(t.Context(), newStoreDB(t), nil); err == nil {
		t.Fatal("a nil key provider must be refused")
	}
}
