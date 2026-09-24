// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-processing/connectorwire"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/devicechain-io/dc-outbound-connectors/processor"
)

// ackingBroker accepts MQTT connections, answers CONNECT with CONNACK and a QoS 1 PUBLISH
// with PUBACK, and counts the PUBLISHes.
func ackingBroker(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var published atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					h, err := r.ReadByte()
					if err != nil {
						return
					}
					n, mult := 0, 1
					for {
						b, err := r.ReadByte()
						if err != nil {
							return
						}
						n += int(b&0x7f) * mult
						mult *= 128
						if b&0x80 == 0 {
							break
						}
					}
					body := make([]byte, n)
					if _, err := io.ReadFull(r, body); err != nil {
						return
					}
					switch h >> 4 {
					case 1:
						_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
					case 3:
						published.Add(1)
						tl := int(body[0])<<8 | int(body[1])
						id := body[2+tl : 4+tl]
						_, _ = c.Write([]byte{0x40, 0x02, id[0], id[1]})
					case 14:
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), &published
}

// The operator's allowed destinations must reach the PUBLISH path through exactly the
// wiring the service runs — newExecutor over the instance's egress configuration — and not
// only the webhook path. A wiring that built the sender from a fresh guard would refuse
// every private broker even after the operator allowed it, and a wiring that built it
// unguarded would allow everything; both are one line away.
func TestEgressAllowanceReachesThePublishPath(t *testing.T) {
	broker, published := ackingBroker(t)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&model.Connector{}, &model.ConnectorVersion{}))
	require.NoError(t, rdb.CreateTenantTokenIndex(db, &model.Connector{}))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	kek, err := secrets.NewInstanceKeyProvider([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	store := secrets.NewStore(db, kek)
	api := model.NewApi(&rdb.RdbManager{Database: db}, store)

	ctx := core.WithTenant(context.Background(), "acme")
	_, err = api.CreateConnector(ctx, &model.ConnectorCreateRequest{
		Token: "pager", Type: "mqtt", Config: `{"urls":["tcp://` + broker + `"],"topic":"alerts"}`,
	})
	require.NoError(t, err)
	_, err = api.PublishConnector(ctx, "pager", nil, nil, "alice", nil)
	require.NoError(t, err)

	req := &connectorwire.ConnectorDispatchRequest{
		Kind: connectorwire.ConnectorKindPublish, Tenant: "acme", IdempotencyKey: "idem-1",
		Payload: `{}`, Publish: &connectorwire.PublishDispatch{ConnectorRef: "pager"},
	}
	run := func(allowed []string) string {
		var infra mscfg.InfrastructureConfiguration
		infra.Egress.AllowedDestinations = allowed
		e, err := newExecutor(infra, processor.NewSecretResolver(store), api, 3*time.Second)
		require.NoError(t, err)
		return e.Execute(ctx, req).Outcome()
	}

	assert.Equal(t, "sent", run([]string{"127.0.0.1/32"}))
	assert.Equal(t, int32(1), published.Load())
	assert.Equal(t, "blocked", run(nil))
	assert.Equal(t, int32(1), published.Load(), "a refused dispatch must not reach the broker")

	// A malformed allowance fails construction rather than being skipped.
	var infra mscfg.InfrastructureConfiguration
	infra.Egress.AllowedDestinations = []string{"127.0.0.1/8"}
	_, err = newExecutor(infra, processor.NewSecretResolver(store), api, time.Second)
	assert.Error(t, err)
}
