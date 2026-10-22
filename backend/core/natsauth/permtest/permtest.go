// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package permtest starts an in-process nats-server whose users hold exactly the permissions
// natsauth generates for them, which is what makes an area's permission test a test of the
// grant the real broker is given rather than of a copy of it.
//
// The configuration is built from natsauth.ServiceUsers, the same value dcctl renders into the
// real broker, and goes through nats-server's own configuration parser, so the test also covers
// the shape of the permissions block (a key the server does not read would be a login with no
// restriction, which is the failure that matters).
//
// A permission violation is recorded from the server's own log, because the client sees one only
// as a request that times out. An area's test starts the broker, runs the area's real wiring
// against it and asserts Violations is empty; the negative control removes one declared subject
// and asserts the violation appears.
package permtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// Broker is a running permission-enforcing test broker.
type Broker struct {
	Host string
	Port uint32

	srv      *natsserver.Server
	instance string
	mu       sync.Mutex
	lines    []string
}

// settings is what an Option adjusts before the broker starts.
type settings struct {
	users map[string]map[string]natsauth.Permissions
	extra map[string]any
	// bare leaves the broker's streams and buckets uncreated (see WithoutReconcile).
	bare bool
}

// Option adjusts the generated users, or the server configuration around them.
type Option func(*settings)

// OpenUser is the username of the unrestricted login WithOpenUser adds.
const OpenUser = "open-test-login"

// WithOpenUser adds an APP login with NO permissions block, which nats-server reads as unrestricted.
// It stands in for the party on the other end of a request an area answers (the broker's own
// authorization request, a requester in another area) without that party's grant being the thing
// under test. Never an area's login: Connect uses the area's own.
func WithOpenUser() Option {
	return func(s *settings) { s.users[natsauth.AppAccount][OpenUser] = natsauth.Permissions{} }
}

// WithoutReconcile starts the broker without running the broker administrator, so a test sees
// what a service finds when nobody has created its streams (the drifted-broker case).
func WithoutReconcile() Option { return func(s *settings) { s.bare = true } }

// WithMQTT turns the MQTT gateway on at an ephemeral port (see Broker.MQTTPort), the way the
// deployed broker runs it.
func WithMQTT() Option {
	return func(s *settings) { s.extra["mqtt"] = map[string]any{"host": "127.0.0.1", "port": -1} }
}

// Without removes one subject from every login's publish allow-list. It is the negative control:
// a test that removes a subject its area uses must see the violation, or the zero-violation
// result proves nothing.
func Without(subject string) Option {
	return func(set *settings) {
		for _, byUser := range set.users {
			for u, p := range byUser {
				if p.Publish != nil {
					p.Publish.Allow = removed(p.Publish.Allow, subject)
				}
				byUser[u] = p
			}
		}
	}
}

// WithoutSubscribe is Without for the subscribe allow-list.
func WithoutSubscribe(subject string) Option {
	return func(set *settings) {
		for _, byUser := range set.users {
			for u, p := range byUser {
				if p.Subscribe != nil {
					p.Subscribe.Allow = removed(p.Subscribe.Allow, subject)
				}
				byUser[u] = p
			}
		}
	}
}

func removed(in []string, subject string) []string {
	var out []string
	for _, s := range in {
		if s != subject {
			out = append(out, s)
		}
	}
	return out
}

// Password is the password of every login this broker mints.
func Password(user string) string { return "pw-" + user }

// Start runs a broker for one instance whose APP users are the generated logins and whose SYS
// account holds the generated system login. It is shut down when the test ends.
func Start(t testing.TB, instance string, opts ...Option) *Broker {
	t.Helper()
	users, err := natsauth.ServiceUsers(instance)
	if err != nil {
		t.Fatalf("generating the broker permissions: %v", err)
	}
	set := &settings{users: users, extra: map[string]any{}}
	for _, o := range opts {
		o(set)
	}
	accounts := map[string]any{}
	for account, byUser := range users {
		var list []map[string]any
		for u, p := range byUser {
			list = append(list, map[string]any{"user": u, "password": Password(u), "permissions": p})
		}
		acct := map[string]any{"users": list}
		if account == natsauth.AppAccount {
			acct["jetstream"] = "enabled"
		}
		accounts[account] = acct
	}
	// The IDENTITY account, as the deployed broker has it: its one login is held to nothing inside
	// the account, because the account is the boundary.
	accounts[natsauth.IdentityAccount] = map[string]any{
		"jetstream": "enabled",
		"users":     []map[string]any{{"user": natsauth.IdentityUser, "password": Password(natsauth.IdentityUser)}},
	}
	cfg := map[string]any{
		"host":           "127.0.0.1",
		"port":           -1,
		"jetstream":      map[string]any{"store_dir": dctest.JetStreamStoreDir(t)},
		"accounts":       accounts,
		"system_account": natsauth.SystemAccount,
	}
	for k, v := range set.extra {
		cfg[k] = v
	}
	// Not json.Marshal: it writes ">" as >, which nats-server's configuration parser refuses.
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nats.conf")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := natsserver.ProcessConfigFile(path)
	if err != nil {
		t.Fatalf("the generated broker configuration is not one nats-server reads: %v", err)
	}
	o.NoLog, o.NoSigs = false, true
	srv, err := natsserver.NewServer(o)
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{srv: srv, instance: instance}
	srv.SetLoggerV2(b, false, false, false)
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("permission-enforcing nats server not ready")
	}
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	b.Host, b.Port = u.Hostname(), uint32(port)
	if !set.bare {
		// What the chart's pre-install hook does before any service starts: the broker
		// administrator creates every stream and bucket, over its own login, so a service under
		// test finds the broker as a deployed one is found and, since it holds no right to create
		// anything, cannot be passing because it did.
		b.Reconcile(t, streams.PlatformAreas...)
	}
	return b
}

// AdminNatsConfiguration is what the broker administrator is given to connect to this broker as
// its own login. The ceilings are small: the administrator creates every stream of every area at
// once, and a test machine's disk is not a production volume.
func (b *Broker) AdminNatsConfiguration() config.NatsConfiguration {
	user := natsauth.AdminUser
	return config.NatsConfiguration{
		Hostname:           b.Host,
		Port:               b.Port,
		Auth:               config.NatsAuthConfiguration{User: user, Password: Password(user), IdentityAccount: true},
		StreamMaxBytes:     16 << 20,
		StreamMaxBytesCold: 4 << 20,
		KvStateMaxBytes:    4 << 20,
		KvCacheMaxBytes:    2 << 20,
	}
}

// Reconcile runs the broker administrator over this broker for the given areas, exactly as the
// hook Job does, and returns what it did. The test fails if it cannot.
func (b *Broker) Reconcile(t testing.TB, areas ...string) *messaging.ReconcileReport {
	t.Helper()
	ms := &core.Microservice{InstanceId: b.instance}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = b.AdminNatsConfiguration()
	admin := messaging.NewBrokerAdmin(ms)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := admin.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("connecting the broker administrator: %v", err)
	}
	defer admin.Conn().Close()
	report, err := admin.ReconcileBroker(ctx, areas)
	if err != nil {
		t.Fatalf("the broker administrator could not reconcile the broker under its generated permissions: %v", err)
	}
	return report
}

// NatsConfiguration is what an area's microservice is given to connect to this broker as its own
// login.
func (b *Broker) NatsConfiguration(area string) config.NatsConfiguration {
	user := natsauth.LoginUser(area)
	return config.NatsConfiguration{
		Hostname: b.Host,
		Port:     b.Port,
		Auth:     config.NatsAuthConfiguration{User: user, Password: Password(user)},
	}
}

// Connect opens a plain connection as area's own login, with the area's inbox prefix, for a test
// that has to act as ANOTHER area (a producer the area under test reads from). It is closed when
// the test ends.
func (b *Broker) Connect(t testing.TB, area string) *nats.Conn {
	t.Helper()
	user := natsauth.LoginUser(area)
	nc, err := nats.Connect(b.srv.ClientURL(), nats.UserInfo(user, Password(user)),
		nats.CustomInboxPrefix(messaging.InboxPrefix(area)))
	if err != nil {
		t.Fatalf("connecting as %s: %v", area, err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// MQTTPort is the port of the MQTT gateway started by WithMQTT.
func (b *Broker) MQTTPort(t testing.TB) int {
	t.Helper()
	return dctest.MQTTPort(t, b.srv)
}

// ConnectOpen opens a connection as the unrestricted login WithOpenUser added.
func (b *Broker) ConnectOpen(t testing.TB) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(b.srv.ClientURL(), nats.UserInfo(OpenUser, Password(OpenUser)))
	if err != nil {
		t.Fatalf("connecting as the open login: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// Violations are the permission violations the server logged so far.
func (b *Broker) Violations() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range b.lines {
		// The server logs "Publish Violation - User ..., Subject ..." and "Subscription Violation -
		// ..." (the client is sent the "Permissions Violation for ..." text, which the server does not log).
		if strings.Contains(l, "Violation") {
			out = append(out, l)
		}
	}
	return out
}

// RequireNoViolations fails the test if the server refused anything.
func (b *Broker) RequireNoViolations(t testing.TB) {
	t.Helper()
	if v := b.Violations(); len(v) > 0 {
		t.Fatalf("the broker refused %d request(s) under the generated permissions:\n  %s", len(v), strings.Join(v, "\n  "))
	}
}

func (b *Broker) record(format string, v ...any) {
	b.mu.Lock()
	b.lines = append(b.lines, fmt.Sprintf(format, v...))
	b.mu.Unlock()
}

// Logger implementation: nats-server's log, kept in memory.
func (b *Broker) Noticef(f string, v ...any) { b.record(f, v...) }
func (b *Broker) Warnf(f string, v ...any)   { b.record(f, v...) }
func (b *Broker) Fatalf(f string, v ...any)  { b.record(f, v...) }
func (b *Broker) Errorf(f string, v ...any)  { b.record(f, v...) }
func (b *Broker) Debugf(f string, v ...any)  {}
func (b *Broker) Tracef(f string, v ...any)  {}
