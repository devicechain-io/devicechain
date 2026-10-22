// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/kv"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// The identity account: where user-management keeps the buckets whose contents are bearer secrets
// (kv.Identity). It is reached over a SECOND connection of the same manager, authenticated as a
// login that user-management alone holds, because a connection belongs to exactly one account.
//
// 🔴 WHY AN ACCOUNT AND NOT A PERMISSION. A login that can create JetStream streams can source,
// mirror or subject-capture a bucket into a stream of its own and read the copy, and the server
// writes that copy past every user permission. So a deny list on the shared login closes nothing
// while the login administers streams. An account is the one boundary a stream does not cross, and
// nothing outside user-management needs these buckets, so nothing is shared between the accounts
// and there is no import or export to keep in step.

// IdentityLoginUser and IdentityAccountName are the identity account's login and account names.
// They are declared here, not in natsauth, because natsauth imports this package; natsauth.IdentityUser
// and natsauth.IdentityAccount are these same values, which is what the infrastructure module and
// dcctl read.
const (
	IdentityLoginUser   = "dc_identity"
	IdentityAccountName = "IDENTITY"
)

// ErrIdentityLoginMissing is what Initialize returns when the instance says the broker has the
// identity account and this service, which opened the connection for it, was given no password.
var ErrIdentityLoginMissing = errors.New("messaging: the instance configuration says the broker has an " +
	"identity account, and this service holds no login for it (DC_NATS_IDENTITY_PASSWORD is empty). " +
	"Refusing to keep the sign-in buckets in the shared account, where every service login can read them")

// ErrIdentityNotAvailable is what KeyValueStore returns for a bucket that lives in the identity
// account when this manager has no connection to it.
var ErrIdentityNotAvailable = errors.New("messaging: this bucket lives in the identity account, which this " +
	"service has no connection to; only user-management holds that login")

// identityLink is the open connection to the identity account.
type identityLink struct {
	nc        *nats.Conn
	js        nats.JetStreamContext
	requested *atomic.Bool
}

// UseIdentityLogin gives the manager the password for natsauth.IdentityUser, read by the caller
// from the environment. It must be called before Initialize, and only by the service that owns
// the identity buckets. An empty password is not "no login": when the instance configuration
// says the account exists, Initialize refuses to start without one (ErrIdentityLoginMissing).
func (nmgr *NatsManager) UseIdentityLogin(password string) {
	nmgr.identityLogin = &password
}

// identityRequired reports whether the instance configuration says the broker has the identity
// account.
func (nmgr *NatsManager) identityRequired() bool {
	return nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.IdentityAccount
}

// initializeIdentity opens the identity connection when this manager holds the login and the
// instance has the account. It runs at the end of ExecuteInitialize.
func (nmgr *NatsManager) initializeIdentity(ctx context.Context, natscfg config.NatsConfiguration, tlsConfig *tls.Config) error {
	if nmgr.identityLogin == nil || !nmgr.identityRequired() {
		return nil
	}
	password := *nmgr.identityLogin
	if password == "" {
		return ErrIdentityLoginMissing
	}
	area := nmgr.Microservice.FunctionalArea
	requested := new(atomic.Bool)
	opts := []nats.Option{
		nats.Name(area + "-identity"),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.UserInfo(IdentityLoginUser, password),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn().Str("area", area).Err(err).
				Msg("Disconnected from the NATS identity account; retrying indefinitely. Sign-in state is unavailable until it returns")
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info().Str("area", area).Str("server", nc.ConnectedUrl()).Msg("Reconnected to the NATS identity account")
		}),
		// The same terminal-close rule as the main connection: a close nobody asked for marks the
		// process not live, so the kubelet restarts it and it re-reads its login.
		nats.ClosedHandler(func(nc *nats.Conn) {
			if requested.Load() {
				return
			}
			cause := nc.LastError()
			if cause == nil {
				cause = errors.New("the client reported no error")
			}
			log.Error().Str("area", area).Err(cause).
				Msg("NATS identity-account connection CLOSED permanently and NOT as part of a shutdown; it will not reconnect")
			nmgr.Microservice.MarkNotLive(fmt.Errorf("NATS identity-account connection closed permanently and not by this process: %w", cause))
		}),
	}
	opts = append(opts, BrokerLivenessOptions(area+"-identity", func() {})...)
	if tlsConfig != nil {
		opts = append(opts, nats.Secure(tlsConfig))
	}
	url := nmgr.NatsUrl()
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return err
	}
	if err := waitForConnected(ctx, nc, nmgr.connectWaitOrDefault()); err != nil {
		closeConn(nc, requested)
		if ctx.Err() != nil {
			return err
		}
		if last := nc.LastError(); last != nil {
			err = fmt.Errorf("%w (last connection error: %w)", err, last)
		}
		return fmt.Errorf("%w: the identity account at %s: %w", ErrBrokerUnreachable, url, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		closeConn(nc, requested)
		return err
	}
	// The login can exist while the account has no JetStream (a broker built without it), which
	// would surface later as a bucket that cannot be created. Ask now, so the start fails naming it.
	if _, err := js.AccountInfo(nats.Context(ctx)); err != nil {
		closeConn(nc, requested)
		return fmt.Errorf("messaging: the identity account at %s has no usable JetStream: %w", url, err)
	}
	nmgr.identity = &identityLink{nc: nc, js: js, requested: requested}
	log.Info().Str("account", IdentityAccountName).Msg("Connected to the NATS identity account")
	return nil
}

// closeIdentity closes the identity connection as a close this manager asked for. It holds no
// subscriptions, so there is nothing to drain; Close flushes what is buffered.
func (nmgr *NatsManager) closeIdentity() {
	if l := nmgr.identity; l != nil && !l.nc.IsClosed() {
		closeConn(l.nc, l.requested)
	}
}

// kvContext selects the JetStream context a bucket is opened in, and the function that records it
// for the replication sampler.
//
// A bucket that lives in the identity account is opened ONLY there. When the instance has the
// account and this manager has no connection to it, the answer is an error, never the shared
// account: that fallback is the exposure this exists to remove.
func (nmgr *NatsManager) kvContext(logical string) (nats.JetStreamContext, func(string), error) {
	if kv.AccountFor(logical) == kv.Identity && nmgr.identityRequired() {
		if nmgr.identity == nil {
			return nil, nil, fmt.Errorf("%w (bucket %q)", ErrIdentityNotAvailable, logical)
		}
		return nmgr.identity.js, nmgr.trackIdentityKvBucket, nil
	}
	if nmgr.js == nil {
		return nil, nil, errors.New("messaging: JetStream context not initialized")
	}
	return nmgr.js, nmgr.trackKvBucket, nil
}

func (nmgr *NatsManager) trackIdentityKvBucket(bucket string) {
	name := kvStreamPrefix + bucket
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	for _, n := range nmgr.identityBucketNames {
		if n == name {
			return
		}
	}
	nmgr.identityBucketNames = append(nmgr.identityBucketNames, name)
}

// sampleIdentity reports the replication of the buckets opened in the identity account, through
// that account's own context: the main sampler's context cannot see them.
func (nmgr *NatsManager) sampleIdentity(ctx context.Context, seen *sampleSeen) {
	l := nmgr.identity
	if l == nil {
		return
	}
	nmgr.streamMu.Lock()
	buckets := append([]string(nil), nmgr.identityBucketNames...)
	nmgr.streamMu.Unlock()
	if len(buckets) == 0 {
		return
	}
	nmgr.metrics.sample(ctx, l.js, nil, nil, buckets, nil, nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered(), seen)
}

// IdentityBucketNames lists the concrete name of every bucket declared for the identity account,
// for an instance. The credential-attempt bucket is instance-prefixed; the others are global.
func IdentityBucketNames(instanceId string) []string {
	var out []string
	for _, b := range kv.All {
		if b.Account != kv.Identity {
			continue
		}
		if b.Name == kv.BucketCredentialAttempts {
			out = append(out, CredentialAttemptsBucketName(instanceId))
			continue
		}
		out = append(out, b.Name)
	}
	return out
}

// RetireSharedIdentityBuckets deletes the copies of the identity buckets that an earlier release
// created in the SHARED account, and returns the names it deleted.
//
// 🔴 THIS IS A DELIBERATE, DOCUMENTED INVALIDATION OF EVERY SESSION, NOT A MIGRATION. The tokens in
// those buckets were readable by every service login for as long as they existed, so carrying them
// into the new account would keep the exposure alive in a place that is no longer exposed, and
// leaving them behind would keep the readable copy. Deleting them means everyone signs in once
// more; short-lived access tokens already issued keep working until they expire. In-flight OAuth
// and single sign-on records (lifetimes of 60 seconds to ten minutes) and the sign-in backoff
// counters go with them. It is idempotent: a bucket that is not there is skipped.
//
// It does nothing unless the identity connection is open, so it can never delete a bucket whose
// replacement does not exist.
func (nmgr *NatsManager) RetireSharedIdentityBuckets() ([]string, error) {
	if nmgr.identity == nil || nmgr.js == nil {
		return nil, nil
	}
	// Deleting a bucket is administering it, and this reaches the broker without KeyValueStore,
	// so it asks the catalog itself: only an area declared on every identity bucket may.
	for _, b := range kv.All {
		if b.Account != kv.Identity {
			continue
		}
		if err := nmgr.requireBucket(b.Name); err != nil {
			return nil, err
		}
	}
	var deleted []string
	for _, name := range IdentityBucketNames(nmgr.Microservice.InstanceId) {
		err := nmgr.js.DeleteKeyValue(name)
		switch {
		case err == nil:
			deleted = append(deleted, name)
		case errors.Is(err, nats.ErrBucketNotFound), errors.Is(err, nats.ErrStreamNotFound):
		default:
			return deleted, fmt.Errorf("deleting the shared-account copy of bucket %q: %w", name, err)
		}
	}
	return deleted, nil
}
