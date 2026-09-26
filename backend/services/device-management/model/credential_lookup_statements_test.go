// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// These tests count the SQL statements a presented credential costs. The lookup runs on
// every credential-bearing event and every MQTT connect, and it is deliberately uncached
// (a disable, delete or expiry must take effect on the very next event), so its statement
// count IS its cost.
//
// The refusal table is the other half, and it is the half that matters more: the lookup
// fetches the owning device on a JOIN, which the tenant-scope callback does NOT qualify
// (it adds its predicate to the statement's own table only). Every refusal the lookup made
// before must still be made, with the cross-tenant row as the one the join could have
// silently turned into a grant.

// stmtCounter records the SQL text of every query statement run on one database.
type stmtCounter struct {
	mu  sync.Mutex
	sql []string
}

var stmtCounterSeq atomic.Int64

// countQueries registers a statement recorder on the Api's database.
func countQueries(t *testing.T, api *Api) *stmtCounter {
	t.Helper()
	c := &stmtCounter{}
	name := fmt.Sprintf("test:count-queries-%d", stmtCounterSeq.Add(1))
	if err := api.RDB.Database.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.sql = append(c.sql, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register statement counter: %v", err)
	}
	return c
}

func (c *stmtCounter) reset() {
	c.mu.Lock()
	c.sql = nil
	c.mu.Unlock()
}

func (c *stmtCounter) taken() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sql...)
}

// credFixture is one device "dev" in tenant acme with an MQTT_BASIC credential "cred-1"
// storing "s3cret" and an ACCESS_TOKEN credential "tok-1", behind the cached decorator the
// resolver actually holds.
type credFixture struct {
	api   *Api
	capi  *CachedApi
	ctx   context.Context
	stmts *stmtCounter
	devId uint
}

// seedCredentialFixture seeds the rows of a credFixture into an Api whose tables are empty.
func seedCredentialFixture(t *testing.T, api *Api, stmts *stmtCounter) credFixture {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	dev, err := api.CreateDevice(ctx, &DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"})
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	secret := "s3cret"
	if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	}); err != nil {
		t.Fatalf("seed basic credential: %v", err)
	}
	if _, err := api.CreateDeviceCredential(ctx, &DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(CredentialAccessToken),
		CredentialId: "tok-1", Enabled: true,
	}); err != nil {
		t.Fatalf("seed access-token credential: %v", err)
	}
	stmts.reset()
	return credFixture{api: api, capi: NewCachedApi(api, &Caches{}), ctx: ctx, stmts: stmts, devId: dev.ID}
}

func newSQLiteCredentialFixture(t *testing.T) credFixture {
	t.Helper()
	api := newPartialUpdateApi(t, append(append([]any{}, deviceProfileTables...), &DeviceCredential{})...)
	return seedCredentialFixture(t, api, countQueries(t, api))
}

func accessToken(id string) *PresentedCredential {
	return &PresentedCredential{CredentialType: string(CredentialAccessToken), CredentialId: id}
}

// requireOneJoinedStatement asserts the call just measured ran exactly one statement, and
// that the statement read both the credential and its device — a count of one satisfied
// by a statement that never read the credential would be a pass for the wrong reason.
func requireOneJoinedStatement(t *testing.T, f credFixture, what string) {
	t.Helper()
	taken := f.stmts.taken()
	if len(taken) != 1 {
		t.Fatalf("%s ran %d statements, want 1: %v", what, len(taken), taken)
	}
	if !strings.Contains(taken[0], "device_credentials") || !strings.Contains(taken[0], "devices") {
		t.Fatalf("%s's one statement does not read both the credential and its device: %s", what, taken[0])
	}
	t.Logf("%s: %s", what, taken[0])
}

// Authenticating an access token is one statement, and stays one on a repeat: nothing
// caches a credential, so the second call pays exactly what the first did.
func TestAuthenticatingACredentialIsOneStatement(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	now := time.Now()
	for i := 1; i <= 2; i++ {
		f.stmts.reset()
		d, err := f.capi.AuthenticateDevice(f.ctx, accessToken("tok-1"), now)
		if err != nil || d == nil || d.Token != "dev" {
			t.Fatalf("call %d: got (%v, %v), want device dev", i, d, err)
		}
		requireOneJoinedStatement(t, f, fmt.Sprintf("access-token authentication (call %d)", i))
	}
}

// An MQTT_BASIC credential is one statement whether the secret is right or wrong, and a
// wrong one is still refused: the compare runs on the row the join returned.
func TestAuthenticatingAnMqttBasicCredentialIsOneStatement(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	now := time.Now()

	d, err := f.capi.AuthenticateDevice(f.ctx, basic("cred-1", "s3cret"), now)
	if err != nil || d == nil || d.Token != "dev" {
		t.Fatalf("right secret: got (%v, %v), want device dev", d, err)
	}
	requireOneJoinedStatement(t, f, "basic authentication, right secret")

	f.stmts.reset()
	if _, err := f.capi.AuthenticateDevice(f.ctx, basic("cred-1", "wrong"), now); !errors.Is(err, ErrCredentialSecretMismatch) {
		t.Fatalf("wrong secret: got %v, want ErrCredentialSecretMismatch", err)
	}
	requireOneJoinedStatement(t, f, "basic authentication, wrong secret")
}

// The callout's resolve (no compare) is one statement too.
func TestResolvingACredentialForTheCalloutIsOneStatement(t *testing.T) {
	f := newSQLiteCredentialFixture(t)
	d, stored, err := f.capi.ResolveDeviceCredential(f.ctx, basic("cred-1", "wrong"), time.Now())
	if err != nil || d == nil || d.Token != "dev" || stored != "s3cret" {
		t.Fatalf("resolve: got (%v, %q, %v), want dev with its stored secret", d, stored, err)
	}
	requireOneJoinedStatement(t, f, "callout resolve")
}

// Every refusal the lookup made before, it still makes — each in one statement.
func TestCredentialLookupRefusesExactlyWhatItRefusedBefore(t *testing.T) {
	runCredentialLookupControls(t, newSQLiteCredentialFixture)
}

// runCredentialLookupControls is the refusal table, shared by the SQLite test above and
// the PostgreSQL one behind the integration tag. fresh must return a newly seeded fixture.
func runCredentialLookupControls(t *testing.T, fresh func(t *testing.T) credFixture) {
	now := time.Now()
	cases := []struct {
		name string
		// arrange changes the fixture; it returns the presented credential to look up.
		arrange func(t *testing.T, f credFixture) *PresentedCredential
		want    error
		// alsoResolve runs the callout's ResolveDeviceCredential too.
		alsoResolve bool
	}{
		{
			name:    "unknown credential",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential { return basic("nobody", "x") },
			want:    ErrCredentialNotResolved,
		},
		{
			name: "disabled credential",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential {
				mustExec(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).
					Where("credential_id = ?", "cred-1").Update("enabled", false))
				return basic("cred-1", "s3cret")
			},
			want: ErrCredentialNotResolved,
		},
		{
			name: "soft-deleted device",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential {
				// A gorm SOFT delete of the device row alone. DeleteDevice would hard-delete
				// the credentials with it, and then there would be no row to join from.
				mustExec(t, f.api.RDB.DB(f.ctx).Delete(&Device{}, f.devId))
				return basic("cred-1", "s3cret")
			},
			want:        ErrCredentialNotResolved,
			alsoResolve: true,
		},
		{
			name: "credential of another tenant",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential {
				beta := core.WithTenant(context.Background(), "beta")
				if _, err := f.api.CreateDeviceType(beta, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
					t.Fatalf("seed beta type: %v", err)
				}
				if _, err := f.api.CreateDevice(beta, &DeviceCreateRequest{Token: "bdev", DeviceTypeToken: "dt"}); err != nil {
					t.Fatalf("seed beta device: %v", err)
				}
				secret := "s3cret"
				if _, err := f.api.CreateDeviceCredential(beta, &DeviceCredentialCreateRequest{
					Token: "bc-1", DeviceToken: "bdev", CredentialType: string(CredentialMqttBasic),
					CredentialId: "beta-only", CredentialValue: &secret, Enabled: true,
				}); err != nil {
					t.Fatalf("seed beta credential: %v", err)
				}
				return basic("beta-only", "s3cret")
			},
			want: ErrCredentialNotResolved,
		},
		{
			name: "expired credential",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential {
				mustExec(t, f.api.RDB.DB(f.ctx).Model(&DeviceCredential{}).Where("credential_id = ?", "cred-1").
					Update("expires_at", sql.NullTime{Time: now.Add(-time.Hour), Valid: true}))
				return basic("cred-1", "s3cret")
			},
			want:        ErrCredentialExpired,
			alsoResolve: true,
		},
		{
			// Corrupt data: an acme credential whose device_id names a device in beta.
			// CreateDeviceCredential cannot produce this (it resolves the device inside the
			// tenant), so the row is inserted directly. The join carries no tenant predicate
			// for the device, so this is the refusal the in-memory check exists for.
			name: "device_id pointing into another tenant",
			arrange: func(t *testing.T, f credFixture) *PresentedCredential {
				beta := core.WithTenant(context.Background(), "beta")
				if _, err := f.api.CreateDeviceType(beta, &DeviceTypeCreateRequest{Token: "dt"}); err != nil {
					t.Fatalf("seed beta type: %v", err)
				}
				foreign, err := f.api.CreateDevice(beta, &DeviceCreateRequest{Token: "foreign", DeviceTypeToken: "dt"})
				if err != nil {
					t.Fatalf("seed foreign device: %v", err)
				}
				row := &DeviceCredential{DeviceId: foreign.ID, CredentialType: string(CredentialMqttBasic),
					CredentialId: "crossed", CredentialValue: sql.NullString{String: "s3cret", Valid: true}, Enabled: true}
				row.Token = "c-crossed"
				mustExec(t, f.api.RDB.DB(f.ctx).Create(row))
				return basic("crossed", "s3cret")
			},
			want:        ErrCredentialNotResolved,
			alsoResolve: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fresh(t)
			presented := tc.arrange(t, f)

			f.stmts.reset()
			d, err := f.capi.AuthenticateDevice(f.ctx, presented, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("AuthenticateDevice: got (%v, %v), want %v", d, err, tc.want)
			}
			if taken := f.stmts.taken(); len(taken) != 1 {
				t.Errorf("AuthenticateDevice ran %d statements, want 1: %v", len(taken), taken)
			}

			if tc.alsoResolve {
				f.stmts.reset()
				d, _, err := f.capi.ResolveDeviceCredential(f.ctx, presented, now)
				if !errors.Is(err, tc.want) {
					t.Fatalf("ResolveDeviceCredential: got (%v, %v), want %v", d, err, tc.want)
				}
				if taken := f.stmts.taken(); len(taken) != 1 {
					t.Errorf("ResolveDeviceCredential ran %d statements, want 1: %v", len(taken), taken)
				}
			}
		})
	}
}

func mustExec(t *testing.T, result *gorm.DB) {
	t.Helper()
	if result.Error != nil {
		t.Fatalf("arrange: %v", result.Error)
	}
}
