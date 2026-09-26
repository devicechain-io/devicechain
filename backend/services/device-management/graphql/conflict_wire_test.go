// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A repeated unique value, answered ON THE WIRE. These run through gqlcore's schema —
// the one every served endpoint is built from — rather than a bare graphql-go schema,
// because the code is given at that boundary and a bare schema would bypass it.
//
// The code is spelled as the literal "CONFLICT" so the test pins the published string.

func conflictWireCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DeviceType{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The per-tenant token index the migration builds; the fixture omits it otherwise.
	if err := rdb.CreateTenantTokenIndex(db, &model.DeviceType{}); err != nil {
		t.Fatalf("create the device type token index: %v", err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, auth.DeviceWrite, auth.DeviceRead)
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

func execServed(t *testing.T, ctx context.Context, query string, vars map[string]any) []*gqlerrors.QueryError {
	t.Helper()
	schema := gqlcore.MustParseSchema(SchemaContent, &SchemaResolver{})
	return schema.Exec(ctx, query, "", vars).Errors
}

func requireNoErrors(t *testing.T, errs []*gqlerrors.QueryError, what string) {
	t.Helper()
	if len(errs) > 0 {
		t.Fatalf("%s failed, so the fixture is broken rather than the duplicate refused: %v", what, errs)
	}
}

func onlyError(t *testing.T, errs []*gqlerrors.QueryError) *gqlerrors.QueryError {
	t.Helper()
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %d: %v", len(errs), errs)
	}
	return errs[0]
}

const (
	wireCreateProfile = `mutation ($request: DeviceProfileCreateRequest) {
  createDeviceProfile(request: $request) { token } }`
	wireCreateType = `mutation ($request: DeviceTypeCreateRequest) {
  createDeviceType(request: $request) { token } }`
	wireCreateCommand = `mutation ($request: CommandDefinitionCreateRequest) {
  createCommandDefinition(request: $request) { token } }`
)

// A device type created twice at one token collides at the unique index. The caller
// gets the code and none of the database's wording.
func TestARepeatedDeviceTypeTokenAnswersConflict(t *testing.T) {
	ctx := conflictWireCtx(t)
	req := map[string]any{"request": map[string]any{"token": "sensor"}}
	requireNoErrors(t, execServed(t, ctx, wireCreateType, req), "the first create")

	qe := onlyError(t, execServed(t, ctx, wireCreateType, req))
	if got := qe.Extensions["code"]; got != "CONFLICT" {
		t.Fatalf("extensions.code = %v, want CONFLICT (message %q)", got, qe.Message)
	}
	const neutral = "the request conflicts with an existing record: a value that must be unique is already in use"
	if !strings.HasSuffix(qe.Message, neutral) {
		t.Errorf("the message does not end in the neutral sentence: %s", qe.Message)
	}
	for _, leak := range []string{"UNIQUE constraint failed", "device_types", "2067", "SQLSTATE"} {
		if strings.Contains(qe.Message, leak) {
			t.Errorf("the message still carries database wording (%q): %s", leak, qe.Message)
		}
	}
}

// A command key the profile already declares is refused by the service's own check,
// before any write. It keeps its sentence and gains the code.
func TestARepeatedCommandKeyAnswersConflict(t *testing.T) {
	ctx := conflictWireCtx(t)
	requireNoErrors(t, execServed(t, ctx, wireCreateProfile,
		map[string]any{"request": map[string]any{"token": "rover"}}), "the profile create")
	requireNoErrors(t, execServed(t, ctx, wireCreateCommand, map[string]any{"request": map[string]any{
		"token": "cmd-1", "deviceProfileToken": "rover", "commandKey": "drive"}}), "the first command")

	qe := onlyError(t, execServed(t, ctx, wireCreateCommand, map[string]any{"request": map[string]any{
		"token": "cmd-2", "deviceProfileToken": "rover", "commandKey": "drive"}}))
	if got := qe.Extensions["code"]; got != "CONFLICT" {
		t.Fatalf("extensions.code = %v, want CONFLICT (message %q)", got, qe.Message)
	}
	if !strings.Contains(qe.Message, `"drive"`) || !strings.Contains(qe.Message, "command keys must be unique per profile") {
		t.Fatalf("the refusal lost its own sentence: %s", qe.Message)
	}
}

// The control: a refusal that is not about a taken value carries no CONFLICT.
func TestAnUnknownProfileIsNotAConflict(t *testing.T) {
	ctx := conflictWireCtx(t)
	qe := onlyError(t, execServed(t, ctx, wireCreateCommand, map[string]any{"request": map[string]any{
		"token": "cmd-1", "deviceProfileToken": "nope", "commandKey": "drive"}}))
	if got, set := qe.Extensions["code"]; set && got == "CONFLICT" {
		t.Fatalf("a missing profile was answered as a conflict: %s", qe.Message)
	}
}
