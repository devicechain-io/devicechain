// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

func TestShardPermissionsOffAndExactGroups(t *testing.T) {
	users, err := ServiceUsers("prod")
	require.NoError(t, err)
	before, err := json.Marshal(users)
	require.NoError(t, err)
	require.NoError(t, AddShardGroupPermissions("prod", users, nil))
	after, err := json.Marshal(users)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, AddShardGroupPermissions("prod", users, &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events", "resolved-events"}}))
	source := users[AppAccount][LoginUser("event-sources")]
	dm := users[AppAccount][LoginUser("device-management")]
	admin := users[AppAccount][AdminUser]
	for group := 0; group < 3; group++ {
		suffix := messaging.GroupSuffix("inbound-events", group)
		stream := messaging.StreamName("prod", suffix)
		durable := messaging.DurableName("prod", "device-management", suffix)
		require.True(t, source.publishes("prod.tenant."+suffix))
		require.True(t, dm.publishes("$JS.API.CONSUMER.MSG.NEXT."+stream+"."+durable))
		require.True(t, dm.publishes("$JS.ACK."+stream+"."+durable+".1.1.1.1.1"))
		require.True(t, admin.publishes("$JS.API.STREAM.CREATE."+stream))
		require.False(t, source.publishes("$JS.API.STREAM.CREATE."+stream))
		require.False(t, source.publishes("$JS.API.CONSUMER.MSG.NEXT."+stream+"."+durable))
	}
	for _, suffix := range []string{"inbound-events-g0", "inbound-events-g3", "inbound-events-g20", "inbound-events-evil", "inbound-events2", "resolved-events-g3", "commands-g1"} {
		require.False(t, source.publishes("prod.tenant."+suffix), suffix)
		require.False(t, admin.publishes("$JS.API.STREAM.CREATE.prod_"+suffix), suffix)
	}
	for _, area := range config.NatsLoginAreas() {
		p := users[AppAccount][LoginUser(area)]
		require.False(t, p.publishes("other.tenant.inbound-events-g1"), area)
		require.False(t, p.publishes("prod.tenant.device-events-g1"), area)
		for group := 1; group < 3; group++ {
			require.False(t, p.publishes(fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.prod_inbound-events-g%d.somebody-else", group)), area)
		}
	}
	invalid := &config.NatsShardGroupsConfiguration{Count: 1000000}
	require.Error(t, AddShardGroupPermissions("prod", users, invalid))
}

func TestShardPermissionsDoNotRewriteInstanceNames(t *testing.T) {
	for _, instance := range []string{"inbound-events", "prod_inbound-events"} {
		users, err := ServiceUsers(instance)
		require.NoError(t, err)
		require.NoError(t, AddShardGroupPermissions(instance, users, &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{"inbound-events"}}))
		source := users[AppAccount][LoginUser("event-sources")]
		require.True(t, source.publishes(instance+".tenant.inbound-events-g1"))
		require.False(t, source.publishes(instance+"-g1.tenant.inbound-events-g1"))
		dm := users[AppAccount][LoginUser("device-management")]
		stream := messaging.StreamName(instance, "inbound-events-g1")
		durable := messaging.DurableName(instance, "device-management", "inbound-events-g1")
		require.True(t, dm.publishes("$JS.API.CONSUMER.MSG.NEXT."+stream+"."+durable))
	}
}
