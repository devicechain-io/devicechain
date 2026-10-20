// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The shared service login may not speak the device-events namespace. These are claims about
// the BROKER (which publish it refuses, and that the refusal is a permission violation that
// never reaches the stream), so they run against an embedded nats-server configured the way the
// infrastructure module configures the static service user.

const (
	denyInstance = "inst"
	denyTenant   = "acme"
	denyDevice   = "dev-1"
)

// denyBroker starts a broker whose service user carries the deny when withDeny, and none when
// not (the negative control: the same test body must see the forgery land). "dev" stands in for
// a device login: it is never denied, because a device's own grant is what lets it publish there.
func denyBroker(t *testing.T, withDeny bool) (*natsserver.Server, int) {
	t.Helper()
	perms := ""
	if withDeny {
		quoted := make([]string, 0, 2)
		for _, s := range ServicePublishDeny() {
			quoted = append(quoted, fmt.Sprintf("%q", s))
		}
		perms = fmt.Sprintf(", permissions: { publish: { deny: [%s] } }", strings.Join(quoted, ", "))
	}
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "deny-test"
jetstream { store_dir: %q }
mqtt { listen: "127.0.0.1:-1" }
accounts {
  APP: { jetstream: enabled, users: [
    {user: dc_service, password: svc%s},
    {user: dev, password: dev}
  ] }
}
`, dir, perms)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return srv, dctest.MQTTPort(t, srv)
}

type denyConn struct {
	nc   *nats.Conn
	errs chan error
}

func dialAs(t *testing.T, srv *natsserver.Server, user, pass string) denyConn {
	t.Helper()
	errs := make(chan error, 16)
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(user, pass),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case errs <- err:
			default:
			}
		}))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return denyConn{nc: nc, errs: errs}
}

// publish sends one message and returns the broker's permission-violation error, or nil when
// none arrived. The violation is reported asynchronously, so the connection is flushed and the
// error channel given a bounded wait either way.
func (c denyConn) publish(t *testing.T, subject string) error {
	t.Helper()
	for len(c.errs) > 0 {
		<-c.errs
	}
	require.NoError(t, c.nc.Publish(subject, []byte(`{"device":"dev-1"}`)))
	require.NoError(t, c.nc.Flush())
	select {
	case err := <-c.errs:
		return err
	case <-time.After(750 * time.Millisecond):
		return nil
	}
}

func captureStream(t *testing.T, srv *natsserver.Server) nats.JetStreamContext {
	t.Helper()
	js, err := dialAs(t, srv, "dc_service", "svc").nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "capture",
		Subjects: []string{messaging.DeviceEventsWildcard(denyInstance)},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)
	return js
}

func stored(t *testing.T, js nats.JetStreamContext) uint64 {
	t.Helper()
	info, err := js.StreamInfo("capture")
	require.NoError(t, err)
	return info.State.Msgs
}

func deviceEventsSubject() string {
	return messaging.DeviceEventsSubject(denyInstance, denyTenant, denyDevice)
}

func TestServiceLoginCannotPublishDeviceEvents(t *testing.T) {
	srv, _ := denyBroker(t, true)
	js := captureStream(t, srv)
	svc := dialAs(t, srv, "dc_service", "svc")

	for _, subject := range []string{deviceEventsSubject(), messaging.MqttMsgsSubject(deviceEventsSubject())} {
		err := svc.publish(t, subject)
		require.Error(t, err, "publish to %q by the service login must be refused", subject)
		require.Contains(t, strings.ToLower(err.Error()), "permissions violation", subject)
	}
	require.Zero(t, stored(t, js), "a refused publish must never reach the capture stream")
}

// The control: with the deny absent the same forgery lands, so the test above is red for the
// reason it claims and not because the harness refuses everything.
func TestServiceLoginDenyAbsentLetsDeviceEventsThrough(t *testing.T) {
	srv, _ := denyBroker(t, false)
	js := captureStream(t, srv)
	svc := dialAs(t, srv, "dc_service", "svc")

	require.NoError(t, svc.publish(t, deviceEventsSubject()))
	require.Eventually(t, func() bool { return stored(t, js) == 1 }, 5*time.Second, 25*time.Millisecond,
		"without the deny the service login's publish lands in the capture stream")
}

// Every publish a service legitimately makes stays allowed: the downlinks, the responses and
// reports the MQTT store forwarder republishes, and the platform's own streams. And a device's
// own publish, over the broker's MQTT gateway, is not affected by a deny on the service login.
func TestServiceLoginLegitimatePublishesStillWork(t *testing.T) {
	srv, mqttPort := denyBroker(t, true)
	js := captureStream(t, srv)
	svc := dialAs(t, srv, "dc_service", "svc")

	for _, suffix := range []string{
		messaging.SubjectDeviceCommands, messaging.SubjectDeviceDesired,
		messaging.SubjectCommandResponses, messaging.SubjectDeviceReports,
	} {
		subject := messaging.DeviceScopedSubject(denyInstance, denyTenant, suffix, denyDevice)
		require.NoError(t, svc.publish(t, subject), "service publish to %q must stay allowed", subject)
	}
	for _, subject := range []string{
		messaging.ScopedSubject(denyInstance, denyTenant, "inbound-events"),
		messaging.ScopedSubject(denyInstance, denyTenant, "failed-decode"),
	} {
		require.NoError(t, svc.publish(t, subject), "service publish to %q must stay allowed", subject)
	}

	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)).
		SetClientID("dev-1").SetUsername("dev").SetPassword("dev")
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(10*time.Second))
	require.NoError(t, tok.Error())
	t.Cleanup(func() { c.Disconnect(100) })
	topic := fmt.Sprintf("%s/%s/devices/%s/events", denyInstance, denyTenant, denyDevice)
	pub := c.Publish(topic, 1, false, `{"device":"dev-1"}`)
	require.True(t, pub.WaitTimeout(10*time.Second))
	require.NoError(t, pub.Error())
	require.Eventually(t, func() bool { return stored(t, js) == 1 }, 5*time.Second, 25*time.Millisecond,
		"a device's own MQTT publish must still be captured")
}

// The infrastructure module renders the deny as literals (HCL cannot call Go). Hold the two in
// step: the module must carry exactly ServicePublishDeny and attach it to the service user.
func TestInfrastructureModuleCarriesTheServiceDeny(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "opentofu", "modules", "nats", "main.tf"))
	require.NoError(t, err)
	tf := string(raw)

	quoted := make([]string, 0, 2)
	for _, s := range ServicePublishDeny() {
		quoted = append(quoted, fmt.Sprintf("%q", s))
	}
	require.Contains(t, tf, "service_publish_deny = ["+strings.Join(quoted, ", ")+"]",
		"deploy/opentofu/modules/nats/main.tf must carry natsauth.ServicePublishDeny verbatim")
	require.Contains(t, tf, "permissions = { publish = { deny = local.service_publish_deny } }",
		"the service user must be given the deny")
}
