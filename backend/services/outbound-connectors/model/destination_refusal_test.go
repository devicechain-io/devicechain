// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A broker URL on a unix socket is refused when the connector is saved, and nothing is
// written: the refusal belongs at the door, where the author is still looking, not in a
// dead-letter the first time a rule fires. The row count is the evidence; an error
// returned after an insert would read the same from the caller's side.
func TestCreateConnectorRefusesUnixBroker(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	_, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "sock", Type: string(ConnectorTypeMQTT),
		Config: `{"urls":["unix:///var/run/broker.sock"],"topic":"t"}`,
	})
	require.Error(t, err)

	var count int64
	require.NoError(t, api.RDB.DB(ctx).Model(&Connector{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "a refused connector must not be stored")
}
