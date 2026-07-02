// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// grammarThing embeds TokenReference (the common case). plainTokenThing declares
// Token directly (as the user-management Tenant and Role do, without embedding
// TokenReference) — both must be guarded. noTokenThing has no token and must pass
// through untouched.
type grammarThing struct {
	gorm.Model
	TokenReference
	Name string
}

type plainTokenThing struct {
	gorm.Model
	Token string `gorm:"not null;size:128"`
	Name  string
}

type noTokenThing struct {
	gorm.Model
	Name string
}

func newGrammarDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := db.AutoMigrate(&grammarThing{}, &plainTokenThing{}, &noTokenThing{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestValidateToken(t *testing.T) {
	valid := []string{
		"device-1", "a", "ops-overview", "abc123", "0", "9-lives",
		"550e8400-e29b-41d4-a716-446655440000", // a uuid (auto-minted claim token)
	}
	for _, tok := range valid {
		if err := ValidateToken(tok); err != nil {
			t.Errorf("ValidateToken(%q) = %v, want nil", tok, err)
		}
	}

	invalid := []string{
		"",             // empty
		"Ops",          // uppercase
		"ops overview", // space
		"ops_overview", // underscore
		"-lead",        // leading hyphen
		"a.b",          // dot — shifts NATS subject segments
		"a*b",          // NATS wildcard
		"a>b",          // NATS wildcard
		"a/b",          // MQTT separator
		"a+b",          // MQTT wildcard
		"a#b",          // MQTT wildcard
		"café",         // non-ascii
	}
	for _, tok := range invalid {
		if err := ValidateToken(tok); err == nil {
			t.Errorf("ValidateToken(%q) = nil, want error", tok)
		}
	}

	// Length bound.
	long := make([]byte, MaxTokenLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateToken(string(long)); err == nil {
		t.Errorf("ValidateToken(len %d) = nil, want error", MaxTokenLen+1)
	}
}

func TestTokenGrammarCallback_Create(t *testing.T) {
	db := newGrammarDB(t)

	if err := db.Create(&grammarThing{TokenReference: TokenReference{Token: "good-1"}}).Error; err != nil {
		t.Fatalf("valid token create: %v", err)
	}
	// A dangerous token never reaches storage.
	if err := db.Create(&grammarThing{TokenReference: TokenReference{Token: "bad.token"}}).Error; err == nil {
		t.Fatalf("create with a metacharacter token must be rejected")
	}
	// Empty token is rejected on create (the not-null column would allow "").
	if err := db.Create(&grammarThing{TokenReference: TokenReference{Token: ""}}).Error; err == nil {
		t.Fatalf("create with an empty token must be rejected")
	}
}

// A token declared directly (not via the embedded TokenReference) is guarded too
// — this is the Tenant/Role shape, and the tenant token is the highest-value
// target since it splices into NATS subjects.
func TestTokenGrammarCallback_PlainTokenField(t *testing.T) {
	db := newGrammarDB(t)
	if err := db.Create(&plainTokenThing{Token: "acme"}).Error; err != nil {
		t.Fatalf("valid plain token create: %v", err)
	}
	if err := db.Create(&plainTokenThing{Token: "acme.evil"}).Error; err == nil {
		t.Fatalf("plain-field token with a metacharacter must be rejected")
	}
}

// A model without a token passes through the callback untouched.
func TestTokenGrammarCallback_NoTokenModel(t *testing.T) {
	db := newGrammarDB(t)
	if err := db.Create(&noTokenThing{Name: "nothing to see"}).Error; err != nil {
		t.Fatalf("non-token model must pass through: %v", err)
	}
}

func TestTokenGrammarCallback_Update(t *testing.T) {
	db := newGrammarDB(t)
	row := &grammarThing{TokenReference: TokenReference{Token: "good-1"}, Name: "one"}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Updating an unrelated field (token unchanged and valid) is fine.
	row.Name = "renamed"
	if err := db.Save(row).Error; err != nil {
		t.Fatalf("update of a non-token field must pass: %v", err)
	}
	// Mutating the token to an unsafe value is rejected.
	row.Token = "bad*token"
	if err := db.Save(row).Error; err == nil {
		t.Fatalf("updating a token to a metacharacter value must be rejected")
	}

	// A partial map update that does not touch the token passes through.
	if err := db.Model(&grammarThing{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{"name": "again"}).Error; err != nil {
		t.Fatalf("map update without a token key must pass: %v", err)
	}
	// A map update that sets a bad token is rejected.
	if err := db.Model(&grammarThing{}).Where("id = ?", row.ID).
		Updates(map[string]interface{}{"token": "bad.token"}).Error; err == nil {
		t.Fatalf("map update setting a metacharacter token must be rejected")
	}
}

// A batch insert aborts if any row's token is invalid.
func TestTokenGrammarCallback_BatchAborts(t *testing.T) {
	db := newGrammarDB(t)
	rows := []grammarThing{
		{TokenReference: TokenReference{Token: "good-a"}},
		{TokenReference: TokenReference{Token: "bad.b"}},
	}
	if err := db.Create(&rows).Error; err == nil {
		t.Fatalf("batch insert with one invalid token must be rejected")
	}
	var count int64
	db.Model(&grammarThing{}).Count(&count)
	if count != 0 {
		t.Fatalf("no rows should persist when the batch is rejected, got %d", count)
	}
}
