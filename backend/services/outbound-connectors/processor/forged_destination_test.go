// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// newRealPublishExecutor builds an executor exactly as the service does — the real
// publish path, not the injected fake — over a real connector store, with the given
// egress allowances.
func newRealPublishExecutor(t *testing.T, allow ...string) (*Executor, *model.Api) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&model.Connector{}, &model.ConnectorVersion{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &model.Connector{}))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider(publishTestRootKey)
	require.NoError(t, err)
	store := secrets.NewStore(db, kek)
	api := model.NewApi(&rdb.RdbManager{Database: db}, store)

	prefixes := make([]netip.Prefix, 0, len(allow))
	for _, a := range allow {
		prefixes = append(prefixes, netip.MustParsePrefix(a))
	}
	return NewExecutor(NewSecretResolver(store), api, egress.NewGuard(prefixes), 3*time.Second), api
}

// forgeConnector writes a connector and its published version straight through gorm,
// past every write-time check — the shape a corrupt or hand-edited row has. Dispatch is
// the last line, so it must refuse what the save path would have refused.
func forgeConnector(t *testing.T, api *model.Api, ctx context.Context, token, typ, config string) {
	t.Helper()
	db := api.RDB.DB(ctx)
	conn := &model.Connector{TokenReference: rdb.TokenReference{Token: token}, Type: typ,
		Config: datatypes.JSON(config)}
	require.NoError(t, db.Create(conn).Error)
	require.NoError(t, db.Create(&model.ConnectorVersion{ConnectorID: conn.ID, Version: 1, Type: typ,
		Config: datatypes.JSON(config)}).Error)
}

// A stored MQTT connector whose broker is a unix socket must never be dialed. The save
// path refuses the scheme; a row that got past it (forged, or written before the rule
// existed) is refused again at dispatch, and the refusal is terminal `invalid` — a
// redelivery cannot change a stored URL. The socket's accept count is the evidence that
// nothing connected, which an error value alone cannot show.
func TestForgedUnixConnectorIsInvalidAndNeverDialed(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "broker.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	defer ln.Close()
	var accepts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			_ = c.Close()
		}
	}()

	// Every loopback address allowed: whatever refuses this, it is not the address check.
	e, api := newRealPublishExecutor(t, "127.0.0.0/8", "::1/128")
	ctx := core.WithTenant(context.Background(), "acme")
	forgeConnector(t, api, ctx, "sock", "mqtt", `{"urls":["unix://`+sock+`"],"topic":"t"}`)

	res := e.Execute(ctx, publishReq("sock"))
	assert.Equal(t, outcomeInvalid, res.outcome, "err: %v", res.err)
	assert.False(t, res.retryable)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), accepts.Load(), "the unix socket must never be dialed")
}

// A Kafka broker that is merely down is a transient failure: the connector is fine and the
// broker may be back on the next delivery. Dead-lettering it as `invalid` throws away a
// dispatch that a retry would have delivered.
func TestAKafkaBrokerThatIsDownIsRetried(t *testing.T) {
	// A port that was open a moment ago and is now closed: connection refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	e, api := newRealPublishExecutor(t, "127.0.0.1/32")
	ctx := core.WithTenant(context.Background(), "acme")
	_, err = api.CreateConnector(ctx, &model.ConnectorCreateRequest{
		Token: "k", Type: "kafka", Config: `{"addresses":["` + addr + `"],"topic":"t"}`,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "k", nil, nil, "alice", nil)
	require.NoError(t, err)

	res := e.Execute(ctx, publishReq("k"))
	assert.Equal(t, outcomeRetry, res.outcome, "err: %v", res.err)
	assert.True(t, res.retryable)
}

// A publish to a destination the egress guard refuses is TERMINAL `blocked`, and it is
// classified before anything else. The counterweight in the same test: the same connector
// with the destination allowed but nothing listening is an ordinary, retryable failure — a
// classifier that called every publish failure blocked would pass the first half alone.
func TestABlockedPublishIsTerminalNotRetryable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	for _, typ := range []struct{ name, config string }{
		{"mqtt", `{"urls":["tcp://` + addr + `"],"topic":"t"}`},
		{"kafka", `{"addresses":["` + addr + `"],"topic":"t"}`},
	} {
		t.Run(typ.name, func(t *testing.T) {
			ctx := core.WithTenant(context.Background(), "acme")

			blocked, api := newRealPublishExecutor(t)
			forgeConnector(t, api, ctx, "c", typ.name, typ.config)
			res := blocked.Execute(ctx, publishReq("c"))
			assert.Equal(t, outcomeBlocked, res.outcome, "err: %v", res.err)
			assert.False(t, res.retryable)

			down, api := newRealPublishExecutor(t, "127.0.0.1/32")
			forgeConnector(t, api, ctx, "c", typ.name, typ.config)
			res = down.Execute(ctx, publishReq("c"))
			assert.Equal(t, outcomeRetry, res.outcome, "err: %v", res.err)
			assert.True(t, res.retryable)
		})
	}
}
