// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func deletionFixture(t *testing.T, sources map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	for name, source := range sources {
		path := filepath.Join(root, filepath.FromSlash("backend/services/"+name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	holders, err := deletionHoldersUnder(root)
	if err != nil {
		t.Fatal(err)
	}
	return holders
}

func TestDeletionPolicyUndeclaredHolder(t *testing.T) {
	holders := deletionFixture(t, map[string]string{
		"example/model/rows.go": "package model; type Row struct { TenantId string; DeviceToken string }",
	})
	problems := deletionPolicyProblems(holders, nil)
	if len(problems) != 1 {
		t.Fatalf("want one missing decision, got %v", problems)
	}
	for _, text := range []string{"backend/services/example/model/rows.go:Row", "decide", "purge", "terminate", "history", "bounded"} {
		if !strings.Contains(problems[0], text) {
			t.Errorf("diagnostic %q omits %q", problems[0], text)
		}
	}
}

func TestDeletionPolicyValidation(t *testing.T) {
	key := "backend/services/example/model/rows.go:Row"
	holders := deletionFixture(t, map[string]string{
		"example/model/rows.go": "package model; type Row struct { TenantId string; EntityToken string }; type Plain struct { Token string }",
	})
	for _, tc := range []struct {
		name, key, policy, why, want string
	}{
		{"StaleMissing", key + "Gone", deletionPurge, "DeletionConsumer removes these rows.", "stale"},
		{"StaleNonHolder", "backend/services/example/model/rows.go:Plain", deletionPurge, "DeletionConsumer removes these rows.", "stale"},
		{"UnknownPolicy", key, "ignore", "This explanation is long enough.", "unknown deletion policy"},
		{"EmptyWhy", key, deletionBounded, "", "Why"},
		{"ShortWhy", key, deletionBounded, "TTL", "Why"},
		{"WhitespaceWhy", key, deletionBounded, strings.Repeat(" ", 30), "Why"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies := map[string]deletionPolicy{key: {deletionPurge, "DeletionConsumer removes these rows."}}
			policies[tc.key] = deletionPolicy{tc.policy, tc.why}
			problems := deletionPolicyProblems(holders, policies)
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("want one %q diagnostic, got %v", tc.want, problems)
			}
		})
	}
	for _, policy := range []string{deletionPurge, deletionTerminate, deletionHistory, deletionBounded} {
		t.Run(policy, func(t *testing.T) {
			if problems := deletionPolicyProblems(holders, map[string]deletionPolicy{
				key: {policy, "A concrete mechanism or bound is explained here."},
			}); len(problems) != 0 {
				t.Fatalf("valid decision rejected: %v", problems)
			}
		})
	}
}

func TestDeletionPolicyHolderShapesAndExclusions(t *testing.T) {
	holders := deletionFixture(t, map[string]string{
		"example/model/rows.go": `package model
type Device struct { TenantId string; DeviceToken string }
type Criteria struct { DeviceToken string }
type Anchor struct { TenantId string; AnchorKind, AnchorToken string }
type TypedAnchor struct { TenantId string; AnchorType, AnchorToken string }
type Origin struct { TenantId string; OriginatorToken *string }
type Entity struct { TenantId string; EntityToken string }
type Source struct { TenantId string; SourceDeviceToken string }
type Plain struct { Token string }
type HalfAnchor struct { AnchorToken string }
func migration() { type Local struct { DeviceToken string } }
`,
		"example/model/migration_old.go":     "package model; type Snapshot struct { DeviceToken string }",
		"example/model/baseline_snapshot.go": "package model; type Baseline struct { DeviceToken string }",
		"example/schema/migration_old.go":    "package schema; type Snapshot struct { DeviceToken string `gorm:\"index\"` }",
		"example/model/rows_test.go":         "package model; type TestOnly struct { DeviceToken string }",
		"example/graphql/request.go":         "package graphql; type Request struct { DeviceToken string }",
		// Qualification is per package, not per file. A live model outside model/
		// brings the sibling result shape into scope too, but no snapshot does.
		"example/store/row.go":               "package store; type Row struct { DeviceToken string `gorm:\"index\"` }",
		"example/store/result.go":            "package store; type Result struct { SourceDeviceToken string }",
		"example/snapshots/migration_old.go": "package snapshots; type Old struct { DeviceToken string `gorm:\"index\"` }",
		"example/snapshots/request.go":       "package snapshots; type Request struct { DeviceToken string }",
		"example/local/request.go":           "package local; func f() { type Snapshot struct { DeviceToken string `gorm:\"index\"` } }; type Request struct { DeviceToken string }",
		"example/embedded/row.go":            "package embedded; type Row struct { gorm.Model; DeviceToken string }",
	})
	want := []string{
		"backend/services/example/embedded/row.go:Row",
		"backend/services/example/model/rows.go:Anchor",
		"backend/services/example/model/rows.go:Device",
		"backend/services/example/model/rows.go:Entity",
		"backend/services/example/model/rows.go:Origin",
		"backend/services/example/model/rows.go:Source",
		"backend/services/example/model/rows.go:TypedAnchor",
		"backend/services/example/store/row.go:Row",
	}
	if !reflect.DeepEqual(holders, want) {
		t.Fatalf("holders = %v, want %v", holders, want)
	}
}

func TestDeletionPolicyOwnerCrossEntityReferences(t *testing.T) {
	holders := deletionFixture(t, map[string]string{
		"device-management/model/rows.go": `package model
type DeviceRequest struct { DeviceToken string }
type PendingEntityDeletion struct { EntityToken string }
type Membership struct { TenantId string; GroupToken string }
type Sample struct { DeviceToken string ` + "`gorm:\"index\"`" + ` }
`,
	})
	want := []string{
		"backend/services/device-management/model/rows.go:Membership",
		"backend/services/device-management/model/rows.go:Sample",
	}
	if !reflect.DeepEqual(holders, want) {
		t.Fatalf("holders = %v, want %v", holders, want)
	}
}
