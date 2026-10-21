// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The system-account login is held to an allow list. These are claims about the BROKER (what
// it refuses the login, and that what the login's consumers need still works), so they run
// against an embedded nats-server whose SYS user is configured the way the infrastructure
// module configures it.

func quotedList(subjects []string) string {
	q := make([]string, 0, len(subjects))
	for _, s := range subjects {
		q = append(q, fmt.Sprintf("%q", s))
	}
	return strings.Join(q, ", ")
}

// sysBroker starts a broker with an APP account and a SYS system account whose dc_sys user
// carries the allow list when restricted, and no permissions when not (the control).
func sysBroker(t *testing.T, restricted bool) *natsserver.Server {
	t.Helper()
	perms := ""
	if restricted {
		perms = fmt.Sprintf(", permissions: { publish: { allow: [%s] }, subscribe: { allow: [%s] } }",
			quotedList(SysPublishAllow()), quotedList(SysSubscribeAllow()))
	}
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "sys-perms-test"
jetstream { store_dir: %q }
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: svc} ] }
  SYS: { users: [ {user: dc_sys, password: sys%s} ] }
}
system_account: SYS
`, dir, perms)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

type sysTestConn struct {
	nc   *nats.Conn
	errs chan error
}

func dialSys(t *testing.T, srv *natsserver.Server) sysTestConn {
	t.Helper()
	errs := make(chan error, 32)
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(SysUser, "sys"),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case errs <- err:
			default:
			}
		}))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return sysTestConn{nc: nc, errs: errs}
}

// request sends one request and returns its reply, or the permission violation the broker
// reported, or the request's own error.
func (c sysTestConn) request(subject, body string) (*nats.Msg, error) {
	for len(c.errs) > 0 {
		<-c.errs
	}
	msg, err := c.nc.Request(subject, []byte(body), time.Second)
	if err != nil {
		select {
		case perr := <-c.errs:
			return nil, perr
		case <-time.After(250 * time.Millisecond):
		}
	}
	return msg, err
}

func isViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "permissions violation")
}

// What the login's two consumers (the presence tap and the session kick) use keeps working
// under the allow list.
func TestTheSysLoginKeepsWhatItsConsumersNeed(t *testing.T) {
	srv := sysBroker(t, true)
	sys := dialSys(t, srv)

	_, err := sys.request("$SYS.REQ.SERVER.PING", "{}")
	require.NoError(t, err, "the cluster ping")
	msg, err := sys.request(fmt.Sprintf("$SYS.REQ.SERVER.%s.CONNZ", srv.ID()), `{"offset":0,"limit":10}`)
	require.NoError(t, err, "one server's connection list")
	require.Contains(t, string(msg.Data), `"connections"`)
	// A kick of a connection id that does not exist reaches the handler, which says so: the
	// request was admitted.
	msg, err = sys.request(fmt.Sprintf("$SYS.REQ.SERVER.%s.KICK", srv.ID()), `{"cid":999999}`)
	require.NoError(t, err, "one server's kick")
	require.Contains(t, string(msg.Data), "no such client")

	// The advisories.
	got := make(chan struct{}, 1)
	sub, err := sys.nc.Subscribe(SysConnectSubject, func(*nats.Msg) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, sys.nc.Flush())
	_, err = nats.Connect(srv.ClientURL(), nats.UserInfo(ServiceUser, "svc"))
	require.NoError(t, err)
	select {
	case <-got:
	case err := <-sys.errs:
		t.Fatalf("subscribing to the connect advisory: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no connect advisory arrived")
	}
	_, err = sys.nc.Subscribe(SysDisconnectSubject, func(*nats.Msg) {})
	require.NoError(t, err)
	require.NoError(t, sys.nc.Flush())
	select {
	case err := <-sys.errs:
		t.Fatalf("subscribing to the disconnect advisory: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
}

// Everything else is refused, and the control (the same requests by an unrestricted login)
// shows the broker would otherwise answer them, so the refusal is the allow list's.
func TestTheSysLoginIsRefusedEverythingElse(t *testing.T) {
	// Requests that answer harmlessly, so the control can make them. ("$SYS.REQ.SERVER.PING.CONNZ"
	// is not here: the "*" in "$SYS.REQ.SERVER.*.CONNZ" matches PING, and the fan-out returns
	// the same connection lists one server at a time does, so admitting it grants nothing new.)
	readOnly := func(id string) []string {
		return []string{
			fmt.Sprintf("$SYS.REQ.SERVER.%s.VARZ", id),
			fmt.Sprintf("$SYS.REQ.SERVER.%s.SUBSZ", id),
			fmt.Sprintf("$SYS.REQ.SERVER.%s.JSZ", id),
			"$SYS.REQ.ACCOUNT.APP.CONNZ",
		}
	}
	t.Run("restricted", func(t *testing.T) {
		srv := sysBroker(t, true)
		sys := dialSys(t, srv)
		// Plus the ones that act on the server, which the control must not send.
		subjects := append(readOnly(srv.ID()),
			fmt.Sprintf("$SYS.REQ.SERVER.%s.LDM", srv.ID()),
			fmt.Sprintf("$SYS.REQ.SERVER.%s.RELOAD", srv.ID()))
		for _, s := range subjects {
			_, err := sys.request(s, "{}")
			require.True(t, isViolation(err), "%s must be a permissions violation, got %v", s, err)
		}
		require.True(t, srv.Running(), "the server is still up")
		// And it cannot read the system account wholesale.
		_, err := sys.nc.Subscribe("$SYS.>", func(*nats.Msg) {})
		require.NoError(t, err)
		require.NoError(t, sys.nc.Flush())
		select {
		case err := <-sys.errs:
			require.True(t, isViolation(err), "subscribing to $SYS.> must be refused, got %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("subscribing to $SYS.> was not refused")
		}
	})
	t.Run("control", func(t *testing.T) {
		srv := sysBroker(t, false)
		sys := dialSys(t, srv)
		for _, s := range readOnly(srv.ID()) {
			_, err := sys.request(s, "{}")
			require.NoError(t, err, "an unrestricted login is answered on %s", s)
		}
	})
}

// The infrastructure module renders the lists as literals (HCL cannot call Go). Hold the two
// in step, and hold the SYS user to them.
func TestSysPermissionsMatchTheInfrastructureModule(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "opentofu", "modules", "nats", "main.tf"))
	require.NoError(t, err)
	tf := string(raw)
	for name, want := range map[string][]string{
		"sys_publish_allow":   SysPublishAllow(),
		"sys_subscribe_allow": SysSubscribeAllow(),
	} {
		m := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*\[(.*)\]\s*$`).FindStringSubmatch(tf)
		require.NotNil(t, m, "main.tf must define local %s", name)
		require.Equal(t, quotedList(want), strings.TrimSpace(m[1]), "local %s must carry natsauth's list verbatim", name)
	}
	require.Regexp(t, `permissions\s*=\s*\{\s*publish\s*=\s*\{\s*allow\s*=\s*local\.sys_publish_allow\s*\},\s*subscribe\s*=\s*\{\s*allow\s*=\s*local\.sys_subscribe_allow\s*\}\s*\}`,
		tf, "the dc_sys user must be given both allow lists")
}
