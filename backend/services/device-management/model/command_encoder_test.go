// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"gorm.io/datatypes"
)

func TestCommandEncoderLifecycle(t *testing.T) {
	api := newEnqueueTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	p := &DeviceProfile{}
	p.Token = "encoder-profile"
	if err := api.RDB.DB(ctx).Create(p).Error; err != nil {
		t.Fatal(err)
	}
	schema := `[{"name":"level","dataType":"DOUBLE"}]`
	encoder := `{"fPort":10,"fields":[{"param":"level","type":"i16be","scale":0.1}]}`
	bad := `{"fPort":10,"fields":[{"param":"missing","type":"u8"}]}`
	_, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{Token: "bad", DeviceProfileToken: p.Token, CommandKey: "bad", ParameterSchema: &schema, Encoder: &bad})
	if err == nil {
		t.Fatal("created an orphan encoder")
	}
	c, err := api.CreateCommandDefinition(ctx, &CommandDefinitionCreateRequest{Token: "encoder-command", DeviceProfileToken: p.Token, CommandKey: "setpoint", ParameterSchema: &schema, Encoder: &encoder})
	if err != nil || c.Encoder == nil || string(*c.Encoder) != encoder {
		t.Fatalf("create: %+v %v", c, err)
	}
	_, err = api.UpdateCommandDefinition(ctx, c.Token, &CommandDefinitionUpdateRequest{Encoder: dcgraphql.OptionalStringOf(bad)})
	if err == nil {
		t.Fatal("updated to orphan encoder")
	}
	_, err = api.UpdateCommandDefinition(ctx, c.Token, &CommandDefinitionUpdateRequest{ParameterSchema: dcgraphql.OptionalStringOf(`[]`)})
	if err == nil {
		t.Fatal("schema edit orphaned stored encoder")
	}
	_, err = api.UpdateCommandDefinition(ctx, c.Token, &CommandDefinitionUpdateRequest{Description: dcgraphql.OptionalStringOf("preserve")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := api.PublishDeviceProfile(ctx, p.Token, nil, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseProfileSnapshot(v.Snapshot)
	if err != nil || len(snap.Commands) != 1 || snap.Commands[0].Encoder == nil || string(*snap.Commands[0].Encoder) != encoder {
		t.Fatalf("published round trip: %+v %v", snap, err)
	}
	// Bypass authoring validation to prove publish independently refuses invalid storage.
	if err := api.RDB.DB(ctx).Model(&CommandDefinition{}).Where("id = ?", c.ID).Update("encoder", datatypes.JSON(bad)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := api.PublishDeviceProfile(ctx, p.Token, nil, nil, "test"); err == nil || !strings.Contains(err.Error(), "setpoint") {
		t.Fatalf("publish should name command: %v", err)
	}
	cleared, err := api.UpdateCommandDefinition(ctx, c.Token, &CommandDefinitionUpdateRequest{Encoder: dcgraphql.OptionalString{Set: true}})
	if err != nil || cleared.Encoder != nil {
		t.Fatalf("null did not clear encoder: %+v %v", cleared, err)
	}
}

func TestSnapshotWithoutEncoderRemainsRaw(t *testing.T) {
	snap, err := parseProfileSnapshot(datatypes.JSON(`{"Commands":[{"CommandKey":"old","ParameterSchema":null}]}`))
	if err != nil || len(snap.Commands) != 1 || snap.Commands[0].Encoder != nil {
		t.Fatalf("old snapshot: %+v %v", snap, err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseProfileSnapshot(b)
	if err != nil || again.Commands[0].Encoder != nil {
		t.Fatal("old snapshot changed")
	}
}

func TestCommandEncoderSchemaKinds(t *testing.T) {
	encoder := `{"fPort":1,"fields":[{"param":"x","type":"u8"}]}`
	for _, schema := range []string{`[]`, `[{"name":"x","dataType":"BOOLEAN"}]`, `[{"name":"x","dataType":"STRING"}]`, `[{"name":"x","dataType":"INT"},{"name":"fPort","dataType":"INT"}]`} {
		if validateCommandEncoder(&encoder, &schema) == nil {
			t.Fatal("accepted", schema)
		}
	}
}
