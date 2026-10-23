// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/natsauth/permtest"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/stretchr/testify/require"
)

// dcctl's own broker checks present the broker administrator's login, not a shared one. This holds it
// against a broker whose users are the generated ones: no `dc_service` exists there, so a check that
// still connected as the shared login would fail to authenticate at all.

const haLoginInstance = "harig"

func administratorView(b *permtest.Broker) config.NatsConfiguration {
	return config.NatsConfiguration{
		Hostname: b.Host,
		Port:     b.Port,
		Auth: config.NatsAuthConfiguration{
			AreaLogins:    true,
			AreaPasswords: map[string]string{config.NatsAdminPrincipal: permtest.Password(natsauth.AdminUser)},
		},
	}
}

func TestTheReplicationCheckConnectsAsTheBrokerAdministratorAndEnumeratesTheBroker(t *testing.T) {
	b := permtest.Start(t, haLoginInstance, permtest.WithMQTT())
	cfg := administratorView(b)

	nc, err := dialBroker(fmt.Sprintf("nats://%s:%d", b.Host, b.Port), cfg)
	require.NoError(t, err, "the check could not authenticate; the generated broker has no shared login to fall back on")
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)

	exp := messaging.ReplicationExpectationFor(haLoginInstance, 1, []string{"device-management", "event-management"}, true)
	snap, err := replication.Collect(js, exp)
	require.NoError(t, err, "the administrator cannot list the streams and consumers the check reads")
	require.NotEmpty(t, snap.Objects, "the sweep examined nothing, which would read as a healthy broker")

	// The MQTT probe presents the same login.
	user, password, err := operatorLogin(cfg)
	require.NoError(t, err)
	require.Equal(t, natsauth.AdminUser, user)
	prev := mqttProbeBackoff
	mqttProbeBackoff = 200 * time.Millisecond
	t.Cleanup(func() { mqttProbeBackoff = prev })
	require.NoError(t, mqttProbe(fmt.Sprintf("127.0.0.1:%d", b.MQTTPort(t)), nil, user, password))
}

// The negative control for the test above: the shared login's name does not authenticate on a
// generated broker, so the test above can only pass by presenting the administrator's.
func TestTheSharedLoginDoesNotAuthenticateOnAGeneratedBroker(t *testing.T) {
	b := permtest.Start(t, haLoginInstance)
	cfg := administratorView(b)
	cfg.Auth.AreaLogins = false
	cfg.Auth.User, cfg.Auth.Password = natsauth.ServiceUser, "whatever"
	_, err := dialBroker(fmt.Sprintf("nats://%s:%d", b.Host, b.Port), cfg)
	require.Error(t, err)
}

func TestOperatorLoginRequiresTheAdministratorWhereThereAreLogins(t *testing.T) {
	// A broker with per-area logins and no readable administrator login is an error, never anonymous.
	_, _, err := operatorLogin(config.NatsConfiguration{Auth: config.NatsAuthConfiguration{AreaLogins: true}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "broker administrator")

	// A broker that predates the logins still connects as the document's user.
	u, p, err := operatorLogin(config.NatsConfiguration{Auth: config.NatsAuthConfiguration{User: "u", Password: "p"}})
	require.NoError(t, err)
	require.Equal(t, [2]string{"u", "p"}, [2]string{u, p})
}
