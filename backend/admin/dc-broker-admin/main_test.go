// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestArgumentsAreRefusedWithAMessageThatSaysWhat(t *testing.T) {
	var out bytes.Buffer
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"no command":        {nil, nil, "usage"},
		"unknown command":   {[]string{"destroy"}, nil, "unknown command"},
		"no instance":       {[]string{"reconcile", "--areas", "device-state"}, nil, "DC_INSTANCE_ID"},
		"no areas":          {[]string{"reconcile"}, map[string]string{"DC_INSTANCE_ID": "i"}, "--areas is empty"},
		"only blank areas":  {[]string{"reconcile", "--areas", " , "}, map[string]string{"DC_INSTANCE_ID": "i"}, "--areas is empty"},
		"an unknown option": {[]string{"reconcile", "--nope"}, map[string]string{"DC_INSTANCE_ID": "i"}, "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), c.args, env(c.env), &out)
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
		})
	}
}

func TestVersionNeedsNothing(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"version"}, env(nil), &out))
	require.Contains(t, out.String(), "dc-broker-admin")
}

// instanceDocument writes the smallest instance document that points at the test broker.
func instanceDocument(t *testing.T, srv *natsserver.Server, areaLogins bool) string {
	t.Helper()
	addr := srv.Addr().(*net.TCPAddr)
	doc := map[string]any{
		"infrastructure": map[string]any{
			"nats": map[string]any{
				"hostname": addr.IP.String(),
				"port":     addr.Port,
				"auth":     map[string]any{"areaLogins": areaLogins},
			},
			"userManagement": map[string]any{"hostname": "user-management", "port": 8080},
		},
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "instance")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func startBroker(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	return srv
}

// The command end to end against a broker: it creates what the deployed areas open, says so, and
// a second run changes nothing.
func TestReconcileCreatesAndIsIdempotent(t *testing.T) {
	srv := startBroker(t)
	path := instanceDocument(t, srv, false)
	args := []string{"reconcile", "--config", path, "--areas", "command-delivery,device-state"}
	e := env(map[string]string{"DC_INSTANCE_ID": "tinst"})

	var first bytes.Buffer
	require.NoError(t, run(context.Background(), args, e, &first))
	require.Contains(t, first.String(), "created   stream "+messaging.StreamName("tinst", streams.DeviceCommands))
	require.Contains(t, first.String(), "reconciled ")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.StreamInfo(messaging.StreamName("tinst", streams.DeviceCommands))
	require.NoError(t, err)
	_, err = js.StreamInfo(messaging.StreamName("tinst", streams.ConnectorDispatch))
	require.ErrorIs(t, err, nats.ErrStreamNotFound, "an area that was not named has its streams created")

	var second bytes.Buffer
	require.NoError(t, run(context.Background(), args, e, &second))
	require.NotContains(t, second.String(), "created ")
	require.NotContains(t, second.String(), "updated ")
}

// A broker that has a login for every principal is never reconciled as the shared login: the
// administrator without its own is refused before it dials anything.
func TestWithoutItsOwnLoginTheAdministratorDoesNotConnect(t *testing.T) {
	srv := startBroker(t)
	path := instanceDocument(t, srv, true)
	args := []string{"reconcile", "--config", path, "--areas", "device-state"}

	err := run(context.Background(), args, env(map[string]string{"DC_INSTANCE_ID": "tinst"}), &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DC_NATS_PASSWORD")

	// Another principal's Secret is not the administrator's either.
	err = run(context.Background(), args, env(map[string]string{
		"DC_INSTANCE_ID": "tinst", "DC_NATS_USER": "dc-device-management-g1", "DC_NATS_PASSWORD": "x",
	}), &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not its own")
}

func TestAnUnreadableInstanceDocumentIsAnError(t *testing.T) {
	err := run(context.Background(), []string{"reconcile", "--config", filepath.Join(t.TempDir(), "absent"), "--areas", "device-state"},
		env(map[string]string{"DC_INSTANCE_ID": "tinst"}), &bytes.Buffer{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "instance configuration")
}

// A typo in --areas is refused with the message that names it, not a crash: the hook's log is what
// the operator reads.
func TestAnUnknownAreaIsRefusedByName(t *testing.T) {
	srv := startBroker(t)
	path := instanceDocument(t, srv, false)
	var out bytes.Buffer
	err := run(context.Background(), []string{"reconcile", "--config", path, "--areas", "device-state,no-such-area"},
		env(map[string]string{"DC_INSTANCE_ID": "tinst"}), &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), `"no-such-area" is not a platform functional area`)
}
