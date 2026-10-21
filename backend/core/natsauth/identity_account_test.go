// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package natsauth

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// The sign-in buckets hold bearer secrets, so the question is what the SHARED service login can do
// to them. These tests run against an embedded nats-server (the version in go.mod) and ask it,
// rather than reading the configuration and concluding.
//
// Four ways to get at a bucket are tried, each from the shared service login:
//
//   - subscribe to the bucket's subjects ($KV.<bucket>.>), the way a subscribe deny would be meant to stop;
//   - read the bucket (a KV get, and a direct get on its backing stream);
//   - create a stream that SOURCES the bucket's backing stream;
//   - create a stream that MIRRORS it.
//
// The last two are the reason a deny list is not the fix: stream creation is not a subscription, the
// server copies the data on the account's behalf, and the copy is then an ordinary stream the login
// reads. Only a different ACCOUNT stops them, because a stream cannot name another account's stream
// without an import that nothing grants.
//
// The control runs the same body against the topology the previous release had, the buckets in the
// shared account beside the service login, and requires every one of the four to SUCCEED there. A
// body that fails in the split topology for a reason that is not the account (a typo in a subject,
// a harness that refuses everything) would pass the split test and fail the control.

const (
	idBucket        = "dc_refresh_tokens"
	idBucketStream  = "KV_dc_refresh_tokens"
	idSecretKey     = "jti-secret"
	idSecretValue   = "owner@example.com"
	idServicePw     = "svc"
	idIdentityLogin = IdentityUser
	idIdentityPw    = "idpw"
)

// exposure is what the shared service login managed to obtain.
type exposure struct {
	subscribed bool // saw the token's KV put on a core subscription to the bucket's subjects
	read       bool // read the token back with a KV get or a direct get
	sourced    bool // a stream it created with the bucket's stream as a source received the token
	mirrored   bool // a stream it created as a mirror of the bucket's stream received the token
}

func accountsBroker(t *testing.T, accounts string) *natsserver.Server {
	t.Helper()
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "identity-probe"
jetstream { store_dir: %q }
accounts {
%s
}
`, dir, accounts)), 0o600))
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

// splitTopology is the broker the infrastructure module renders: the sign-in buckets' owner in an
// account of its own.
func splitTopology(t *testing.T) *natsserver.Server {
	return accountsBroker(t, fmt.Sprintf(`
  APP: { jetstream: enabled, users: [ {user: %s, password: %s} ] }
  %s: { jetstream: enabled, users: [ {user: %s, password: %s} ] }
`, ServiceUser, idServicePw, IdentityAccount, idIdentityLogin, idIdentityPw))
}

// sharedTopology is the previous release's: the bucket owner and the service login in ONE account.
func sharedTopology(t *testing.T) *natsserver.Server {
	return accountsBroker(t, fmt.Sprintf(`
  APP: { jetstream: enabled, users: [ {user: %s, password: %s}, {user: %s, password: %s} ] }
`, ServiceUser, idServicePw, idIdentityLogin, idIdentityPw))
}

func connectAs(t *testing.T, srv *natsserver.Server, user, pw string) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(user, pw))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return nc, js
}

func streamHolds(js nats.JetStreamContext, name string) bool {
	info, err := js.StreamInfo(name)
	return err == nil && info.State.Msgs > 0
}

// probeBucket makes the owner create and fill the bucket, has the shared service login try all four
// ways in, and reports what worked.
func probeBucket(t *testing.T, srv *natsserver.Server) exposure {
	t.Helper()
	_, ownerJS := connectAs(t, srv, idIdentityLogin, idIdentityPw)
	bucket, err := ownerJS.CreateKeyValue(&nats.KeyValueConfig{Bucket: idBucket, TTL: time.Hour})
	require.NoError(t, err)

	svcNC, svcJS := connectAs(t, srv, ServiceUser, idServicePw)
	sub, err := svcNC.SubscribeSync("$KV." + idBucket + ".>")
	require.NoError(t, err)
	require.NoError(t, svcNC.Flush())
	// Creation may be refused outright (the stream it names does not exist in its account). That
	// is a result, not a failure of the harness: what matters is whether the token ever arrives.
	_, _ = svcJS.AddStream(&nats.StreamConfig{
		Name:    "probe_source",
		Sources: []*nats.StreamSource{{Name: idBucketStream}},
		Storage: nats.MemoryStorage,
	})
	_, _ = svcJS.AddStream(&nats.StreamConfig{
		Name:    "probe_mirror",
		Mirror:  &nats.StreamSource{Name: idBucketStream},
		Storage: nats.MemoryStorage,
	})

	_, err = bucket.Put(idSecretKey, []byte(idSecretValue))
	require.NoError(t, err)

	var got exposure
	// The waits are bounded and are for the ABSENCE case too: a copy that has not arrived in this
	// long is not on its way, because the same broker delivers the control's in milliseconds.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !(got.subscribed && got.read && got.sourced && got.mirrored) {
		if _, err := sub.NextMsg(50 * time.Millisecond); err == nil {
			got.subscribed = true
		}
		if kvh, err := svcJS.KeyValue(idBucket); err == nil {
			if e, err := kvh.Get(idSecretKey); err == nil && string(e.Value()) == idSecretValue {
				got.read = true
			}
		}
		if m, err := svcJS.GetLastMsg(idBucketStream, "$KV."+idBucket+"."+idSecretKey); err == nil && string(m.Data) == idSecretValue {
			got.read = true
		}
		got.sourced = got.sourced || streamHolds(svcJS, "probe_source")
		got.mirrored = got.mirrored || streamHolds(svcJS, "probe_mirror")
	}
	return got
}

// The fix: with the bucket in the identity account, none of the four works.
func TestTheServiceLoginCannotReachTheSignInBucketsInTheIdentityAccount(t *testing.T) {
	got := probeBucket(t, splitTopology(t))
	require.False(t, got.subscribed, "a subscription in the shared account must not see the identity account's KV puts")
	require.False(t, got.read, "the shared service login must not read a token from the identity account")
	require.False(t, got.sourced, "a stream sourcing the bucket's stream must receive nothing: the stream is not in its account")
	require.False(t, got.mirrored, "a stream mirroring the bucket's stream must receive nothing: the stream is not in its account")
}

// The negative control, and the reason the test above can fail: the previous release's topology,
// the same login and the same four attempts. Every one of them succeeds, so each assertion above
// is red for the reason it names and not because the harness refuses everything.
func TestTheServiceLoginReachesTheSignInBucketsWhenTheyShareItsAccount(t *testing.T) {
	got := probeBucket(t, sharedTopology(t))
	require.True(t, got.subscribed, "control: a core subscription sees the put when the bucket shares the account")
	require.True(t, got.read, "control: the service login reads the token when the bucket shares its account")
	require.True(t, got.sourced, "control: a stream sourcing the bucket's stream copies the token")
	require.True(t, got.mirrored, "control: a stream mirroring the bucket's stream copies the token")
}

// Sources can name an external stream by API prefix, which is how streams cross accounts WHEN an
// import exists. None does here, so the attempt is refused or inert. Pinned because it is the one
// cross-account route a stream has, and the claim "nothing is shared" is only as good as this.
func TestNoStreamCanBeSourcedAcrossIntoTheIdentityAccount(t *testing.T) {
	srv := splitTopology(t)
	_, ownerJS := connectAs(t, srv, idIdentityLogin, idIdentityPw)
	bucket, err := ownerJS.CreateKeyValue(&nats.KeyValueConfig{Bucket: idBucket, TTL: time.Hour})
	require.NoError(t, err)
	_, svcJS := connectAs(t, srv, ServiceUser, idServicePw)
	_, _ = svcJS.AddStream(&nats.StreamConfig{
		Name: "probe_external",
		Sources: []*nats.StreamSource{{
			Name:     idBucketStream,
			External: &nats.ExternalStream{APIPrefix: "$JS." + IdentityAccount + ".API", DeliverPrefix: "deliver.identity"},
		}},
		Storage: nats.MemoryStorage,
	})
	_, err = bucket.Put(idSecretKey, []byte(idSecretValue))
	require.NoError(t, err)
	time.Sleep(1500 * time.Millisecond)
	require.False(t, streamHolds(svcJS, "probe_external"), "no import exists, so an external source must stay empty")
}

// The infrastructure module renders the account and its login as literals (HCL cannot call Go).
// Hold the two in step: a login or account renamed on one side only would lock user-management out
// of a broker that is otherwise healthy.
func TestInfrastructureModuleCarriesTheIdentityNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "opentofu", "modules", "nats", "main.tf"))
	require.NoError(t, err)
	tf := string(raw)
	for name, want := range map[string]string{"identity_user": IdentityUser, "identity_account": IdentityAccount} {
		require.Regexp(t, regexp.MustCompile(`(?m)^\s*`+name+`\s*=\s*"`+regexp.QuoteMeta(want)+`"\s*$`), tf,
			"deploy/opentofu/modules/nats/main.tf must carry %s = %q", name, want)
	}
}
