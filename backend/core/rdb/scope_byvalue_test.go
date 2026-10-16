// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// This file uses only API that existed before scoped memberships (ADR-081): the scoped
// caller's claims are DECODED from the token payload a scoped member carries, exactly as
// the validator decodes them. Before the scope callback existed the "scp" field was
// unknown, so the decoder dropped it and the query below returned every row of the
// tenant — the test fails there by value.
func scopedCallerFromPayload(t *testing.T, tenant string) context.Context {
	t.Helper()
	var claims auth.Claims
	payload := `{"tenant":"` + tenant + `","typ":"access","authorities":["*"],"scp":["customer:acme-corp"]}`
	if err := json.Unmarshal([]byte(payload), &claims); err != nil {
		t.Fatal(err)
	}
	return auth.WithClaims(core.WithTenant(context.Background(), tenant), &claims)
}

// 🔴 FAIL CLOSED: a scoped caller's query against a tenant-scoped model that declares no
// scope rule is REFUSED — never answered with the whole tenant. `widget` is such a model.
func TestScopedQueryAgainstUnruledModelIsRefused(t *testing.T) {
	db := newTestDB(t)
	unscoped := core.WithTenant(context.Background(), "A")
	if err := db.WithContext(unscoped).Create(&widget{Name: "globex-pump"}).Error; err != nil {
		t.Fatal(err)
	}

	var got []widget
	err := db.WithContext(scopedCallerFromPayload(t, "A")).Find(&got).Error
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a scoped caller read an unruled tenant-scoped model: err=%v rows=%v", err, got)
	}
	if len(got) != 0 {
		t.Fatalf("a refused scoped query still returned rows: %v", got)
	}

	// Negative control: the same query unscoped returns the row, so the refusal above
	// is the scope and not the fixture.
	if err := db.WithContext(unscoped).Find(&got).Error; err != nil || len(got) != 1 {
		t.Fatalf("the unscoped control: err=%v rows=%v", err, got)
	}
}
