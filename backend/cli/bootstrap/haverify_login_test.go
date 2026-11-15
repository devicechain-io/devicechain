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

	// The MQTT probe does NOT present this login: see the test below.
}

// probeView is administratorView plus the one per-area login the broker accepts over MQTT, as
// deployedInstanceConfigWithLogins overlays it from the instance's Secrets.
func probeView(b *permtest.Broker) config.NatsConfiguration {
	cfg := administratorView(b)
	cfg.Auth.AreaPasswords[mqttPlatformPrincipal] = permtest.Password(natsauth.LoginUser(mqttPlatformPrincipal))
	return cfg
}

func fastProbeRetries(t *testing.T) {
	prev := mqttProbeBackoff
	mqttProbeBackoff = 100 * time.Millisecond
	t.Cleanup(func() { mqttProbeBackoff = prev })
}

// `dcctl ha verify --probe-mqtt` authenticates on a broker that holds its logins to their
// connection types the way every dcctl-built broker has since v1.1.0, with the MQTT-capable set
// taken from the function that renders it into the apply, so the probe and the grant cannot drift.
//
// Through v1.6 the probe presented the broker administrator's login and was refused on every such
// broker. The test broker used to leave every login unrestricted, which is how that went unseen;
// the negative control below is that exact refusal.
func TestTheMQTTProbeAuthenticatesOnABrokerThatHoldsLoginsToTheirConnectionTypes(t *testing.T) {
	st := &State{Values: map[string]string{"natsCalloutIssuerPublic": "AI"}} // a fresh install: no shared login
	capable := mqttCapableUsers(st)
	b := permtest.Start(t, haLoginInstance, permtest.WithMQTT(), permtest.WithConnectionTypes(capable...))
	cfg := probeView(b)
	addr := fmt.Sprintf("127.0.0.1:%d", b.MQTTPort(t))
	fastProbeRetries(t)

	user, _, err := mqttProbeLogin(cfg)
	require.NoError(t, err)
	require.Contains(t, capable, user, "the probe presents a login the apply does not let speak MQTT")
	// Through the function the command calls, so a call site that chose another login fails here.
	require.NoError(t, probeMqttAt(addr, cfg), "the probe was refused on a broker built the way dcctl builds it")

	// NEGATIVE CONTROL: the administrator's login, which is what the probe presented through v1.6,
	// is refused over MQTT by this same broker. So the pass above is the login's doing, not a broker
	// that lets anything in.
	adminUser, adminPassword, err := operatorLogin(cfg)
	require.NoError(t, err)
	err = mqttProbe(addr, nil, adminUser, adminPassword)
	require.Error(t, err, "the broker accepted a NATS-protocol-only login over MQTT; the restriction is not in force")
	require.Contains(t, err.Error(), "refused the MQTT probe's login")
	// ...while the same login still connects over the NATS protocol, so the refusal is the connection
	// type and not a dead login.
	nc, err := dialBroker(fmt.Sprintf("nats://%s:%d", b.Host, b.Port), cfg)
	require.NoError(t, err)
	nc.Close()
}

func TestTheMQTTProbeLoginFailsClosedWithoutItsPassword(t *testing.T) {
	// Per-area logins and no readable MQTT-capable one is an error naming it, never the
	// administrator's (refused over MQTT) or an anonymous connect.
	_, _, err := mqttProbeLogin(config.NatsConfiguration{Auth: config.NatsAuthConfiguration{
		AreaLogins:    true,
		AreaPasswords: map[string]string{config.NatsAdminPrincipal: "admin-pw"},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), mqttPlatformPrincipal)

	// A broker that predates the logins: the document's user, as every area connected then.
	u, p, err := mqttProbeLogin(config.NatsConfiguration{Auth: config.NatsAuthConfiguration{User: "u", Password: "p"}})
	require.NoError(t, err)
	require.Equal(t, [2]string{"u", "p"}, [2]string{u, p})
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
