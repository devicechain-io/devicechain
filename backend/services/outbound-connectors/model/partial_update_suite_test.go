// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"testing"

	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	"github.com/devicechain-io/dc-microservice/secrets"
)

// THIS SERVICE'S HALF OF THE PARTIAL-UPDATE HARNESS.
//
// The properties, the anti-vacuity controls and the exhaustiveness check live in core's
// rdb/partialupdatetest, because the three-state update semantic is one platform rule
// and a per-service copy of the harness is the per-family copy one level up — the
// eighth copy certifies less than the first, and every one of them is green. Read that
// package's harness.go for what each property catches.
//
// What stays here is what is genuinely local: the fixture that builds THIS service's
// Api (which needs a secret store, not just a database), the tenant it runs under, and
// the one converted family.

const partialUpdateTenant = "acme"

// newPartialUpdateApi builds a SQLite-backed Api migrated for one family. The database
// half — the named shared-cache DSN, the pool close, the token-grammar registration,
// and the reasons all three are load-bearing — is putest.NewSQLiteDB's.
//
// 🔴 THE SECRET STORE IS PART OF THE FIXTURE, NOT AN EXTRA. A connector's credential is
// never a column, so a fixture without a store could not observe the `secret` field's
// three states at all — and an unobservable field is one the harness cannot protect.
// The store is the real envelope-encrypting one over the same database, so what the
// family reads back has been through the actual seal/resolve path.
func newPartialUpdateApi(t *testing.T, tables ...any) *Api {
	t.Helper()
	db := putest.NewSQLiteDB(t, tables...)
	if err := secrets.NewSecretStoreSchema().Migrate(db); err != nil {
		t.Fatalf("migrate the secret store: %v", err)
	}
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	if err != nil {
		t.Fatalf("build the instance key provider: %v", err)
	}
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek))
}

// storedSecret renders a connector's write-only credential for the harness's
// map[string]string reading: the sealed value, or NullMarker when none is stored.
//
// NullMarker rather than "" is deliberate. "" is what applyConnectorSecret writes to
// mean DELETE, so rendering "no credential" as "" would make the harness unable to tell
// "the caller cleared this" from "a credential holding the empty string" — a state that
// cannot exist today, but the ambiguity would be invisible on the day it could.
func storedSecret(t *testing.T, api *Api, ctx context.Context, token string) string {
	t.Helper()
	found, err := api.ConnectorsByToken(ctx, []string{token})
	conn := putest.RequireOne(t, "connector", found, err)
	ref, err := ConnectorSecretRef(ctx, conn.ID)
	if err != nil {
		t.Fatalf("build the connector's secret ref: %v", err)
	}
	value, err := api.Secrets.Resolve(ctx, ref)
	if errors.Is(err, secrets.ErrSecretNotFound) {
		return putest.NullMarker
	}
	if err != nil {
		t.Fatalf("resolve the connector's secret: %v", err)
	}
	return string(value)
}

// The values the connector family drives. type and config are declared here together
// because they MOVE together — see connectorFamily.
const (
	seededConnectorConfig   = `{"urls":["tcp://broker:1883"],"topic":"alerts"}`
	replacedConnectorConfig = `{"addresses":["k:9092"],"topic":"t"}`
)

// connectorFamily is this service's one converted family.
//
// 🔴 `type` AND `config` ARE DECLARED AS PARTNERS, AND THAT IS A PROPERTY OF THE
// DATATYPE RATHER THAN A CONVENIENCE. The per-type field shape is validated as a pair:
// an mqtt config is not a valid kafka config, so re-pointing the type while leaving the
// config alone is REFUSED at the write (which is the behaviour we want — the
// alternative is a connector that dead-letters at send time). Driving `type` on its own,
// which is what every property here does by default, would therefore assert that a legal
// request fails, and the only way to make the suite green would be to stop driving the
// field at all. The partner mechanism moves the pair and excludes each from the other's
// "everything else is unchanged" check.
func connectorFamily() putest.Family[*Api] {
	typeField, configField := putest.PairedWith(
		putest.RequiredStringField("type", string(ConnectorTypeMQTT), string(ConnectorTypeKafka),
			func(r *ConnectorUpdateRequest) *dcgraphql.OptionalString { return &r.Type }),
		putest.RequiredStringField("config", seededConnectorConfig, replacedConnectorConfig,
			func(r *ConnectorUpdateRequest) *dcgraphql.OptionalString { return &r.Config }),
	)
	return putest.Family[*Api]{
		Name:    "connector",
		Token:   "conn-1",
		Migrate: []any{&Connector{}, &ConnectorVersion{}},
		Seed: func(t *testing.T, api *Api, ctx context.Context) {
			if _, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token:       "conn-1",
				Name:        strp("Ops pager"),
				Description: strp("Original description"),
				Type:        string(ConnectorTypeMQTT),
				Config:      seededConnectorConfig,
				Secret:      strp("s3cret"),
			}); err != nil {
				t.Fatalf("seed connector: %v", err)
			}
		},
		Read: func(t *testing.T, api *Api, ctx context.Context) map[string]string {
			rows, err := api.ConnectorsByToken(ctx, []string{"conn-1"})
			e := putest.RequireOne(t, "connector", rows, err)
			return map[string]string{
				"name":        putest.NullString(e.Name),
				"description": putest.NullString(e.Description),
				"type":        e.Type,
				"config":      string(e.Config),
				"secret":      storedSecret(t, api, ctx, "conn-1"),
			}
		},
		NewRequest: func() any { return new(ConnectorUpdateRequest) },
		Update: func(api *Api, ctx context.Context, token string, req any) error {
			// expectedUpdatedAt is nil: the optimistic-concurrency precondition is a
			// different contract with its own tests, and passing one here would make
			// every property depend on a timestamp round trip.
			_, err := api.UpdateConnector(ctx, token, req.(*ConnectorUpdateRequest), nil)
			return err
		},
		Fields: []putest.Field{
			putest.OptionalStringField("name", "Ops pager", "Renamed",
				func(r *ConnectorUpdateRequest) *dcgraphql.OptionalString { return &r.Name }),
			putest.OptionalStringField("description", "Original description", "Rewritten",
				func(r *ConnectorUpdateRequest) *dcgraphql.OptionalString { return &r.Description }),
			typeField,
			configField,
			// The write-only credential. It is CLEARABLE, and that is this conversion's
			// decision: an explicit null DELETES the stored secret, which is what null
			// means on every other field on the platform. Under the old *string the
			// clear was spelled as the empty string, because a pointer had no third
			// state to give it.
			putest.Field{
				Name: "secret", Seeded: "s3cret", Replace: "rotated", Cleared: putest.NullMarker,
				Kind: putest.Clearable,
				Set: func(req any, v string) {
					req.(*ConnectorUpdateRequest).Secret = dcgraphql.OptionalStringOf(v)
				},
				SetNull: func(req any) {
					req.(*ConnectorUpdateRequest).Secret = dcgraphql.ClearedString()
				},
			},
		},
	}
}

func partialUpdateFamilies() []putest.Family[*Api] {
	return []putest.Family[*Api]{connectorFamily()}
}

// TestPartialUpdate drives every property over every converted family in this service.
//
// A single property or family is still addressable:
//
//	go test ./model -run 'TestPartialUpdate/SettingOneFieldLeavesEveryOtherAlone/connector'
func TestPartialUpdate(t *testing.T) {
	putest.Run(t, putest.Suite[*Api]{
		NewApi:   newPartialUpdateApi,
		Context:  putest.TenantContext(partialUpdateTenant),
		Families: partialUpdateFamilies(),

		// The strictness probe: the only token-keyed create this service has.
		CreateWithToken: func(t *testing.T, api *Api, ctx context.Context, token string) error {
			_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
				Token: token, Type: string(ConnectorTypeMQTT), Config: seededConnectorConfig,
			})
			return err
		},
		StrictnessTables: []any{&Connector{}, &ConnectorVersion{}},
		ValidToken:       "a-valid_token1",
	})
}

// 🔴 THE EXHAUSTIVENESS CHECK OVER THIS SERVICE'S UPDATE SURFACE.
//
// The guard itself is core's putest.AssertEveryUpdateTakesADedicatedRequest — it
// enumerates *Api's own Update* methods and asks three structural things of each: that
// the request type is REGISTERED with partialUpdateFamilies() so something drives its
// three states against a real database; that it carries no Token field; and that every
// exported field carries the three states. Run asks whether the converted families
// BEHAVE; this asks whether the set of converted families is still the whole set.
//
// What is local is what only this service can say: which updates have NOT been
// converted, and how many Update* methods reflection must find before the walk is
// believable.
//
// UpdateConnector carries a trailing `expectedUpdatedAt *string`, which an earlier
// version of the guard walked straight past — it matched on parameter POSITION, so this
// service's only update was certified by nothing at all. The guard now locates the
// input by SHAPE. Nothing here has to say so; it is recorded because the floor below
// looks generous for one method and is not: it counts every Update* method, and one is
// what this service has.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Api]{
		Families: partialUpdateFamilies(),

		// No exemptions: this service's one update is converted. The guard fails on an
		// exemption matching nothing, so the map cannot quietly regrow an entry
		// describing a state of the world that has ended.
		Exempt:            map[string]string{},
		NotAnEntityUpdate: map[string]string{},

		// The anti-vacuity floor, set at the measured count. Reflection over a renamed
		// or embedded receiver could find nothing at all, and a loop over nothing
		// reports success.
		MinUpdateMethods: 1,
	})
}
